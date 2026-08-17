package lint

import (
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// Language-agnostic rules: length, rhythm, and repetition. They need no word
// lists beyond the function words repeated-word ignores, which live in the
// per-language files next to the rest of that language's vocabulary.

func init() {
	Register(Rule{
		Name:        "long-sentence",
		Title:       "Long sentence",
		Description: "Sentences over the word limit (default 25 words, warning over 35).",
		Languages:   []string{AnyLanguage},
		Severity:    SeverityWarn,
		Check:       checkLongSentence,
	})
	Register(Rule{
		Name:        "hard-sentence",
		Title:       "Hard sentence",
		Description: "The worst sentences by their own reading-ease score — the fix-these-first list.",
		Languages:   []string{string(textproc.LangGerman), string(textproc.LangEnglish)},
		Severity:    SeverityWarn,
		Check:       checkHardSentence,
	})
	Register(Rule{
		Name:        "sentence-length-variance",
		Title:       "Sentence length variance",
		Description: "Document-level: sentence lengths are near-uniform (monotone) or wildly erratic.",
		Languages:   []string{AnyLanguage},
		Severity:    SeverityInfo,
		Check:       checkSentenceLengthVariance,
	})
	Register(Rule{
		Name:        "repeated-word",
		Title:       "Repeated word",
		Description: "The same content word repeated within a short window (default 40 words).",
		Languages:   []string{AnyLanguage},
		Severity:    SeverityInfo,
		Check:       checkRepeatedWord,
	})
	Register(Rule{
		Name:        "repeated-sentence-start",
		Title:       "Repeated sentence start",
		Description: "Three or more consecutive sentences opening with the same word.",
		Languages:   []string{AnyLanguage},
		Severity:    SeverityInfo,
		Check:       checkRepeatedSentenceStart,
	})
	Register(Rule{
		Name:        "long-word",
		Title:       "Long word",
		Description: "Words over the character limit (default 20 runes) — in German usually a compound worth breaking up.",
		Languages:   []string{AnyLanguage},
		Severity:    SeverityInfo,
		Check:       checkLongWord,
	})
}

// checkLongSentence is the highest-yield finding in practice, so the message
// names the actual count and the suggestion is an instruction, not a lament.
func checkLongSentence(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	for _, sv := range v.sentences {
		n := len(sv.Toks)
		if n <= cfg.MaxSentenceWords {
			continue
		}
		sev := SeverityInfo
		if n > cfg.WarnSentenceWords {
			sev = SeverityWarn
		}
		msg := tr(d.Language,
			fmt.Sprintf("Satz mit %d Wörtern (Grenze %d).", n, cfg.MaxSentenceWords),
			fmt.Sprintf("Sentence runs to %d words (limit %d).", n, cfg.MaxSentenceWords))
		sug := tr(d.Language,
			"In zwei Sätze aufteilen: ein Gedanke pro Satz.",
			"Split into two sentences: one idea per sentence.")
		out = append(out, finding(d, "long-sentence", sev, sv.Num, sv.S.Start, sv.S.End, msg, sug, float64(n)))
	}
	return out
}

// sentenceEase scores one sentence on the reading-ease scale of its language.
//
// The arithmetic is duplicated from internal/analyze/readability on purpose.
// That package's registry is document-level — every Metric takes a whole
// *textproc.Doc and returns one score — so using it here would mean faking a
// one-sentence document per sentence, and it would couple the lint registry to
// the metric registry in a package that the metric packages could plausibly
// want to import back. Two lines of arithmetic are cheaper than that edge.
//
//	Flesch (en): 206.835 − 1.015×ASL − 84.6×ASW
//	Amstad (de): 180 − ASL − 58.5×ASW
//
// For a single sentence ASL is simply its word count and ASW its syllables per
// word.
func sentenceEase(lang textproc.Language, toks []token) (float64, bool) {
	n := len(toks)
	if n == 0 {
		return 0, false
	}
	syll := 0
	for _, t := range toks {
		syll += t.Syll
	}
	asl := float64(n)
	asw := float64(syll) / float64(n)
	switch lang {
	case textproc.LangGerman:
		return 180 - asl - 58.5*asw, true
	case textproc.LangEnglish:
		return 206.835 - 1.015*asl - 84.6*asw, true
	default:
		return 0, false
	}
}

// checkHardSentence reports the worst N sentences below the threshold: the
// "rewrite these five" list, not another score.
func checkHardSentence(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	type scored struct {
		sv    sentView
		score float64
	}
	cands := []scored{}
	for _, sv := range v.sentences {
		if len(sv.Toks) < cfg.MinHardSentenceWords {
			continue // a headline is not a hard sentence, it is a headline
		}
		score, ok := sentenceEase(d.Language, sv.Toks)
		if !ok || score >= cfg.HardSentenceScore {
			continue
		}
		cands = append(cands, scored{sv, score})
	}
	// Worst first; ties broken by position so the selection is deterministic.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score < cands[j].score
		}
		return cands[i].sv.S.Start < cands[j].sv.S.Start
	})
	if len(cands) > cfg.Worst {
		cands = cands[:cfg.Worst]
	}

	out := []Finding{}
	for _, c := range cands {
		score := round(c.score, 1)
		sev := SeverityInfo
		if c.score < cfg.HardSentenceWarnScore {
			sev = SeverityWarn
		}
		num := strconv.FormatFloat(score, 'f', -1, 64)
		msg := tr(d.Language,
			fmt.Sprintf("Schwer lesbarer Satz: Lesbarkeit %s von 100 (Amstad), %d Wörter.", num, len(c.sv.Toks)),
			fmt.Sprintf("Hard sentence: reading ease %s of 100 (Flesch), %d words.", num, len(c.sv.Toks)))
		sug := tr(d.Language,
			"Kürzen, Nebensätze auflösen, lange Wörter durch kurze ersetzen.",
			"Shorten it, unpack the subordinate clauses, swap long words for short ones.")
		out = append(out, finding(d, "hard-sentence", sev, c.sv.Num, c.sv.S.Start, c.sv.S.End, msg, sug, score))
	}
	return out
}

