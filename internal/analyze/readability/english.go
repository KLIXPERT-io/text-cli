package readability

import (
	"fmt"
	"math"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// The English grade-level pack: five formulas that answer "how many years of
// schooling does this text assume?" rather than "how easy is it?". They all
// invert the flesch.go direction — LOWER is EASIER — which is recorded for
// callers in the directions table in wstf.go.
//
// They disagree by design, and running several is the point: Flesch-Kincaid and
// ARI read sentence length and word length, Gunning Fog and SMOG read the share
// of long words, Coleman-Liau reads characters and never guesses at syllables.
// A document that all five put at grade 9 is genuinely a grade-9 document; a
// wide spread means one of the inputs (a table of numbers, a page of acronyms,
// a code block) is distorting the count.

// gradeScale is the shared direction string for the US grade-level formulas.
const gradeScale = "US grade level (1–18+), lower is easier"

// usGradeBands map a grade level onto the schooling it implies, easiest first.
// The boundaries are inclusive at the top: grade 12.0 is still "high school".
var usGradeBands = []gradeBand{
	{6, "very easy", "elementary school (up to 6th grade)"},
	{9, "easy", "middle school (7th–9th grade)"},
	{12, "medium", "high school (10th–12th grade)"},
	{16, "difficult", "college"},
	{math.Inf(1), "very difficult", "college graduate"},
}

// smogMinSentences is the sample size McLaughlin's calibration assumes: SMOG was
// derived from 30-sentence samples (10 each from the start, middle and end).
const smogMinSentences = 30

// awl is characters per word, preferring the value textproc computed and falling
// back to the raw counts, like asl and asw in readability.go. "Characters" here
// are the letters of the words themselves — punctuation and spaces are not
// counted — which is what Coleman-Liau and ARI were calibrated on.
func awl(d *textproc.Doc) float64 {
	if d.Stats.AvgWordLength != 0 {
		return d.Stats.AvgWordLength
	}
	if d.Stats.Words == 0 {
		return 0
	}
	return float64(d.Stats.Characters) / float64(d.Stats.Words)
}

// englishGrade assembles the common part of a grade-level result.
func englishGrade(d *textproc.Doc, metric, title string, score float64, extra map[string]any) analyze.Result {
	// Band from the rounded score — see flesch.go.
	s := round(score, 1)
	level, grade := classifyGrade(usGradeBands, s, true)
	return analyze.Result{
		Metric:   metric,
		Title:    title,
		Score:    s,
		Level:    level,
		Grade:    grade,
		Scale:    gradeScale,
		Language: language(d, textproc.LangEnglish),
		Extra:    extra,
	}
}

// FleschKincaid computes the Flesch-Kincaid Grade Level:
//
//	0.39×ASL + 11.8×ASW − 15.59
//
// It is Flesch Reading Ease rescaled onto US grades for the US Navy (Kincaid et
// al. 1975): the same two inputs, read the other way round.
func FleschKincaid(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("flesch-kincaid")
	}
	score := 0.39*asl(d) + 11.8*asw(d) - 15.59
	return englishGrade(d, "flesch-kincaid", "Flesch-Kincaid Grade Level", score, map[string]any{
		"asl":       round(asl(d), 2),
		"asw":       round(asw(d), 2),
		"words":     d.Stats.Words,
		"sentences": d.Stats.Sentences,
		"syllables": d.Stats.Syllables,
	}), nil
}

// GunningFog computes Robert Gunning's Fog Index:
//
//	0.4 × (ASL + 100 × polysyllabicWords/words)
//
// Gunning's "hard words" are words of three or more syllables, which
// textproc counts as PolysyllabicWords.
func GunningFog(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("gunning-fog")
	}
	ms := msPct(d) // 100 × polysyllabic/words
	score := 0.4 * (asl(d) + ms)
	return englishGrade(d, "gunning-fog", "Gunning Fog Index", score, map[string]any{
		"asl":                round(asl(d), 2),
		"ms":                 round(ms, 2),
		"polysyllabic_words": d.Stats.PolysyllabicWords,
		"words":              d.Stats.Words,
		"sentences":          d.Stats.Sentences,
	}), nil
}

