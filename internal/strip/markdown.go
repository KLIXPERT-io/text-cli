package strip

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Every pattern is compiled once: stripping runs once per document, but a
// regexp compiled inside a function would recompile once per line.
var (
	// A reference definition is not prose. The label must not start with '^',
	// which is a footnote definition and does carry prose.
	mdRefDefRe = regexp.MustCompile(`^ {0,3}\[[^^\]][^\]]*\][ \t]*:[ \t]*\S+.*$`)
	// Footnote definition: drop the marker, keep the note text.
	mdFootnoteDefRe = regexp.MustCompile(`^ {0,3}\[\^[^\]]+\][ \t]*:[ \t]*`)
	mdFootnoteRefRe = regexp.MustCompile(`\[\^[^\]]+\]`)

	mdATXRe       = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	mdTrailHashRe = regexp.MustCompile(`[ \t]+#+[ \t]*$`)
	// A setext underline turns the line above it into a heading. '=' is
	// unambiguous; '-' needs at least two characters so that a lone "-" bullet
	// is not mistaken for one.
	mdSetextRe = regexp.MustCompile(`^ {0,3}(?:=+|--+)[ \t]*$`)
	mdHRRe     = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	mdQuoteRe  = regexp.MustCompile(`^[ \t]*(?:>[ \t]?)+`)
	mdListRe   = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d{1,9}[.)])[ \t]+`)
	mdTaskRe   = regexp.MustCompile(`^\[[ xX]\][ \t]*`)
	// The alignment row of a table: pipes, dashes, colons and spaces only.
	mdTableSepRe = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-*:?[ \t]*)*\|?[ \t]*$`)

	mdImageRe = regexp.MustCompile(`!\[[^\]]*\](?:\([^)]*\)|\[[^\]]*\])?`)
	// The URL part allows one level of nested parentheses, which is what
	// Wikipedia-style links need.
	mdLinkRe    = regexp.MustCompile(`\[([^\[\]]*)\]\((?:[^()]|\([^()]*\))*\)`)
	mdRefLinkRe = regexp.MustCompile(`\[([^\[\]]*)\]\[[^\]]*\]`)
	mdBracketRe = regexp.MustCompile(`\[([^\[\]]*)\]`)
	// {#custom-id} and {.class} attribute blocks.
	mdAttrRe = regexp.MustCompile(`[ \t]*\{[#.][^}\n]*\}`)
	// <https://…>, <mailto:…> and bare <user@host> autolinks.
	mdAutolinkRe = regexp.MustCompile(`(?i)<(?:https?|ftp|mailto):[^>\s]*>|<[^\s<>@]+@[^\s<>@]+>`)
	// A bare URL must not swallow the sentence terminator that follows it, so
	// it may not end on punctuation.
	mdBareURLRe = regexp.MustCompile(`(?i)\b(?:https?://|ftp://|www\.)[^\s<>()\[\]"']*[^\s<>()\[\]"'.,;:!?]`)

	mdEmRe    = regexp.MustCompile(`\*([^*\n]+)\*`)
	mdUnderRe = regexp.MustCompile(`(^|[^\w])_+([^_\n]+)_+($|[^\w])`)
)

// htmlBlockMark stands in for an HTML block boundary between the tag pass and
// the line pass. It is a control character, so it can never occur in prose and
// any stray one is dropped as a wordless line by normalizeProse.
const htmlBlockMark = "\x1e"

// escapable lists the characters Markdown lets you escape with a backslash.
// "\*not emphasis\*" has to survive every pass that hunts for those same
// characters, so an escape is swapped for a private-use rune up front and
// swapped back at the very end. Undoing escapes at the end instead would be too
// late: the emphasis pass would already have eaten the asterisks.
const escapable = "\\`*_{}[]()#+-.!>~|"

// escapeBase is the start of a Unicode private-use area block; no real document
// contains these code points, so they cannot collide with content.
const escapeBase = rune(0xE000)

