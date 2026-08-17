package readability

import (
	"math"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// lixScale states the direction: like every grade-level formula here and unlike
// flesch/amstad, LOWER is EASIER.
const lixScale = "20–60+, lower is easier"

// lixBands are Björnsson's own bands, easiest first. The boundaries are
// exclusive at the top — the published table reads "<25 very easy, 25–35 easy",
// so a score of exactly 25.0 is "easy" — which is the opposite convention to
// the Wiener Sachtextformel's "≤6"; classifyGrade takes the choice as an
// argument rather than guessing.
var lixBands = []gradeBand{
	{25, "very easy", "children's literature"},
	{35, "easy", "fiction, popular press"},
	{45, "medium", "normal prose"},
	{55, "difficult", "technical, non-fiction"},
	{math.Inf(1), "very difficult", "academic"},
}

// LIX computes Carl-Hugo Björnsson's Läsbarhetsindex:
//
//	LIX = words/sentences + 100 × longWords/words
//
// where a long word is one of more than six characters (textproc.Stats.LongWords).
//
// LIX counts characters and sentence lengths only — never syllables — which is
// why it is the one formula here that carries analyze.AnyLanguage: syllable
// counting is the part of readability scoring that has to be recalibrated per
// language (see amstad.go), and LIX simply does not do it. That makes it the
// right metric for a mixed-language corpus, where it is the only score that can
// be compared row to row.
//
// The result is a difficulty index, not a reading-ease score: LOWER is EASIER,
// the opposite direction to flesch and amstad. See wstf.go for how that
// direction is published to callers.
func LIX(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("lix")
	}
	// 100×longWords/words is exactly iwPct, the Wiener formulas' IW term.
	longPct := iwPct(d)
	// Band from the rounded score — see flesch.go.
	score := round(asl(d)+longPct, 1)
	level, grade := classifyGrade(lixBands, score, false)
	return analyze.Result{
		Metric: "lix",
		Title:  "LIX (Läsbarhetsindex)",
		Score:  score,
		Level:  level,
		Grade:  grade,
		Scale:  lixScale,
		// No fallback language to claim: LIX is calibrated for none in
		// particular, so a Doc analysed without a language reports "*".
		Language: language(d, textproc.Language(analyze.AnyLanguage)),
		Extra: map[string]any{
			"asl":        round(asl(d), 2),
			"iw":         round(longPct, 2),
			"long_words": d.Stats.LongWords,
			"words":      d.Stats.Words,
			"sentences":  d.Stats.Sentences,
		},
	}, nil
}

func init() {
	analyze.Register(analyze.Metric{
		Name:        "lix",
		Aliases:     []string{"lasbarhetsindex", "läsbarhetsindex", "lesbarkeitsindex"},
		Title:       "LIX (Läsbarhetsindex)",
		Description: "Language-agnostic difficulty index: words/sentences + 100×longWords/words, 20–60+, LOWER is easier.",
		Languages:   []string{analyze.AnyLanguage},
		Compute:     LIX,
	})
}