// SMOG computes McLaughlin's Simple Measure of Gobbledygook:
//
//	1.0430 × sqrt(polysyllabicWords × 30/sentences) + 3.1291
//
// SMOG is calibrated on samples of at least 30 sentences; on a shorter document
// the 30/sentences scaling amplifies every polysyllabic word, so a two-sentence
// abstract with one long word reads as college level. Rather than refuse to
// score — a CI pipeline wants a number for every row — the result carries a
// "note" in Extra saying the sample was too short to trust.
func SMOG(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("smog")
	}
	poly, sentences := float64(d.Stats.PolysyllabicWords), float64(d.Stats.Sentences)
	score := 1.0430*math.Sqrt(poly*30/sentences) + 3.1291
	extra := map[string]any{
		"polysyllabic_words": d.Stats.PolysyllabicWords,
		"words":              d.Stats.Words,
		"sentences":          d.Stats.Sentences,
	}
	if d.Stats.Sentences < smogMinSentences {
		extra["note"] = fmt.Sprintf(
			"SMOG is calibrated on samples of %d+ sentences; this document has %d, so the score is indicative only.",
			smogMinSentences, d.Stats.Sentences)
	}
	return englishGrade(d, "smog", "SMOG Index", score, extra), nil
}

// ColemanLiau computes the Coleman-Liau Index:
//
//	0.0588×L − 0.296×S − 15.8
//
// where L is letters per 100 words and S is sentences per 100 words. It is the
// one English formula here that never counts a syllable, so it is unaffected by
// the heuristics in textproc.Syllables — useful as a second opinion when
// Flesch-Kincaid and SMOG disagree.
func ColemanLiau(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("coleman-liau")
	}
	l := awl(d) * 100                                              // letters per 100 words
	s := 100 * float64(d.Stats.Sentences) / float64(d.Stats.Words) // sentences per 100 words
	score := 0.0588*l - 0.296*s - 15.8
	return englishGrade(d, "coleman-liau", "Coleman-Liau Index", score, map[string]any{
		"l":          round(l, 2),
		"s":          round(s, 2),
		"characters": d.Stats.Characters,
		"words":      d.Stats.Words,
		"sentences":  d.Stats.Sentences,
	}), nil
}

// ARI computes the Automated Readability Index:
//
//	4.71×(characters/words) + 0.5×(words/sentences) − 21.43
//
// Designed in 1967 to be computable by an electric typewriter as the text was
// being typed: characters, words and sentences, nothing else.
func ARI(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("ari")
	}
	score := 4.71*awl(d) + 0.5*asl(d) - 21.43
	return englishGrade(d, "ari", "Automated Readability Index", score, map[string]any{
		"asl":            round(asl(d), 2),
		"chars_per_word": round(awl(d), 2),
		"characters":     d.Stats.Characters,
		"words":          d.Stats.Words,
		"sentences":      d.Stats.Sentences,
	}), nil
}

func init() {
	analyze.Register(analyze.Metric{
		Name:        "flesch-kincaid",
		Aliases:     []string{"fkgl", "flesch-kincaid-grade"},
		Title:       "Flesch-Kincaid Grade Level",
		Description: "US grade level: 0.39×ASL + 11.8×ASW − 15.59, LOWER is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     FleschKincaid,
	})
	analyze.Register(analyze.Metric{
		Name:        "gunning-fog",
		Aliases:     []string{"fog", "gunning-fog-index"},
		Title:       "Gunning Fog Index",
		Description: "US grade level from long words: 0.4×(ASL + 100×polysyllabic/words), LOWER is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     GunningFog,
	})
	analyze.Register(analyze.Metric{
		Name:        "smog",
		Aliases:     []string{"smog-index"},
		Title:       "SMOG Index",
		Description: "US grade level: 1.0430×sqrt(polysyllabic×30/sentences) + 3.1291; meaningful on 30+ sentences, LOWER is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     SMOG,
	})
	analyze.Register(analyze.Metric{
		Name:        "coleman-liau",
		Aliases:     []string{"cli", "coleman-liau-index"},
		Title:       "Coleman-Liau Index",
		Description: "US grade level from characters, no syllables: 0.0588×L − 0.296×S − 15.8, LOWER is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     ColemanLiau,
	})
	analyze.Register(analyze.Metric{
		Name:        "ari",
		Aliases:     []string{"automated-readability-index"},
		Title:       "Automated Readability Index",
		Description: "US grade level: 4.71×(characters/words) + 0.5×(words/sentences) − 21.43, LOWER is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     ARI,
	})
}
