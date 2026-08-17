package readability

import (
	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// fleschScale describes the direction of the number for anyone — human or
// agent — seeing it for the first time.
const fleschScale = "0–100, higher is easier"

// fleschBands are Flesch's own reading-ease bands, easiest first, with the US
// schooling level each implies.
var fleschBands = []band{
	{90, "very easy", "5th grade"},
	{80, "easy", "6th grade"},
	{70, "fairly easy", "7th grade"},
	{60, "standard", "8th–9th grade"},
	{50, "fairly difficult", "10th–12th grade"},
	{30, "difficult", "college"},
	{0, "very confusing", "college graduate"},
}

// Flesch computes the Flesch Reading Ease score of an analysed document:
//
//	206.835 − 1.015×ASL − 84.6×ASW
//
// where ASL is words per sentence and ASW syllables per word. The constants are
// calibrated on English and only English — see amstad.go for why running them
// over German is meaningless.
func Flesch(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("flesch")
	}
	// Band from the rounded score, so the printed number and its label can
	// never contradict each other at a boundary (89.97 prints as 90.0).
	score := round(206.835-1.015*asl(d)-84.6*asw(d), 1)
	level, grade := classify(fleschBands, score)
	return analyze.Result{
		Metric:   "flesch",
		Title:    "Flesch Reading Ease",
		Score:    score,
		Level:    level,
		Grade:    grade,
		Scale:    fleschScale,
		Language: language(d, textproc.LangEnglish),
		Extra:    extra(d),
	}, nil
}

func init() {
	analyze.Register(analyze.Metric{
		Name:        "flesch",
		Aliases:     []string{"fre", "flesch-reading-ease"},
		Title:       "Flesch Reading Ease",
		Description: "English reading ease: 206.835 − 1.015×ASL − 84.6×ASW, 0–100, higher is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     Flesch,
	})
}
