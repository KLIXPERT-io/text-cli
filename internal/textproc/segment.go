package textproc

import (
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Sentence segmentation
// ---------------------------------------------------------------------------
//
// The splitter is a single left-to-right pass over the text. A terminator
// (. ! ? … and runs of them) only ends a sentence when
//
//   1. the left context allows it — no abbreviation ("z.B.", "Dr."), no ordinal
//      or decimal ("am 3. Oktober", "3.14"), no dotted acronym ("U.S."), and
//   2. the right context looks like a new sentence — whitespace followed by an
//      upper-case-ish start, or the end of the text.
//
// Rule 2 alone already kills "example.com" and "file.txt" without a URL parser.
// A blank line ends a sentence even without punctuation, and text without any
// terminal punctuation at all stays ONE sentence, so a headline never scores as
// zero sentences (which would make every metric divide by zero).

// abbrevs are tokens that keep a following dot from ending a sentence. Stored
// lower-case and without the trailing dot. Abbreviations that contain an inner
// dot ("z.B.", "i.d.R.", "u.U.", "a.m.", "U.S.") do not need an entry: any
// token with an inner dot is treated as an abbreviation or acronym.
var abbrevs = wordSet(`
	mr mrs ms dr prof st jr sr vs etc inc ltd co corp dept approx fig no vol
	univ ave blvd rd est min max sec al ed eds cf ibid
	jan feb mar apr jun jul aug sep sept oct nov dec
	bzw ca usw evtl inkl exkl nr abb hr fr ggf ggfs vgl bspw mio mrd jh jhd
	str bzgl sog tel dt engl geb gest kap bes allg urspr insb zzgl mind
	hrsg anm bsp tsd mtl jhrl versch entspr
`)

func isTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '…':
		return true
	}
	return false
}

// isCloser reports whether r may trail a terminator and still belong to the
// sentence: `."`, `.)`, `.«`.
func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '’', '”', '“', '»', '«', '›', ')', ']', '}', '*', '_':
		return true
	}
	return false
}

// isOpener reports whether r may precede the first word of a new sentence.
func isOpener(r rune) bool {
	switch r {
	case '"', '\'', '‘', '“', '„', '«', '»', '‹', '(', '[', '{', '-', '–', '—', '*', '#', '>', '•':
		return true
	}
	return false
}

// SplitSentences splits text into sentences with byte offsets into text.
// For every returned sentence text[s.Start:s.End] == s.Text, i.e. the offsets
// span the trimmed sentence. Sentences that are empty after trimming are
// dropped.
func SplitSentences(text string) []Sentence {
	if len(text) == 0 {
		return nil
	}
	// ~80 bytes per sentence is a reasonable guess for prose; over- or
	// under-shooting only costs one re-grow.
	out := make([]Sentence, 0, len(text)/80+1)
	start, i, n := 0, 0, len(text)
	for i < n {
		c := text[i]
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r != '…' {
				i += size
				continue
			}
		} else {
			switch c {
			case '.', '!', '?', '\n':
			default:
				i++
				continue
			}
			if c == '\n' {
				// A blank line (two or more newlines in one whitespace run)
				// ends a sentence even without punctuation.
				j, newlines := i, 0
				for j < n {
					r2, s2 := utf8.DecodeRuneInString(text[j:])
					if !unicode.IsSpace(r2) {
						break
					}
					if r2 == '\n' {
						newlines++
					}
					j += s2
				}
				if newlines >= 2 {
					out = appendSentence(out, text, start, i)
					start = j
				}
				i = j
				continue
			}
		}

		// Consume a run of terminators ("?!", "...", "…").
		end := i
		for end < n {
			r2, s2 := utf8.DecodeRuneInString(text[end:])
			if !isTerminator(r2) {
				break
			}
			end += s2
		}
		// A single dot is the ambiguous case: abbreviation, ordinal, decimal,
		// acronym. Runs ("...", "?!") and ! ? … are never abbreviations.
		if end == i+1 && text[i] == '.' && !dotTerminates(text, i) {
			i = end
			continue
		}
		// Closing quotes and brackets belong to the sentence.
		for end < n {
			r2, s2 := utf8.DecodeRuneInString(text[end:])
			if !isCloser(r2) {
				break
			}
			end += s2
		}
		if !startsSentence(text, end) {
			i = end
			continue
		}
		out = appendSentence(out, text, start, end)
		start, i = end, end
	}
	// Trailing text without terminal punctuation is still a sentence.
	return appendSentence(out, text, start, n)
}

// dotTerminates reports whether the '.' at byte offset dot may end a sentence,
// judged from its left context only.
func dotTerminates(text string, dot int) bool {
	if dot > 0 {
		if r, _ := utf8.DecodeLastRuneInString(text[:dot]); unicode.IsDigit(r) {
			// A dot right after a number is an ordinal ("am 3. Oktober", "im
			// 19. Jahrhundert"), a decimal ("3.14"), a thousands separator
			// ("1.000,50") or a version ("v1.2.3") — unless the number is long
			// enough that it cannot be an ordinal, which is what makes dates
			// end sentences ("... am 15. März 2020. Danach ...").
			return digitRunBefore(text, dot) >= 3
		}
	}
	tok := tokenBefore(text, dot)
	if tok == "" {
		return true
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] == '.' {
			// Dotted acronym or multi-part abbreviation: z.B., i.d.R., U.S.
			return false
		}
	}
	if utf8.RuneCountInString(tok) == 1 {
		// Initial: "J. R. R. Tolkien".
		return false
	}
	return !isAbbrev(tok)
}

