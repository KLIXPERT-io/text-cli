package doc

import (
	"strconv"
	"strings"
)

// mdBuilder assembles the markdown a decoder returns.
//
// It exists so six decoders cannot disagree about what a heading looks like.
// The strip pass downstream re-encodes a block boundary as sentence
// punctuation, and it can only do that for boundaries that survived the
// decode — so the one thing every decoder must get right is emitting *blocks*,
// separated by blank lines, rather than a wall of text. This type makes that
// the easy path: every Add method separates itself from the previous block.
//
// It deliberately does not escape text. A paragraph that happens to begin with
// "#" becomes a heading, which costs one sentence terminator that was going to
// be added anyway, while escaping would leave backslashes in the prose that
// the tokenizer then counts as words.
type mdBuilder struct {
	blocks []string

	// open is the block still being written to, and kind says which sort it is.
	//
	// A list and a table are the two blocks that grow a line at a time, and they
	// are the reason this is a builder rather than a string: appending a line to
	// the block with += copies the whole block, so a table costs the square of
	// its own size to assemble. That is not a micro-optimisation — an ordinary
	// spreadsheet export runs to hundreds of thousands of rows, and squared it
	// turns a one-megabyte .ods into minutes of copying inside a CLI that has
	// already told the user its input limit is ten megabytes.
	open strings.Builder
	kind mdKind
}

// mdKind is the sort of block currently open, if any.
type mdKind int

const (
	mdNone mdKind = iota
	mdList
	mdTable
)

// close finishes the open block, if there is one. Every method that starts a
// block of a different sort calls it first, which is what keeps a paragraph
// beginning with "- " from swallowing the list item that follows it.
func (b *mdBuilder) close() {
	if b.kind == mdNone {
		return
	}
	b.blocks = append(b.blocks, b.open.String())
	b.open.Reset()
	b.kind = mdNone
}

// line appends one line to an open block of the given kind, opening one if the
// block currently open is of another kind.
func (b *mdBuilder) line(kind mdKind, s string) {
	if b.kind != kind {
		b.close()
		b.kind = kind
	} else {
		b.open.WriteByte('\n')
	}
	b.open.WriteString(s)
}

// Heading appends a heading at the given level, clamped to markdown's 1–6.
func (b *mdBuilder) Heading(level int, text string) {
	text = cleanInline(text)
	if text == "" {
		return
	}
	if level < 1 {
		level = 1
	}
	if level > 6 {
		level = 6
	}
	b.close()
	b.blocks = append(b.blocks, strings.Repeat("#", level)+" "+text)
}

// Para appends a paragraph.
func (b *mdBuilder) Para(text string) {
	text = cleanInline(text)
	if text == "" {
		return
	}
	b.close()
	b.blocks = append(b.blocks, text)
}

// Item appends a list item at an indent depth, ordered or not. Consecutive
// items form one list block, because a blank line between every item would
// make each of them a paragraph and change nothing else.
func (b *mdBuilder) Item(depth int, ordered bool, text string) {
	text = cleanInline(text)
	if text == "" {
		return
	}
	if depth < 0 {
		depth = 0
	}
	marker := "-"
	if ordered {
		marker = "1."
	}
	b.line(mdList, strings.Repeat("  ", depth)+marker+" "+text)
}

// Row appends one table row. The header separator is the caller's business:
// the strip pass reads a table row as a sentence either way, and inventing a
// header for a table that had none would put a made-up line in the prose.
func (b *mdBuilder) Row(cells []string) {
	out := make([]string, 0, len(cells))
	empty := true
	for _, c := range cells {
		c = cleanInline(c)
		if c != "" {
			empty = false
		}
		out = append(out, c)
	}
	if empty {
		return
	}
	b.line(mdTable, "| "+strings.Join(out, " | ")+" |")
}

// Empty reports whether nothing has been added yet.
func (b *mdBuilder) Empty() bool { return len(b.blocks) == 0 && b.kind == mdNone }

// String renders the document: one blank line between blocks, trailing
// newline, nothing else.
func (b *mdBuilder) String() string {
	b.close()
	if len(b.blocks) == 0 {
		return ""
	}
	return strings.Join(b.blocks, "\n\n") + "\n"
}

// cleanInline normalises one block's text: soft hyphens and zero-width joiners
// out, every run of whitespace to a single space, trimmed.
//
// The invisible characters matter more than they look. A soft hyphen inside a
// justified PDF word splits it into two "words" for the tokenizer and changes
// the syllable count of both, and a non-breaking space is not whitespace to
// Go's unicode.IsSpace-free string functions unless it is normalised here.
func cleanInline(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch r {
		case '\u00ad', '\u200b', '\u200c', '\u200d', '\ufeff':
			// Soft hyphen and zero-width marks: invisible to a reader, a word
			// boundary to a tokenizer. Drop them.
			continue
		case '\u00a0', '\u2007', '\u202f':
			// Non-breaking spaces: a space to every reader, and not one to a
			// tokenizer that only knows ASCII whitespace.
			r = ' '
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// itoa is a readability shim for the decoders that number slides and pages.
func itoa(n int) string { return strconv.Itoa(n) }