// checkSentenceLengthVariance is a whole-document finding: rhythm is a property
// of the sequence, not of any one sentence. It is therefore anchored at offset
// 0 with an empty span and sentence 0, which is how every document-level
// finding in this package marks itself.
func checkSentenceLengthVariance(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	n := len(v.sentences)
	if n < cfg.MinVarianceSentences {
		return nil
	}
	sum := 0
	for _, sv := range v.sentences {
		sum += len(sv.Toks)
	}
	mean := float64(sum) / float64(n)
	if mean == 0 {
		return nil
	}
	sq := 0.0
	for _, sv := range v.sentences {
		diff := float64(len(sv.Toks)) - mean
		sq += diff * diff
	}
	cv := math.Sqrt(sq/float64(n)) / mean

	avg := strconv.FormatFloat(round(mean, 1), 'f', -1, 64)
	num := strconv.FormatFloat(round(cv, 2), 'f', -1, 64)
	var msg, sug string
	switch {
	case cv < cfg.MinLengthCV:
		msg = tr(d.Language,
			fmt.Sprintf("Gleichförmiger Satzrhythmus im ganzen Dokument: %d Sätze, im Schnitt %s Wörter, kaum Längenunterschied (Variationskoeffizient %s).", n, avg, num),
			fmt.Sprintf("Monotone rhythm across the document: %d sentences averaging %s words with almost no variation (coefficient of variation %s).", n, avg, num))
		sug = tr(d.Language,
			"Kurze und lange Sätze mischen; einen kurzen Satz als Pointe setzen.",
			"Mix short and long sentences; land a short one to make a point.")
	case cv > cfg.MaxLengthCV:
		msg = tr(d.Language,
			fmt.Sprintf("Sehr unregelmäßige Satzlängen im ganzen Dokument: %d Sätze, im Schnitt %s Wörter (Variationskoeffizient %s).", n, avg, num),
			fmt.Sprintf("Erratic sentence lengths across the document: %d sentences averaging %s words (coefficient of variation %s).", n, avg, num))
		sug = tr(d.Language,
			"Die Ausreißer nach oben teilen, damit der Text gleichmäßig liest.",
			"Split the outliers so the text reads evenly.")
	default:
		return nil
	}
	return []Finding{finding(d, "sentence-length-variance", SeverityInfo, 0, 0, 0, msg, sug, round(cv, 2))}
}