func protectEscapes(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if idx := strings.IndexByte(escapable, s[i+1]); idx >= 0 {
				b.WriteRune(escapeBase + rune(idx))
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func restoreEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if idx := r - escapeBase; idx >= 0 && int(idx) < len(escapable) {
			b.WriteByte(escapable[idx])
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripMarkdown reduces a Markdown document to the prose a reader actually
// reads. The passes are ordered so that each one only ever sees text the
// earlier ones could not claim: code first (its contents must never be parsed
// as Markdown or HTML), then raw HTML, then the line-oriented block markers,
// then the inline markers.
func stripMarkdown(src string) string {
	src = normalizeNewlines(src)
	src = trimFrontMatter(src)
	src = removeCodeBlocks(src)
	escaped := strings.Contains(src, `\`)
	if escaped {
		src = protectEscapes(src)
	}
	src = removeCodeSpans(src)
	src = dropRawElements(src)
	src = mdAutolinkRe.ReplaceAllString(src, "")
	// Embedded HTML blocks are marked rather than merely broken, so that the
	// line loop below can end the sentence they close, exactly as it does for a
	// heading. Inline tags leave nothing at all behind.
	src = stripTags(src, "\n"+htmlBlockMark+"\n")

	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	// Index in out of the last emitted prose line, i.e. the line a setext
	// underline would turn into a heading. -1 when there is none.
	prev := -1
	// openItem is true while a list item is still waiting for its terminator
	// because it ran over more than one line.
	openItem := false

	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			prev, openItem = -1, false
			continue
		}
		if strings.TrimSpace(line) == htmlBlockMark {
			// An embedded HTML block ended here: give the prose it contained a
			// terminator and a paragraph break, like any other block.
			if prev >= 0 {
				out[prev] = terminate(out[prev])
			}
			out = append(out, "")
			prev, openItem = -1, false
			continue
		}
		line = mdQuoteRe.ReplaceAllString(line, "") // blockquote markers
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			prev, openItem = -1, false
			continue
		}
		if mdRefDefRe.MatchString(line) {
			continue
		}
		if mdSetextRe.MatchString(line) {
			if prev >= 0 {
				// The line above was a heading after all.
				out[prev] = terminate(out[prev])
				prev = -1
				continue
			}
			// No preceding text: it was a horizontal rule.
			out = append(out, "")
			continue
		}
		if mdHRRe.MatchString(line) {
			out = append(out, "")
			prev, openItem = -1, false
			continue
		}
		if m := mdATXRe.FindStringSubmatch(line); m != nil {
			text := inlineMarkdown(mdTrailHashRe.ReplaceAllString(m[2], ""))
			// A heading has no terminal punctuation of its own. Without one it
			// fuses with the paragraph below into a single enormous sentence,
			// which is the biggest single source of error in scoring Markdown.
			out = append(out, terminate(text))
			prev, openItem = -1, false
			continue
		}
		if strings.Contains(line, "|") {
			if mdTableSepRe.MatchString(line) {
				out = append(out, "")
				prev, openItem = -1, false
				continue
			}
			out = append(out, terminate(tableRow(line)))
			prev, openItem = -1, false
			continue
		}
		list := mdListRe.MatchString(line)
		if list {
			line = mdListRe.ReplaceAllString(line, "")
			line = mdTaskRe.ReplaceAllString(line, "") // task list checkbox
			openItem = true
		}
		line = mdFootnoteDefRe.ReplaceAllString(line, "")
		line = inlineMarkdown(line)
		if openItem && !continuesItem(lines, i) {
			// A list item is a block: it ends where the next item begins, and
			// like a heading it usually carries no terminal punctuation. An item
			// spanning several lines is terminated on its last line only, so its
			// own wrapped text is not chopped into fragments.
			line = terminate(line)
			prev, openItem = -1, false
		} else {
			prev = len(out)
		}
		out = append(out, line)
	}

	res := normalizeProse(decodeEntities(strings.Join(out, "\n")))
	if escaped {
		res = restoreEscapes(res)
	}
	return res
}

// inlineMarkdown removes the inline markers from one line of text. Order
// matters: images before links (an image inside a link must not leave its alt
// text behind), and links before emphasis (a URL may contain '_' and '*').
func inlineMarkdown(s string) string {
	if s == "" {
		return s
	}
	s = mdFootnoteRefRe.ReplaceAllString(s, "")
	// Image alt text is not read as body prose, so it goes entirely, unlike
	// link text which is.
	s = mdImageRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = mdRefLinkRe.ReplaceAllString(s, "$1")
	s = mdAttrRe.ReplaceAllString(s, "")
	s = mdBareURLRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "~~", "")
	s = strings.ReplaceAll(s, "**", "")
	s = mdEmRe.ReplaceAllString(s, "$1")
	s = mdUnderRe.ReplaceAllString(s, "$1$2$3")
	// Whatever brackets are left are shortcut reference links ("[Anthropic]")
	// whose definition was dropped above.
	return mdBracketRe.ReplaceAllString(s, "$1")
}

// trimFrontMatter removes a YAML or TOML front-matter block, which is metadata
// and not prose. It only ever applies at the very start of the document, and
// only when the block is closed — an unterminated "---" is a horizontal rule or
// a setext underline, not front matter.
func trimFrontMatter(src string) string {
	for _, delim := range []string{"---", "+++"} {
		if !strings.HasPrefix(src, delim) {
			continue
		}
		lines := strings.Split(src, "\n")
		if strings.TrimRight(lines[0], " \t") != delim {
			continue
		}
		for i := 1; i < len(lines); i++ {
			switch strings.TrimRight(lines[i], " \t") {
			case delim, "...":
				return strings.Join(lines[i+1:], "\n")
			}
		}
	}
	return src
}

// removeCodeBlocks blanks out fenced and indented code blocks. The lines are
// replaced with empty ones rather than deleted so that the paragraph structure
// around them survives.
//
// Limitation: an indented block is only recognised outside list context, since
// four spaces inside a list is a continuation paragraph, not code. Telling the
// two apart exactly needs a real block parser.
func removeCodeBlocks(src string) string {
	lines := strings.Split(src, "\n")
	var (
		fenceChar byte
		fenceLen  int
		inFence   bool
		inIndent  bool
		inList    bool
		prevBlank = true
	)
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		blank := strings.TrimSpace(line) == ""
		indent := indentWidth(line)

		if inFence {
			if isClosingFence(trimmed, fenceChar, fenceLen) {
				inFence = false
			}
			lines[i] = ""
			continue
		}
		if inIndent {
			if blank || indent >= 4 {
				lines[i] = ""
				continue
			}
			inIndent = false
		}
		if blank {
			lines[i] = ""
			prevBlank = true
			continue
		}
		if c, n, ok := openFence(trimmed); ok && indent < 4 {
			// An unclosed fence runs to the end of the input, which is what
			// CommonMark says and also the only safe reading.
			inFence, fenceChar, fenceLen = true, c, n
			lines[i] = ""
			prevBlank = false
			continue
		}
		switch {
		case mdListRe.MatchString(line):
			inList = true
		case indent < 4 && prevBlank:
			inList = false
		}
		if !inList && prevBlank && indent >= 4 {
			inIndent = true
			lines[i] = ""
			continue
		}
		prevBlank = false
	}
	return strings.Join(lines, "\n")
}

// openFence reports whether the (left-trimmed) line opens a code fence.
func openFence(line string) (char byte, length int, ok bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	c := line[0]
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	// A backtick info string may not contain a backtick: "``code`` text" is an
	// inline span, not a fence.
	if c == '`' && strings.ContainsRune(line[n:], '`') {
		return 0, 0, false
	}
	return c, n, true
}

// isClosingFence reports whether the (left-trimmed) line closes a fence opened
// with length characters of char: same character, at least as long, nothing else.
func isClosingFence(line string, char byte, length int) bool {
	n := 0
	for n < len(line) && line[n] == char {
		n++
	}
	return n >= length && strings.TrimSpace(line[n:]) == ""
}

// indentWidth counts leading indentation with tabs worth four columns.
func indentWidth(line string) int {
	w := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			w++
		case '\t':
			w += 4
		default:
			return w
		}
	}
	return w
}

// removeCodeSpans drops inline code spans together with their contents: code is
// not prose, and `client.Post(ctx, &req)` tokenizes into five nonsense words.
//
// Limitation: spans are matched within a single line. A code span wrapped
// across a newline is rare in real documents and matching across lines would
// risk swallowing whole paragraphs on an unbalanced backtick.
func removeCodeSpans(src string) string {
	if !strings.Contains(src, "`") {
		return src
	}
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		lines[i] = removeLineCodeSpans(line)
	}
	return strings.Join(lines, "\n")
}

func removeLineCodeSpans(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	for i := 0; i < len(line); {
		if line[i] != '`' {
			b.WriteByte(line[i])
			i++
			continue
		}
		open := i
		for i < len(line) && line[i] == '`' {
			i++
		}
		n := i - open
		if closed, ok := findCodeSpanEnd(line, i, n); ok {
			i = closed
			continue
		}
		// Unbalanced backticks: keep the text, drop the ticks.
	}
	return b.String()
}

// findCodeSpanEnd looks for a run of exactly n backticks at or after from and
// returns the offset just past it.
func findCodeSpanEnd(line string, from, n int) (int, bool) {
	for i := from; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(line) && line[j] == '`' {
			j++
		}
		if j-i == n {
			return j, true
		}
		i = j
	}
	return 0, false
}

// tableRow keeps the cell text of a table row and drops the pipes. Cells are
// joined with commas when they do not already end in punctuation, and the
// caller terminates the row, so a table contributes one sentence per row
// instead of one sentence for the whole table.
//
// Limitation: an escaped pipe inside a cell still splits the cell.
func tableRow(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var b strings.Builder
	for _, cell := range strings.Split(line, "|") {
		cell = strings.TrimSpace(inlineMarkdown(cell))
		if !hasProse(cell) {
			continue
		}
		if b.Len() > 0 {
			if last := lastRune(b.String()); isTerminator(last) || last == ',' || last == ';' {
				b.WriteByte(' ')
			} else {
				b.WriteString(", ")
			}
		}
		b.WriteString(cell)
	}
	return b.String()
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// continuesItem reports whether the line after i continues the current list
// item rather than starting something new. Only an indented, non-marker line
// counts; Markdown's lazy continuation (an unindented line continuing an item)
// is deliberately not honoured, because it is indistinguishable from the next
// paragraph without a real block parser and the cost of guessing wrong is a
// missing sentence boundary.
func continuesItem(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	next := lines[i+1]
	if strings.TrimSpace(next) == "" {
		return false
	}
	if next[0] != ' ' && next[0] != '\t' {
		return false
	}
	return !mdListRe.MatchString(next) && !mdATXRe.MatchString(next) && !strings.Contains(next, "|")
}
