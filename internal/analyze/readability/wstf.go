package readability

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// ---------------------------------------------------------------------------
// Score direction
//
// Flesch and Amstad run 0–100 with HIGHER meaning EASIER. Every grade-level
// formula in this package — the four Wiener Sachtextformeln, LIX, and the
// English pack in english.go — runs the other way: the number is a school
// grade, so LOWER means EASIER. A consumer that treats the two families alike
// (a CI gate, a dashboard threshold, an LLM summarising the output) inverts its
// own verdict.
//
// analyze.Metric has no Direction field and that registry is not ours to widen,
// so the direction lives here instead: an exported lookup keyed by the metric's
// canonical registry name. It is a hand-maintained table rather than something
// parsed out of the Scale string, because a threshold check must not depend on
// prose that is written in two languages and free to be reworded.
//
// Adding a metric to this package means adding one line here.
// ---------------------------------------------------------------------------

// Direction says which way a metric's score runs.
type Direction string

const (
	// HigherIsEasier marks a reading-ease score (Flesch, Amstad): 0–100, up is
	// easier. A "must be at least X" gate is meaningful; "at most X" is not.
	HigherIsEasier Direction = "higher_is_easier"
	// LowerIsEasier marks a grade-level score (WSTF, LIX, Flesch-Kincaid,
	// Gunning Fog, SMOG, Coleman-Liau, ARI): the number is a schooling level,
	// down is easier. A "must be at most X" gate is meaningful; "at least X" is
	// not.
	LowerIsEasier Direction = "lower_is_easier"
)

// directions maps every metric this binary registers to its direction. Keys are
// canonical registry names; aliases resolve through the registry in DirectionOf.
var directions = map[string]Direction{
	// Reading ease — see flesch.go and amstad.go.
	"flesch": HigherIsEasier,
	"amstad": HigherIsEasier,
	// German school grades — this file.
	"wstf1": LowerIsEasier,
	"wstf2": LowerIsEasier,
	"wstf3": LowerIsEasier,
	"wstf4": LowerIsEasier,
	// Language-agnostic — lix.go.
	"lix": LowerIsEasier,
	// US grade levels — english.go.
	"flesch-kincaid": LowerIsEasier,
	"gunning-fog":    LowerIsEasier,
	"smog":           LowerIsEasier,
	"coleman-liau":   LowerIsEasier,
	"ari":            LowerIsEasier,
}

// DirectionOf reports which way a metric's score runs, resolving aliases
// ("fkgl", "wstf", "fre-de") through the registry. The second return value is
// false for a metric that has not declared a direction — a caller gating on the
// score should treat that as "unknown" rather than assume, and the returned
// Direction is only a conservative default.
func DirectionOf(name string) (Direction, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if d, ok := directions[key]; ok {
		return d, true
	}
	if m, ok := analyze.Get(key); ok {
		if d, ok := directions[strings.ToLower(m.Name)]; ok {
			return d, true
		}
	}
	return HigherIsEasier, false
}

// ---------------------------------------------------------------------------
// Grade-level bands
// ---------------------------------------------------------------------------

// gradeBand is one row of a lower-is-easier level table: it owns every score up
// to max, the rows being ordered from the easiest up. The last row must carry
// +Inf so every score finds a label.
type gradeBand struct {
	max   float64
	level string
	grade string
}

// classifyGrade maps a lower-is-easier score onto a grade-level table. It is the
// mirror of classify(), which walks a higher-is-easier table downwards and
// clamps to 0–100; grade levels have no upper bound worth clamping to, so a
// tortured text keeps its grade 27.
//
// inclusiveMax says whether the boundary belongs to the easier band or the
// harder one, because the published tables disagree: the Wiener Sachtextformel
// bands are written "≤6 sehr leicht", LIX's as "<25 very easy".
func classifyGrade(bands []gradeBand, score float64, inclusiveMax bool) (level, grade string) {
	for _, b := range bands {
		if score < b.max || (inclusiveMax && score == b.max) {
			return b.level, b.grade
		}
	}
	last := bands[len(bands)-1]
	return last.level, last.grade
}

// pctOfWords is a share of the document's words, in percent, guarded against a
// zero denominator.
func pctOfWords(n, words int) float64 {
	if words == 0 {
		return 0
	}
	return 100 * float64(n) / float64(words)
}

// msPct is MS: the share of words with three or more syllables ("mehrsilbige
// Wörter"), in percent.
func msPct(d *textproc.Doc) float64 { return pctOfWords(d.Stats.PolysyllabicWords, d.Stats.Words) }

