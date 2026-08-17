// Package readability holds the reading-ease formulas: Flesch for English and
// Amstad — Toni Amstad's 1978 German recalibration — for German.
//
// Every metric here is a pure function of a *textproc.Doc, so a document is
// tokenized once and scored by as many formulas as apply to its language. The
// metrics register themselves with the analyze package at init time; importing
// this package for its side effect is the only wiring a command needs.
//
// Scores are returned raw, never clamped: a sentence tortured enough to score
// −12 is information, and hiding it behind a floor of 0 would throw that away.
// Only the *band lookup* clamps, because the labels stop at the ends of the
// scale.
package readability

import (
	"math"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// band is one row of a level table: every score at or above min, and below the
// min of the band before it, carries these labels.
type band struct {
	min   float64
	level string
	grade string
}

// classify maps a score onto a level table. The tables are ordered from the
// easiest band down, and the score is clamped to 0–100 first so an out-of-range
// score still gets the label of the nearest end rather than an empty string.
func classify(bands []band, score float64) (level, grade string) {
	s := math.Max(0, math.Min(100, score))
	for _, b := range bands {
		if s >= b.min {
			return b.level, b.grade
		}
	}
	last := bands[len(bands)-1]
	return last.level, last.grade
}

// round returns v rounded to places decimals. Scores are reported to one
// decimal (the formulas are not precise enough to justify more) and the
// averages behind them to two.
func round(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// asl is words per sentence. It prefers the value textproc computed and falls
// back to the raw counts, so a hand-built Doc (tests, other callers) still
// scores correctly instead of silently reading 0.
func asl(d *textproc.Doc) float64 {
	if d.Stats.AvgSentenceLength != 0 {
		return d.Stats.AvgSentenceLength
	}
	if d.Stats.Sentences == 0 {
		return 0
	}
	return float64(d.Stats.Words) / float64(d.Stats.Sentences)
}

// asw is syllables per word, with the same fallback as asl.
func asw(d *textproc.Doc) float64 {
	if d.Stats.AvgSyllablesPerWord != 0 {
		return d.Stats.AvgSyllablesPerWord
	}
	if d.Stats.Words == 0 {
		return 0
	}
	return float64(d.Stats.Syllables) / float64(d.Stats.Words)
}

// extra is the per-metric detail map. Both formulas take the same two inputs,
// so both report the same evidence: an agent that disagrees with a score can
// see exactly which count produced it.
func extra(d *textproc.Doc) map[string]any {
	return map[string]any{
		"asl":       round(asl(d), 2),
		"asw":       round(asw(d), 2),
		"words":     d.Stats.Words,
		"sentences": d.Stats.Sentences,
		"syllables": d.Stats.Syllables,
	}
}

// emptyErr is the shared failure for a document with nothing to measure.
// Dividing by zero sentences would yield NaN, which is worse than an error.
func emptyErr(metric string) *errs.E {
	return errs.Newf(errs.CodeEmptyInput, "%s: no words or sentences to measure", metric).
		WithHint("Check that the input actually contains prose; punctuation-only or empty documents cannot be scored.")
}

// language reports the language a result was computed for, defaulting to the
// metric's own language when the Doc was built without one.
func language(d *textproc.Doc, fallback textproc.Language) string {
	if d.Language != "" && d.Language != textproc.LangAuto {
		return string(d.Language)
	}
	return string(fallback)
}
