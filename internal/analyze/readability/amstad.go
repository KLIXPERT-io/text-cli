package readability

import (
	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// amstadScale mirrors the Flesch scale string, in the language of the metric.
const amstadScale = "0–100, höher ist leichter"

// amstadBands are the German reading-ease bands, easiest first. Level carries
// the German label a German-speaking reader expects; Grade adds an English
// gloss of the schooling level so a non-German pipeline still gets a handle on
// the number.
var amstadBands = []band{
	{90, "sehr leicht", "4th grade"},
	{80, "leicht", "5th–6th grade"},
	{70, "mittelleicht", "7th grade"},
	{60, "mittel", "8th–9th grade"},
	{50, "mittelschwer", "10th–12th grade"},
	{30, "schwer", "Studium"},
	{0, "sehr schwer", "akademisch"},
}

// Amstad computes the German reading-ease score:
//
//	180 − ASL − 58.5×ASW
//
// Toni Amstad recalibrated Flesch's formula for German in his 1978 dissertation
// because the English constants do not transfer. German words are markedly
// longer and freely compounded ("Geschwindigkeitsbegrenzung" is one word of
// eight syllables where English needs two words), so ASW runs far higher for
// prose of the same difficulty. Feed German through the English formula and the
// 84.6×ASW term alone drags almost every text below zero — a newspaper article
// and a legal opinion would both come out "very confusing", which tells a
// reader nothing. Amstad's smaller syllable weight (58.5) and unit sentence
// weight (1.0, against Flesch's 1.015) restore a usable spread over the same
// 0–100 scale, so the band labels stay comparable across the two languages.
func Amstad(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("amstad")
	}
	// Band from the rounded score — see Flesch for why.
	score := round(180-asl(d)-58.5*asw(d), 1)
	level, grade := classify(amstadBands, score)
	return analyze.Result{
		Metric:   "amstad",
		Title:    "Amstad (Flesch für Deutsch)",
		Score:    score,
		Level:    level,
		Grade:    grade,
		Scale:    amstadScale,
		Language: language(d, textproc.LangGerman),
		Extra:    extra(d),
	}, nil
}

func init() {
	analyze.Register(analyze.Metric{
		Name:        "amstad",
		Aliases:     []string{"flesch-de", "flesch-amstad", "fre-de"},
		Title:       "Amstad (Flesch für Deutsch)",
		Description: "German reading ease (Amstad 1978): 180 − ASL − 58.5×ASW, 0–100, higher is easier.",
		Languages:   []string{string(textproc.LangGerman)},
		Compute:     Amstad,
	})
}