// digitRunBefore counts the digits immediately left of pos.
func digitRunBefore(text string, pos int) int {
	n, i := 0, pos
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if !unicode.IsDigit(r) {
			break
		}
		n++
		i -= size
	}
	return n
}

// tokenBefore returns the letter/digit/dot run immediately left of pos.
func tokenBefore(text string, pos int) string {
	i := pos
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:i])
		if r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}
		i -= size
	}
	return text[i:pos]
}

// isAbbrev looks tok up in abbrevs without allocating.
func isAbbrev(tok string) bool {
	const maxAbbrev = 8
	if len(tok) == 0 || len(tok) > maxAbbrev {
		return false
	}
	var buf [maxAbbrev]byte
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c < 'a' || c > 'z' {
			return false // digits or non-ASCII: never an abbreviation
		}
		buf[i] = c
	}
	_, ok := abbrevs[string(buf[:len(tok)])]
	return ok
}

// startsSentence reports whether the text at pos looks like the start of a new
// sentence: end of text, or whitespace followed by an upper-case-ish rune.
func startsSentence(text string, pos int) bool {
	if pos >= len(text) {
		return true
	}
	r, size := utf8.DecodeRuneInString(text[pos:])
	if !unicode.IsSpace(r) {
		return false // "example.com", "file.txt", "3.PM"
	}
	j := pos + size
	for j < len(text) {
		r2, s2 := utf8.DecodeRuneInString(text[j:])
		if !unicode.IsSpace(r2) {
			break
		}
		j += s2
	}
	for j < len(text) {
		r2, s2 := utf8.DecodeRuneInString(text[j:])
		if !isOpener(r2) {
			break
		}
		j += s2
	}
	if j >= len(text) {
		return true
	}
	r3, _ := utf8.DecodeRuneInString(text[j:])
	return !unicode.IsLower(r3)
}

// appendSentence trims the span [start,end) and appends it when it has content.
func appendSentence(out []Sentence, text string, start, end int) []Sentence {
	i, j := start, end
	for i < j {
		r, size := utf8.DecodeRuneInString(text[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	for j > i {
		r, size := utf8.DecodeLastRuneInString(text[i:j])
		if !unicode.IsSpace(r) {
			break
		}
		j -= size
	}
	if i >= j {
		return out
	}
	return append(out, Sentence{Text: text[i:j], Start: i, End: j})
}

// ---------------------------------------------------------------------------
// Word tokenization
// ---------------------------------------------------------------------------

// isWordRune reports whether r can start and carry a token. Unicode-aware, so
// umlauts and accented letters are ordinary word characters.
func isWordRune(r rune) bool {
	if r < utf8.RuneSelf { // fast path for ASCII
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isConnector reports whether r may sit inside a token when a word rune
// follows: apostrophes ("don't", "l'année") and hyphens ("e-mail",
// "Software-Entwicklung"). '.' and ',' are only connectors between digits
// ("3.14", "1.000,50"), which the caller checks.
func isConnector(r rune) bool {
	switch r {
	case '\'', '’', '‘', '´', '-', '‐', '.', ',':
		return true
	}
	return false
}

// nextToken finds the next word token in s at or after from and returns its
// byte range. The returned range never has leading or trailing punctuation and
// always contains at least one letter or digit. Because it returns offsets, the
// caller can slice s without allocating.
func nextToken(s string, from int) (start, end int, ok bool) {
	n := len(s)
	i := from
	for i < n {
		r, size := decode(s, i)
		if isWordRune(r) {
			break
		}
		i += size
	}
	if i >= n {
		return 0, 0, false
	}
	start = i
	for i < n {
		r, size := decode(s, i)
		if isWordRune(r) {
			i += size
			continue
		}
		if !isConnector(r) {
			break
		}
		next, _ := decode(s, i+size)
		if !isWordRune(next) {
			break
		}
		if r == '.' || r == ',' {
			// Only inside numbers: "3.14", "1.000,50" — not "end.Next".
			prev, _ := utf8.DecodeLastRuneInString(s[:i])
			if !unicode.IsDigit(prev) || !unicode.IsDigit(next) {
				break
			}
		}
		i += size
	}
	return start, i, true
}

// decode is utf8.DecodeRuneInString with an inlined ASCII fast path. It returns
// (utf8.RuneError, 0) past the end of s.
func decode(s string, i int) (rune, int) {
	if i >= len(s) {
		return utf8.RuneError, 0
	}
	if c := s[i]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	return utf8.DecodeRuneInString(s[i:])
}

// SplitWords splits a string into word tokens (no punctuation, no empty
// strings).
func SplitWords(s string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s)/6+1)
	for i := 0; ; {
		start, end, ok := nextToken(s, i)
		if !ok {
			return out
		}
		out = append(out, s[start:end])
		i = end
	}
}

// measure returns the length of tok in runes and the number of those runes that
// are letters or digits (the "characters" every readability formula counts).
func measure(tok string) (runes, chars int) {
	for _, r := range tok {
		runes++
		if isWordRune(r) {
			chars++
		}
	}
	return runes, chars
}

// wordSet builds a lookup set from a whitespace-separated list.
func wordSet(list string) map[string]struct{} {
	m := make(map[string]struct{}, 128)
	start := -1
	for i := 0; i <= len(list); i++ {
		if i < len(list) && !isASCIISpace(list[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			m[list[start:i]] = struct{}{}
			start = -1
		}
	}
	return m
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
