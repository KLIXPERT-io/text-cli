package textproc

import (
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Syllable counting
// ---------------------------------------------------------------------------
//
// Both counters work on a folded copy of the word: lower-case ASCII letters
// only, with accented letters mapped to their base letter (ä→a, ö→o, ü→u, ß→s,
// é→e ...) and unknown letters mapped to 'x' so they behave like consonants and
// still separate vowel groups. The buffer lives on the stack for words up to 48
// bytes, so counting a word allocates nothing.
//
// English is irregular enough that a pure heuristic mispredicts a handful of
// very common words; those live in enExceptions. German is regular, so it gets
// its own nucleus scanner instead of the English rules.

const foldBufSize = 48

// Syllables counts syllables in a single word for the given language. It always
// returns at least 1 for a word containing a letter and 0 for a token with no
// letters (a digit-only token such as "42" — Analyze counts those as one
// syllable, see analyze.go).
func Syllables(word string, lang Language) int {
	if lang == LangGerman {
		return syllablesDE(word)
	}
	return syllablesEN(word)
}

// fold appends the lower-cased, ASCII-folded letters of word to dst.
func fold(word string, dst []byte) []byte {
	for i := 0; i < len(word); {
		c := word[i]
		if c < utf8.RuneSelf {
			switch {
			case c >= 'a' && c <= 'z':
				dst = append(dst, c)
			case c >= 'A' && c <= 'Z':
				dst = append(dst, c+'a'-'A')
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(word[i:])
		i += size
		if b := foldRune(r); b != 0 {
			dst = append(dst, b)
		}
	}
	return dst
}

// foldRune maps a non-ASCII rune to a single ASCII letter, or 0 if it is not a
// letter at all. Umlauts fold to their base vowel: that is safe for syllable
// counting because "äu" behaves exactly like "au" and "ö"/"ü" are single
// nuclei just like "o"/"u".
func foldRune(r rune) byte {
	switch unicode.ToLower(r) {
	case 'ä', 'à', 'á', 'â', 'ã', 'å', 'ā', 'ą', 'æ':
		return 'a'
	case 'ö', 'ò', 'ó', 'ô', 'õ', 'ø', 'ō':
		return 'o'
	case 'ü', 'ù', 'ú', 'û', 'ū':
		return 'u'
	case 'è', 'é', 'ê', 'ë', 'ē', 'ę':
		return 'e'
	case 'ì', 'í', 'î', 'ï', 'ī':
		return 'i'
	case 'ß', 'ś', 'š':
		return 's'
	case 'ç', 'ć', 'č':
		return 'c'
	case 'ñ', 'ń':
		return 'n'
	case 'ý', 'ÿ':
		return 'y'
	case 'ł':
		return 'l'
	case 'ż', 'ź', 'ž':
		return 'z'
	case 'ř':
		return 'r'
	case 'ď', 'đ':
		return 'd'
	}
	if unicode.IsLetter(r) {
		return 'x' // unknown script: behave like a consonant
	}
	return 0
}

// ---------------------------------------------------------------------------
// English
// ---------------------------------------------------------------------------

// enExceptions are words the vowel-group heuristic gets wrong and that are
// common enough to matter. Keeping the list this short is deliberate: every
// entry is a rule the heuristic could not express (silent vowels in
// "business", the /saɪ/ of "science", the hiatus in "create").
var enExceptions = map[string]int{
	"business":   2,
	"businesses": 3,
	"science":    2,
	"sciences":   2,
	"scientist":  3,
	"scientists": 3,
	"scientific": 4,
	"create":     2,
	"creates":    2,
	"created":    3,
	"creating":   3,
	"creation":   3,
	"creative":   3,
	"poem":       2,
	"poems":      2,
	"poet":       2,
	"poetry":     3,
	"aisle":      1,
	"wednesday":  2,
	"colonel":    2,
}

func isVowelEN(c byte, i int) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	case 'y':
		return i > 0 // "yes" starts with a consonant, "myth" does not
	}
	return false
}

func syllablesEN(word string) int {
	var arr [foldBufSize]byte
	s := fold(word, arr[:0])
	if len(s) == 0 {
		return 0 // no letters: "42", "---"
	}
	if n, ok := enExceptions[string(s)]; ok {
		return n
	}
	n := vowelGroupsEN(s)
	n += hiatusEN(s)
	n -= silentEN(s)
	if n < 1 {
		n = 1
	}
	return n
}

// vowelGroupsEN counts maximal runs of vowels; consecutive vowels are one group.
func vowelGroupsEN(s []byte) int {
	n, prev := 0, false
	for i := 0; i < len(s); i++ {
		v := isVowelEN(s[i], i)
		if v && !prev {
			n++
		}
		prev = v
	}
	return n
}

// hiatusEN adds the syllable that a word-final "ea"/"ia"/"io" hides: "idea",
// "area", "media", "radio" are one syllable longer than their vowel groups
// suggest. Words whose only group is that pair ("sea", "flea") are left alone.
func hiatusEN(s []byte) int {
	if len(s) < 4 {
		return 0
	}
	a, b := s[len(s)-2], s[len(s)-1]
	pair := (a == 'e' && b == 'a') || (a == 'i' && (b == 'a' || b == 'o'))
	if !pair {
		return 0
	}
	if isVowelEN(s[len(s)-3], len(s)-3) {
		return 0 // part of a longer group, e.g. "aria" is fine but "aiea" is not
	}
	if vowelGroupsEN(s) < 2 {
		return 0
	}
	return 1
}

// silentEN returns 1 when the trailing "e" of the word (also inside "-es" and
// "-ed") is silent and must not count as a syllable.
func silentEN(s []byte) int {
	n := len(s)
	if n < 3 {
		return 0 // "be", "he", "the" is handled by the >= 1 clamp
	}
	switch {
	case s[n-1] == 'e':
		if isVowelEN(s[n-2], n-2) {
			return 0 // "coffee", "queue", "eye": the 'e' is part of a group
		}
		if syllabicLE(s, n-2) {
			return 0 // "table", "little", "simple"
		}
		return 1 // "make", "code"
	case s[n-1] == 's' && s[n-2] == 'e':
		if isVowelEN(s[n-3], n-3) {
			return 0 // "toes", "series"
		}
		if syllabicLE(s, n-3) {
			return 0 // "apples"
		}
		if sibilant(s, n-3) {
			return 0 // "passes", "boxes", "watches", "races", "changes"
		}
		return 1 // "makes", "notes"
	case s[n-1] == 'd' && s[n-2] == 'e':
		if isVowelEN(s[n-3], n-3) {
			return 0 // "agreed"
		}
		if s[n-3] == 't' || s[n-3] == 'd' {
			return 0 // "wanted", "needed"
		}
		if syllabicLE(s, n-3) {
			return 0 // "handled"
		}
		return 1 // "asked", "played"
	}
	return 0
}

// syllabicLE reports whether s[i] is the 'l' of a syllabic "-le" ending, i.e.
// preceded by a consonant ("table", "little"), not a vowel ("whole", "smile").
func syllabicLE(s []byte, i int) bool {
	return i >= 1 && s[i] == 'l' && !isVowelEN(s[i-1], i-1)
}

// sibilant reports whether the consonant at s[i] makes a following "-es" its
// own syllable.
func sibilant(s []byte, i int) bool {
	if i < 0 {
		return false
	}
	switch s[i] {
	case 's', 'x', 'z', 'c', 'g', 'j':
		return true
	case 'h':
		return i >= 1 && (s[i-1] == 'c' || s[i-1] == 's') // "-ches", "-shes"
	}
	return false
}

// ---------------------------------------------------------------------------
// German
// ---------------------------------------------------------------------------
//
// German spelling maps onto syllables almost one-to-one: every vowel nucleus is
// a syllable. The scanner walks the folded word and counts nuclei, treating the
// digraphs below as a single nucleus. Notable trade-offs:
//
//   - "ie" is counted as one nucleus ("Chemie" = 2, "Familie" = 3). Words where
//     i and e are actually separate ("Linie" = 3, we say 2) are the price; the
//     opposite default would break far more words. The one systematic exception
//     is the "-ien" plural ("Ferien", "Familien", "Studien"), which does split.
//   - "ß" folds to 's' (a consonant) so "Straße" = 2 and no false vowel
//     adjacency appears.
//   - vowel + "h" ("geht", "Bahn") stays one nucleus because 'h' is a consonant
//     and simply ends the run.

func isVowelDE(c byte) bool {
	switch c {
	case 'a', 'e', 'i', 'o', 'u', 'y':
		return true
	}
	return false
}

// digraphDE reports whether a+b form a single nucleus.
func digraphDE(a, b byte) bool {
	switch a {
	case 'a':
		return b == 'u' || b == 'i' || b == 'y' || b == 'a'
	case 'e':
		return b == 'u' || b == 'i' || b == 'y' || b == 'e'
	case 'i':
		return b == 'e'
	case 'o':
		return b == 'o'
	}
	return false
}

func syllablesDE(word string) int {
	var arr [foldBufSize]byte
	s := fold(word, arr[:0])
	if len(s) == 0 {
		return 0
	}
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		if !isVowelDE(c) {
			i++
			continue
		}
		if c == 'u' && i > 0 && s[i-1] == 'q' {
			i++ // "qu" is a consonant cluster: "Quelle" = 2
			continue
		}
		n++
		if i+1 < len(s) && digraphDE(c, s[i+1]) {
			// "-ien" is a Latin plural and does split: Fe-ri-en, Fa-mi-li-en.
			// Short words ("Wien") keep the diphthong.
			if c == 'i' && s[i+1] == 'e' && len(s) >= 6 && i+2 == len(s)-1 && s[len(s)-1] == 'n' {
				n++
			}
			i += 2
			continue
		}
		i++
	}
	if n < 1 {
		n = 1
	}
	return n
}