// iwPct is IW: the share of words longer than six characters ("lange Wörter"),
// in percent.
func iwPct(d *textproc.Doc) float64 { return pctOfWords(d.Stats.LongWords, d.Stats.Words) }

// esPct is ES: the share of one-syllable words ("einsilbige Wörter"), in
// percent.
func esPct(d *textproc.Doc) float64 { return pctOfWords(d.Stats.MonosyllabicWords, d.Stats.Words) }

// ---------------------------------------------------------------------------
// Wiener Sachtextformel
// ---------------------------------------------------------------------------

// wstfScale states the direction in the metric's own language. The inversion
// against Flesch/Amstad is the whole reason this string is explicit: a WSTF of 4
// is a children's book and a WSTF of 15 is a court ruling. It also names the
// calibrated range, because the formulas are linear and will happily return 0.4
// for a short simple sentence — see wstfMinGrade.
const wstfScale = "4–15 Schulstufe, niedriger ist leichter; Werte außerhalb 4–15 liegen unter bzw. über der Skala"

// The Wiener Sachtextformeln are calibrated to return a Schulstufe, and the
// Austrian school system it was built for runs from the 4th year to university
// entry. The formulas are unbounded linear combinations, so a two-clause
// sentence of short words lands at 0.4 and a 60-word legal period at 20 —
// neither is a school grade. Scores are still reported raw (this package never
// clamps; see the package comment, and an Amstad score of −32.9 stands as-is),
// but a score outside the range is labelled as out of range and carries a note,
// so "0.0 / sehr leicht" can never be mistaken for a measured grade zero.
const (
	wstfMinGrade = 4.0
	wstfMaxGrade = 15.0
)

// wstfBands are the school-grade bands, easiest first. The boundaries are
// inclusive at the top ("≤6 ist sehr leicht"), so a score of exactly 8.0 reads
// "leicht" rather than "mittel".
var wstfBands = []gradeBand{
	{6, "sehr leicht", "4.–6. Schulstufe"},
	{8, "leicht", "6.–8. Schulstufe"},
	{10, "mittel", "8.–10. Schulstufe"},
	{12, "schwer", "10.–12. Schulstufe"},
	{math.Inf(1), "sehr schwer", "Studium"},
}

// wstfCoeff are one variant's published coefficients. A zero coefficient means
// the variant does not use that input at all — WSTF3 drops IW and ES, WSTF4
// keeps only MS and SL — and Extra reports only the inputs that were used.
type wstfCoeff struct {
	ms    float64 // weight on % polysyllabic words
	sl    float64 // weight on average sentence length
	iw    float64 // weight on % words longer than six characters
	es    float64 // weight on % monosyllabic words (negative: short words help)
	konst float64 // additive constant
}

// The four variants as published by Bamberger & Vanecek (1984).
var (
	wstf1Coeff = wstfCoeff{ms: 0.1935, sl: 0.1672, iw: 0.1297, es: -0.0327, konst: -0.875}
	wstf2Coeff = wstfCoeff{ms: 0.2007, sl: 0.1682, iw: 0.1373, konst: -2.779}
	wstf3Coeff = wstfCoeff{ms: 0.2963, sl: 0.1905, konst: -1.1144}
	wstf4Coeff = wstfCoeff{ms: 0.2744, sl: 0.2656, konst: -1.693}
)

// WSTF1 is the general-purpose Wiener Sachtextformel and what the `wstf` alias
// resolves to:
//
//	0.1935×MS + 0.1672×SL + 0.1297×IW − 0.0327×ES − 0.875
//
// Richard Bamberger and Erich Vanecek derived the four formulas for Austrian and
// German school texts: the result is the school grade (Schulstufe) at which the
// text can be read, so unlike Flesch and Amstad a LOWER number means an EASIER
// text. WSTF1 uses all four inputs and is the one to reach for by default.
func WSTF1(d *textproc.Doc) (analyze.Result, error) {
	return wstf(d, "wstf1", "Wiener Sachtextformel 1", wstf1Coeff)
}

// WSTF2 drops the monosyllable term:
//
//	0.2007×MS + 0.1682×SL + 0.1373×IW − 2.779
func WSTF2(d *textproc.Doc) (analyze.Result, error) {
	return wstf(d, "wstf2", "Wiener Sachtextformel 2", wstf2Coeff)
}

// WSTF3 needs only syllable counts and sentence length:
//
//	0.2963×MS + 0.1905×SL − 1.1144
func WSTF3(d *textproc.Doc) (analyze.Result, error) {
	return wstf(d, "wstf3", "Wiener Sachtextformel 3", wstf3Coeff)
}