// checkRepeatedWord fires on the second occurrence, which is the one a writer
// would replace.
func checkRepeatedWord(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	stop := functionWords(d.Language)
	last := make(map[string]token, len(v.tokens))
	out := []Finding{}
	for _, t := range v.tokens {
		if t.Runes < cfg.MinRepeatWordChars || stop.has(t.Lower) || !hasLetter(t.Text) {
			continue
		}
		if prev, ok := last[t.Lower]; ok {
			if gap := t.Index - prev.Index; gap <= cfg.RepeatWindow {
				msg := tr(d.Language,
					fmt.Sprintf("„%s“ wiederholt sich nach %d Wörtern.", t.Text, gap),
					fmt.Sprintf("%q repeats after %d words.", t.Text, gap))
				sug := tr(d.Language,
					"Synonym verwenden oder die zweite Nennung streichen.",
					"Use a synonym or drop the second mention.")
				out = append(out, finding(d, "repeated-word", SeverityInfo, t.Sentence, t.Start, t.End, msg, sug, float64(gap)))
			}
		}
		last[t.Lower] = t
	}
	return out
}

// checkRepeatedSentenceStart anchors on the first word of the run, which is
// where the rewrite starts.
func checkRepeatedSentenceStart(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	i := 0
	for i < len(v.sentences) {
		if len(v.sentences[i].Toks) == 0 {
			i++
			continue
		}
		word := v.sentences[i].Toks[0].Lower
		j := i + 1
		for j < len(v.sentences) && len(v.sentences[j].Toks) > 0 && v.sentences[j].Toks[0].Lower == word {
			j++
		}
		if run := j - i; run >= cfg.RepeatedStartRun {
			first := v.sentences[i].Toks[0]
			msg := tr(d.Language,
				fmt.Sprintf("%d aufeinanderfolgende Sätze beginnen mit „%s“.", run, first.Text),
				fmt.Sprintf("%d consecutive sentences open with %q.", run, first.Text))
			sug := tr(d.Language,
				"Satzanfänge variieren: mit dem Subjekt, einem Zeitwort oder dem Ergebnis beginnen.",
				"Vary the openings: start with the subject, a time marker, or the result.")
			out = append(out, finding(d, "repeated-sentence-start", SeverityInfo, v.sentences[i].Num, first.Start, first.End, msg, sug, float64(run)))
		}
		i = j
	}
	return out
}

// checkLongWord counts runes, not bytes: "Straßenverkehrsordnung" is 22
// characters, and a byte count would call it 23 and be wrong about why.
func checkLongWord(d *textproc.Doc, cfg Config) []Finding {
	v := cfg.tokens(d)
	out := []Finding{}
	for _, t := range v.tokens {
		if t.Runes <= cfg.MaxWordChars || !hasLetter(t.Text) {
			continue
		}
		msg := tr(d.Language,
			fmt.Sprintf("Sehr langes Wort: „%s“ (%d Zeichen).", t.Text, t.Runes),
			fmt.Sprintf("Very long word: %q (%d characters).", t.Text, t.Runes))
		sug := tr(d.Language,
			"Kompositum auftrennen oder mit Bindestrich gliedern.",
			"Break the compound up or replace it with a shorter term.")
		out = append(out, finding(d, "long-word", SeverityInfo, t.Sentence, t.Start, t.End, msg, sug, float64(t.Runes)))
	}
	return out
}

// functionWords picks the ignore list for repeated-word. Each language's list
// lives with the rest of that language's vocabulary, in rules_de.go and
// rules_en.go.
func functionWords(lang textproc.Language) *phraseSet {
	switch lang {
	case textproc.LangGerman:
		return deFunctionWords
	case textproc.LangEnglish:
		return enFunctionWords
	default:
		return emptyPhrases
	}
}

var emptyPhrases = newPhrases()

// hasLetter keeps pure numbers out of the word rules: "2024" repeating twice is
// not a style problem.
func hasLetter(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127 {
			return true
		}
	}
	return false
}
