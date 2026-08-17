package textproc

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Language detection
// ---------------------------------------------------------------------------
//
// Detection is a weighted vote, offline and dependency-free. Three families of
// signal are scored:
//
//   1. function words — the closed class that dominates any real sentence and
//      barely overlaps between the two languages. Words that exist in both
//      ("in", "an", "am", "was", "man", "war", "hat", "will", "so") are in
//      neither list, so they cannot swing the vote.
//   2. orthography — ä ö ü ß, and the German derivation suffixes -ung, -keit,
//      -heit, -schaft, -lich, -isch, -chen, -tät. English gets a small bonus
//      for -ing/-ly and for contractions.
//   3. capitalisation — German capitalises every noun, so mid-sentence capitals
//      are a weak but real signal. Weighted low (proper nouns exist in English
//      too) and only used once there are enough words to judge.
//
// One short sentence in either language is enough: three function-word hits
// already outweigh anything the other language collects.

const detectWordCap = 2000 // detection never needs more than the first ~2k words

var deStop = wordSet(`
	der die das den dem des ein eine einen einem einer eines und oder aber auch
	noch nur schon nicht ist sind sein seine ihre haben hatte hatten werden wird
	wurde wurden mit von zu zum zur auf für aus bei nach über unter vor durch
	gegen ohne um im beim vom als dass daß weil wenn dann sich ich du er sie es
	wir uns euch mich dich ihm ihn ihnen kann können muss müssen soll sollen
	wollen sehr mehr wie was wer wo hier dort diese dieser dieses jede jeden
	alle alles heute immer wieder zwischen bis nun etwa dabei damit dazu doch
	nichts schon seit sowie werden viele einige zwar deshalb jedoch
`)

var enStop = wordSet(`
	the of and to a is it that for on are as with be at this have from or by not
	but what all were we when there can which do how out about many then them
	some would like into time has more see could people than been who now find
	down did get come made may over new take only work know year most very after
	our just think say where through much before too right any same want also
	well must even because here why ask need home again off play life always
	both together group often until without second later idea enough real almost
	above while might next open begin still should world high every near own
	last never start under they their there's you your his her him she its
`)

var deSuffixes = [...]string{"ung", "keit", "heit", "schaft", "lich", "isch", "chen", "tät", "ungen", "nisse"}

// Detect guesses the language of text. Returns LangEnglish or LangGerman and
// falls back to LangEnglish when there is no signal.
func Detect(text string) Language {
	var de, en float64
	words, caps := 0, 0
	prevEnd := -1

	for i := 0; ; {
		start, end, ok := nextToken(text, i)
		if !ok {
			break
		}
		i = end
		words++
		if words > detectWordCap {
			break
		}
		tok := text[start:end]
		low := strings.ToLower(tok) // no allocation for lower-case ASCII

		if _, ok := deStop[low]; ok {
			de++
		}
		if _, ok := enStop[low]; ok {
			en++
		}
		if hasGermanRune(tok) {
			de += 2
		}
		if len(low) >= 6 && hasAnySuffix(low, deSuffixes[:]) {
			de += 1.5
		}
		if len(low) >= 5 && (strings.HasSuffix(low, "ing") || strings.HasSuffix(low, "ly")) {
			en++
		}
		if strings.Contains(low, "n't") || strings.HasSuffix(low, "'s") || strings.HasSuffix(low, "’s") {
			en++
		}
		if prevEnd >= 0 && !containsTerminator(text[prevEnd:start]) {
			if r, _ := utf8.DecodeRuneInString(tok); unicode.IsUpper(r) && !allUpper(tok) {
				caps++
			}
		}
		prevEnd = end
	}

	// Capitalisation is a tie-breaker, never the sole evidence: it only counts
	// once German has already scored lexically (a function word, an umlaut or a
	// derivation suffix). Otherwise a proper-noun-heavy English sentence
	// ("Ada Lovelace worked in London.") would be called German on capitals
	// alone, when the documented default for "no signal" is English.
	if words >= 5 && de > 0 {
		de += 0.4 * float64(caps)
	}
	if de > en {
		return LangGerman
	}
	return LangEnglish
}

func hasGermanRune(tok string) bool {
	for _, r := range tok {
		switch r {
		case 'ä', 'ö', 'ü', 'Ä', 'Ö', 'Ü', 'ß':
			return true
		}
	}
	return false
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			return true
		}
	}
	return false
}

func containsTerminator(gap string) bool {
	for _, r := range gap {
		if isTerminator(r) || r == '\n' || r == ':' {
			return true
		}
	}
	return false
}

func allUpper(tok string) bool {
	for _, r := range tok {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// Supported returns the languages Analyze accepts (excluding LangAuto).
func Supported() []Language {
	return []Language{LangEnglish, LangGerman}
}
