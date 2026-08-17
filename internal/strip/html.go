package strip

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Elements whose *content* is not prose
// ---------------------------------------------------------------------------
//
// Everything below is removed together with what it contains. Comments are
// obvious, <script> and <style> are code, <head> is metadata, and <noscript> is
// a fallback the reader of a rendered page never sees.
//
// Each element needs its own regexp because RE2 has no backreferences, so the
// closing tag cannot be spelled as \1.
var (
	htmlScriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	htmlStyleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	htmlNoscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript\s*>`)
	htmlHeadRe     = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head\s*>`)
	// An unclosed <head> is common in hand-written and templated HTML; <body>
	// implicitly closes it. Losing the <body> tag itself costs nothing.
	htmlHeadOpenRe = regexp.MustCompile(`(?is)<head\b[^>]*>.*?<body\b[^>]*>`)
	// An unterminated <script>/<style> means the rest of the document is code.
	// The same assumption for <head> would be far too destructive, so it is not
	// made there.
	htmlScriptOpenRe  = regexp.MustCompile(`(?is)<script\b[^>]*>[\s\S]*\z`)
	htmlStyleOpenRe   = regexp.MustCompile(`(?is)<style\b[^>]*>[\s\S]*\z`)
	htmlCommentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlCommentOpenRe = regexp.MustCompile(`(?s)<!--[\s\S]*\z`)

	htmlWhitespaceRe = regexp.MustCompile(`[ \t\n\f\v\x{00a0}\x{200b}]+`)
)

// blockTags are the elements that end a line of prose when a browser renders
// them. They become sentence boundaries; every other element (a, em, strong,
// span, code, …) is inline and must vanish without leaving whitespace behind,
// or "<span>Sprach</span><span>raum</span>" would become two words.
var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"body": true, "br": true, "caption": true, "center": true, "col": true,
	"colgroup": true, "dd": true, "details": true, "dialog": true, "dir": true,
	"div": true, "dl": true, "dt": true, "fieldset": true, "figcaption": true,
	"figure": true, "footer": true, "form": true, "frame": true,
	"frameset": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hgroup": true, "hr": true,
	"html": true, "iframe": true, "legend": true, "li": true, "main": true,
	"menu": true, "nav": true, "ol": true, "option": true, "p": true,
	"pre": true, "section": true, "summary": true, "table": true,
	"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true,
	"tr": true, "ul": true,
}

// stripHTML reduces an HTML fragment or document to prose.
//
// Limitation: <pre> content is treated like any other block, i.e. its
// significant whitespace is collapsed. Preformatted text is code far more often
// than it is prose, and either way its line breaks are not sentence boundaries.
func stripHTML(src string) string {
	src = normalizeNewlines(src)
	src = dropRawElements(src)
	// Collapse source whitespace before the tag pass: in HTML a newline inside
	// a paragraph is just a space, and all structure comes from the tags. Doing
	// this first means every newline in the result was put there by a block tag.
	src = htmlWhitespaceRe.ReplaceAllString(src, " ")
	text := stripTags(src, "\n\n")
	text = decodeEntities(text)
	return normalizeProse(terminateBlocks(text))
}

// dropRawElements removes the elements whose contents are not prose.
func dropRawElements(src string) string {
	if !strings.Contains(src, "<") {
		return src
	}
	src = htmlScriptRe.ReplaceAllString(src, " ")
	src = htmlStyleRe.ReplaceAllString(src, " ")
	src = htmlNoscriptRe.ReplaceAllString(src, " ")
	src = htmlHeadRe.ReplaceAllString(src, " ")
	src = htmlHeadOpenRe.ReplaceAllString(src, " ")
	src = htmlScriptOpenRe.ReplaceAllString(src, " ")
	src = htmlStyleOpenRe.ReplaceAllString(src, " ")
	src = htmlCommentRe.ReplaceAllString(src, " ")
	return htmlCommentOpenRe.ReplaceAllString(src, " ")
}

// stripTags removes every tag, replacing block-level ones with blockSep and
// inline ones with nothing.
//
// Limitation: it follows the HTML5 rule that '<' plus a letter starts a tag, so
// an unescaped "a<b … >" in text is eaten exactly as a browser would eat it.
// "5 < 10" (a '<' followed by a space) is safe, and in valid markup the other
// case is written "&lt;".
func stripTags(src, blockSep string) string {
	if !strings.Contains(src, "<") {
		return src
	}
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); {
		if src[i] != '<' {
			b.WriteByte(src[i])
			i++
			continue
		}
		name, end, ok := parseTag(src, i)
		if !ok {
			b.WriteByte(src[i])
			i++
			continue
		}
		if blockTags[name] {
			b.WriteString(blockSep)
		}
		i = end
	}
	return b.String()
}

// parseTag parses the tag starting at src[i] == '<'. It returns the lower-cased
// element name (empty for a doctype or processing instruction), the offset just
// past the closing '>', and whether src[i:] really was a tag. Quoted attribute
// values may contain '>', so they are scanned rather than skipped with IndexByte.
func parseTag(src string, i int) (name string, end int, ok bool) {
	j := i + 1
	if j >= len(src) {
		return "", 0, false
	}
	if src[j] == '!' || src[j] == '?' {
		k := strings.IndexByte(src[j:], '>')
		if k < 0 {
			return "", 0, false
		}
		return "", j + k + 1, true // doctype, CDATA, processing instruction
	}
	if src[j] == '/' {
		j++
	}
	start := j
	for j < len(src) && isTagNameByte(src[j], j == start) {
		j++
	}
	if j == start || j >= len(src) {
		return "", 0, false
	}
	switch src[j] {
	case ' ', '\t', '\n', '\r', '\f', '/', '>':
	default:
		return "", 0, false // "a<b" is not a tag
	}
	name = strings.ToLower(src[start:j])
	for j < len(src) {
		switch c := src[j]; c {
		case '"', '\'':
			j++
			for j < len(src) && src[j] != c {
				j++
			}
			if j >= len(src) {
				return "", 0, false
			}
			j++
		case '>':
			return name, j + 1, true
		default:
			j++
		}
	}
	return "", 0, false // unterminated: treat as text
}

func isTagNameByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case first:
		return false
	case c >= '0' && c <= '9', c == '-', c == '_', c == ':':
		return true
	}
	return false
}

// terminateBlocks turns the block separators left by stripTags into terminated
// paragraphs, so that every element that ended a line in the rendered page also
// ends a sentence. See terminate for why this matters so much.
func terminateBlocks(text string) string {
	chunks := strings.Split(text, "\n")
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		c = collapseSpaces(c)
		if !hasProse(c) {
			continue
		}
		out = append(out, terminate(c))
	}
	return strings.Join(out, "\n\n")
}

// ---------------------------------------------------------------------------
// Entities
// ---------------------------------------------------------------------------

// namedEntities covers what actually shows up in text content. German content
// is full of entity-encoded umlauts, and leaving "&auml;" raw would corrupt the
// syllable count (three phantom syllables) and the character count alike.
var namedEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'",
	"nbsp": " ", "ensp": " ", "emsp": " ", "thinsp": " ", "shy": "",
	"auml": "ä", "ouml": "ö", "uuml": "ü", "szlig": "ß",
	"Auml": "Ä", "Ouml": "Ö", "Uuml": "Ü",
	"aacute": "á", "agrave": "à", "acirc": "â", "aring": "å", "atilde": "ã",
	"eacute": "é", "egrave": "è", "ecirc": "ê", "euml": "ë",
	"iacute": "í", "igrave": "ì", "icirc": "î", "iuml": "ï",
	"oacute": "ó", "ograve": "ò", "ocirc": "ô", "otilde": "õ", "oslash": "ø",
	"uacute": "ú", "ugrave": "ù", "ucirc": "û",
	"ccedil": "ç", "ntilde": "ñ", "yuml": "ÿ",
	"Aacute": "Á", "Agrave": "À", "Eacute": "É", "Egrave": "È",
	"Iacute": "Í", "Oacute": "Ó", "Uacute": "Ú", "Ccedil": "Ç", "Ntilde": "Ñ",
	"ndash": "–", "mdash": "—", "hellip": "…", "middot": "·", "bull": "•",
	"laquo": "«", "raquo": "»", "lsquo": "‘", "rsquo": "’", "sbquo": "‚",
	"ldquo": "“", "rdquo": "”", "bdquo": "„", "prime": "′", "Prime": "″",
	"copy": "©", "reg": "®", "trade": "™", "sect": "§", "para": "¶",
	"euro": "€", "pound": "£", "yen": "¥", "cent": "¢", "deg": "°",
	"plusmn": "±", "times": "×", "divide": "÷", "frac12": "½", "frac14": "¼",
	"sup2": "²", "sup3": "³", "micro": "µ", "dagger": "†", "permil": "‰",
	"larr": "←", "rarr": "→", "harr": "↔", "hearts": "♥", "star": "★",
}

// maxEntityLen bounds the lookahead for a ';' so that a stray '&' in prose
// ("Tom & Jerry sagte, ...") costs one byte comparison, not a scan.
const maxEntityLen = 32

// decodeEntities replaces HTML entities with the characters they stand for. It
// runs after tag stripping, so a decoded "&lt;p&gt;" can never be mistaken for
// a tag; and it decodes exactly once, so "&amp;lt;" correctly yields "&lt;".
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		limit := i + maxEntityLen
		if limit > len(s) {
			limit = len(s)
		}
		semi := strings.IndexByte(s[i:limit], ';')
		if semi <= 1 {
			b.WriteByte('&')
			i++
			continue
		}
		if rep, ok := decodeEntity(s[i+1 : i+semi]); ok {
			b.WriteString(rep)
			i += semi + 1
			continue
		}
		b.WriteByte('&')
		i++
	}
	return b.String()
}

func decodeEntity(ent string) (string, bool) {
	if ent == "" {
		return "", false
	}
	if ent[0] != '#' {
		rep, ok := namedEntities[ent]
		return rep, ok
	}
	digits, base := ent[1:], 10
	if len(digits) > 1 && (digits[0] == 'x' || digits[0] == 'X') {
		digits, base = digits[1:], 16
	}
	n, err := strconv.ParseUint(digits, base, 32)
	if err != nil || n == 0 || n > unicode.MaxRune || (n >= 0xD800 && n <= 0xDFFF) {
		return "", false
	}
	return runeText(rune(n)), true
}

// runeText renders a decoded code point as text. Anything that is whitespace or
// invisible becomes a plain space (or nothing), because a non-breaking space or
// a zero-width joiner inside a word would either fuse two words or hide a word
// boundary from the tokenizer.
func runeText(r rune) string {
	switch {
	case r == 0x00AD, r == 0x200B, r == 0x200C, r == 0x200D, r == 0xFEFF:
		return ""
	case unicode.IsSpace(r), unicode.IsControl(r):
		return " "
	case !utf8.ValidRune(r):
		return ""
	}
	return string(r)
}