// WSTF4 is the shortest variant, weighting sentence length most heavily:
//
//	0.2744×MS + 0.2656×SL − 1.693
func WSTF4(d *textproc.Doc) (analyze.Result, error) {
	return wstf(d, "wstf4", "Wiener Sachtextformel 4", wstf4Coeff)
}

// wstf evaluates one variant. The four differ only in their coefficients, so
// they share a body: a transcription error in a constant is then visible in one
// table rather than hidden in four near-identical functions.
func wstf(d *textproc.Doc, name, title string, c wstfCoeff) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr(name)
	}
	ms, sl, iw, es := msPct(d), asl(d), iwPct(d), esPct(d)
	// Band from the rounded score, so the printed number and its label can never
	// contradict each other at a boundary — see flesch.go.
	score := round(c.ms*ms+c.sl*sl+c.iw*iw+c.es*es+c.konst, 1)
	level, grade := classifyGrade(wstfBands, score, true)

	ex := map[string]any{
		"ms":                 round(ms, 2),
		"sl":                 round(sl, 2),
		"words":              d.Stats.Words,
		"sentences":          d.Stats.Sentences,
		"polysyllabic_words": d.Stats.PolysyllabicWords,
	}
	if c.iw != 0 {
		ex["iw"] = round(iw, 2)
		ex["long_words"] = d.Stats.LongWords
	}
	if c.es != 0 {
		ex["es"] = round(es, 2)
		ex["monosyllabic_words"] = d.Stats.MonosyllabicWords
	}
	// Out of the calibrated 4–15 range the labels above would be misleading on
	// their own: relabel and say so in Extra rather than clamp the score.
	switch {
	case score < wstfMinGrade:
		level, grade = "unter der Skala", "unter der 4. Schulstufe"
		ex["note"] = fmt.Sprintf(
			"score %s is below the calibrated range %g–%g: the text is easier than the Schulstufe scale measures, not a school grade.",
			strconv.FormatFloat(score, 'f', -1, 64), wstfMinGrade, wstfMaxGrade)
	case score > wstfMaxGrade:
		level, grade = "über der Skala", "über der 12. Schulstufe"
		ex["note"] = fmt.Sprintf(
			"score %s is above the calibrated range %g–%g: the text is harder than the Schulstufe scale measures, not a school grade.",
			strconv.FormatFloat(score, 'f', -1, 64), wstfMinGrade, wstfMaxGrade)
	}

	return analyze.Result{
		Metric:   name,
		Title:    title,
		Score:    score,
		Level:    level,
		Grade:    grade,
		Scale:    wstfScale,
		Language: language(d, textproc.LangGerman),
		Extra:    ex,
	}, nil
}

func init() {
	// Note the direction in every description: `text metrics list` is where an
	// agent discovers what this binary measures, and the number it will read
	// back runs the opposite way to flesch and amstad on the line above.
	analyze.Register(analyze.Metric{
		Name:        "wstf1",
		Aliases:     []string{"wstf", "wiener-sachtextformel", "wiener"},
		Title:       "Wiener Sachtextformel 1",
		Description: "German school grade (Bamberger/Vanecek 1984): 0.1935×MS + 0.1672×SL + 0.1297×IW − 0.0327×ES − 0.875, 4–15, LOWER is easier.",
		Languages:   []string{string(textproc.LangGerman)},
		Compute:     WSTF1,
	})
	analyze.Register(analyze.Metric{
		Name:        "wstf2",
		Aliases:     []string{"wiener-sachtextformel-2"},
		Title:       "Wiener Sachtextformel 2",
		Description: "German school grade, without the monosyllable term: 0.2007×MS + 0.1682×SL + 0.1373×IW − 2.779, LOWER is easier.",
		Languages:   []string{string(textproc.LangGerman)},
		Compute:     WSTF2,
	})
	analyze.Register(analyze.Metric{
		Name:        "wstf3",
		Aliases:     []string{"wiener-sachtextformel-3"},
		Title:       "Wiener Sachtextformel 3",
		Description: "German school grade from syllables and sentence length: 0.2963×MS + 0.1905×SL − 1.1144, LOWER is easier.",
		Languages:   []string{string(textproc.LangGerman)},
		Compute:     WSTF3,
	})
	analyze.Register(analyze.Metric{
		Name:        "wstf4",
		Aliases:     []string{"wiener-sachtextformel-4"},
		Title:       "Wiener Sachtextformel 4",
		Description: "German school grade, shortest variant: 0.2744×MS + 0.2656×SL − 1.693, LOWER is easier.",
		Languages:   []string{string(textproc.LangGerman)},
		Compute:     WSTF4,
	})
}
