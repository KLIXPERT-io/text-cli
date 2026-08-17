package textproc

import (
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// Analyze tokenizes text and computes all Stats. lang may be LangAuto, in which
// case the language is detected and Doc.Detected is set to true.
//
// It returns *errs.E with errs.CodeUnsupportedLanguage for a language that is
// not auto/en/de. Empty or word-less input is NOT an error: the returned Doc has
// zero Stats and callers decide what to do with it.
//
// Everything is computed in one pass — sentences, words, syllables — so a run
// with several metrics tokenizes exactly once. Sentences without a single word
// (a line of dashes, a stray "...") are dropped, so Doc.Sentences and
// Stats.Sentences always agree.
func Analyze(text string, lang Language) (*Doc, error) {
	switch lang {
	case LangAuto, LangEnglish, LangGerman:
	default:
		return nil, errs.Newf(errs.CodeUnsupportedLanguage, "unsupported language %q", string(lang)).
			WithHint("use --lang en, --lang de, or --lang auto to detect")
	}

	doc := &Doc{Text: text, Language: lang}
	if lang == LangAuto {
		doc.Language = Detect(text)
		doc.Detected = true
	}
	if len(text) == 0 {
		return doc, nil
	}

	sentences := SplitSentences(text)
	kept := sentences[:0]
	st := Stats{}

	for si := range sentences {
		s := sentences[si]
		body := s.Text
		words := make([]Word, 0, len(body)/6+1)
		for i := 0; ; {
			start, end, ok := nextToken(body, i)
			if !ok {
				break
			}
			i = end
			tok := body[start:end]
			runes, chars := measure(tok)
			syl := Syllables(tok, doc.Language)
			if syl == 0 {
				// A token with no letters but at least one digit ("42",
				// "1.000,50"): numbers are words and count as one syllable.
				syl = 1
			}
			words = append(words, Word{Text: tok, Syllables: syl, Runes: runes})

			st.Words++
			st.Syllables += syl
			st.Characters += chars
			switch {
			case syl == 1:
				st.MonosyllabicWords++
			case syl >= 3:
				st.PolysyllabicWords++
			}
			if chars > 6 {
				st.LongWords++
			}
		}
		if len(words) == 0 {
			continue
		}
		s.Words = words
		kept = append(kept, s)
		st.Sentences++
	}

	doc.Sentences = kept
	// Guard every average: a zero denominator yields 0.0, never NaN or +Inf —
	// NaN would serialize as invalid JSON and break downstream consumers.
	if st.Sentences > 0 {
		st.AvgSentenceLength = float64(st.Words) / float64(st.Sentences)
	}
	if st.Words > 0 {
		st.AvgSyllablesPerWord = float64(st.Syllables) / float64(st.Words)
		st.AvgWordLength = float64(st.Characters) / float64(st.Words)
	}
	doc.Stats = st
	return doc, nil
}
