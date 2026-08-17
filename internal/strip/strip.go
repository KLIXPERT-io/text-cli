// Package strip reduces markup to the prose a human actually reads.
//
// Readability formulas count words, sentences and syllables. Feeding them raw
// Markdown or HTML makes them count the markup too: a fenced code block turns
// into dozens of one-syllable "words" with no sentence terminator, a URL turns
// into one very long "word", and a heading fuses into the paragraph below it.
// A single code block can move a Flesch score by twenty points, so the score
// stops describing the prose and starts describing the markup.
//
// This package is deliberately not a Markdown or HTML parser. It is a prose
// extractor: where a full parse would be needed to be exactly right, it takes
// the pragmatic path and documents the limitation. Being slightly wrong about
// an exotic construct is much cheaper than refusing to score a document.
package strip

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Mode names a preprocessing strategy.
type Mode string

const (
	ModeNone     Mode = "none"     // pass the text through untouched
	ModeMarkdown Mode = "markdown" // strip Markdown (and any HTML embedded in it)
	ModeHTML     Mode = "html"     // strip HTML
	ModeAuto     Mode = "auto"     // sniff the input and pick
)

// modes is the canonical order used for help text: the two explicit formats
// between the two meta-modes that bracket them.
var modes = []Mode{ModeNone, ModeMarkdown, ModeHTML, ModeAuto}

// Modes returns every mode name, for help text and validation.
func Modes() []string {
	out := make([]string, len(modes))
	for i, m := range modes {
		out[i] = string(m)
	}
	return out
}

// Valid reports whether s names a supported mode.
func Valid(s string) bool {
	for _, m := range modes {
		if string(m) == s {
			return true
		}
	}
	return false
}

// Apply reduces markup to prose per the mode. It never returns an error:
// unparseable markup degrades to best-effort text, because refusing to score a
// document because its markup was odd would be worse than scoring it slightly
// wrong.
//
// ModeNone — and ModeAuto on input that sniffs as plain text — is a strict
// identity: whitespace normalisation is part of stripping markup, not something
// this package does to text it was told to leave alone.
func Apply(text string, mode Mode) string {
	switch mode {
	case ModeMarkdown:
		return stripMarkdown(text)
	case ModeHTML:
		return stripHTML(text)
	case ModeAuto:
		if m := Detect(text); m != ModeAuto && m != ModeNone {
			return Apply(text, m)
		}
		return text
	default:
		// ModeNone and anything unrecognised. An unknown mode cannot be
		// reported (Apply has no error return) and dropping the document would
		// be worse than passing it through, so pass it through.
		return text
	}
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

var (
	// A document-level HTML element is conclusive wherever it appears: prose
	// does not casually contain <html> or <body>.
	detectHTMLDocRe = regexp.MustCompile(`(?i)<!doctype\s+html|</?html\b|</?body\b`)

	// Block-level tags, used to count *distinct* elements. One stray <br> in a
	// Markdown file must not tip the balance, three different block elements
	// almost certainly mean the document is HTML.
	detectBlockTagRe = regexp.MustCompile(`(?i)<(/?)(p|div|section|article|aside|header|footer|nav|main|ul|ol|li|table|thead|tbody|tr|td|th|h[1-6]|blockquote|pre|figure|form|br|hr|dl|dt|dd)\b[^>]*>`)

	detectFrontMatterRe = regexp.MustCompile(`\A(---|\+\+\+)[ \t]*\n`)
	detectFenceRe       = regexp.MustCompile("(?m)^ {0,3}(```|~~~)")
	detectHeadingRe     = regexp.MustCompile(`(?m)^ {0,3}#{1,6}[ \t]+\S`)
	detectListRe        = regexp.MustCompile(`(?m)^ {0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+\S`)
	detectLinkRe        = regexp.MustCompile(`!?\[[^\]\n]*\]\([^)\n]*\)`)
	detectRefDefRe      = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:[ \t]*\S`)
	detectTableRe       = regexp.MustCompile(`(?m)^ {0,3}\|.*\|[ \t]*$`)
)

// Detect sniffs whether text looks like Markdown, HTML, or neither.
//
// Ambiguous input prefers Markdown: Markdown may legally embed HTML, and the
// Markdown stripper also strips HTML, so guessing Markdown degrades gracefully
// while guessing HTML would leave every Markdown construct in the text.
func Detect(text string) Mode {
	if strings.TrimSpace(text) == "" {
		return ModeNone
	}
	if detectHTMLDocRe.MatchString(text) {
		return ModeHTML
	}
	if hasMarkdownSignal(text) {
		return ModeMarkdown
	}
	if tags, distinct := countBlockTags(text); distinct >= 3 || tags >= 4 {
		return ModeHTML
	}
	return ModeNone
}

func hasMarkdownSignal(text string) bool {
	return detectFrontMatterRe.MatchString(text) ||
		detectFenceRe.MatchString(text) ||
		detectHeadingRe.MatchString(text) ||
		detectListRe.MatchString(text) ||
		detectLinkRe.MatchString(text) ||
		detectRefDefRe.MatchString(text) ||
		detectTableRe.MatchString(text)
}

// countBlockTags returns the number of block-tag occurrences and the number of
// distinct block element names.
func countBlockTags(text string) (total, distinct int) {
	seen := make(map[string]struct{}, 8)
	for _, m := range detectBlockTagRe.FindAllStringSubmatch(text, -1) {
		total++
		seen[strings.ToLower(m[2])] = struct{}{}
	}
	return total, len(seen)
}

// ---------------------------------------------------------------------------
// Shared text helpers
// ---------------------------------------------------------------------------

// dupTerminatorRe collapses "runs" of terminators that stripping can leave
// behind ("Heading. . Body"). Each phantom sentence deflates every per-sentence
// average, so they are worth one extra pass. Terminators that are directly
// adjacent ("?!", "...") are intentional and left alone.
var dupTerminatorRe = regexp.MustCompile(`([.!?…])(?:[ \t]*[,;:]?[ \t]+[.!?…])+`)

// spaceBeforePunctRe cleans up after a removal that sat between a word and its
// punctuation ("read <https://…>." leaves "read ."). Cosmetic for the metrics,
// but the stripped text is also something a user can print.
var spaceBeforePunctRe = regexp.MustCompile(`[ \t]+([.,;:!?…])`)

func normalizeNewlines(s string) string {
	if strings.IndexByte(s, '\r') < 0 {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// hasProse reports whether s contains anything a tokenizer would count as a
// word. Lines that fail this test are dropped rather than emitted, which is
// what keeps a stripped-empty heading from becoming a lone "." sentence.
func hasProse(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// collapseSpaces trims a line and squeezes every run of whitespace inside it to
// a single space. Removing markup leaves double spaces everywhere; a tokenizer
// does not care, but a human reading `--strip`ped output does.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
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

// terminate makes sure s ends a sentence.
//
// THIS IS THE MOST IMPORTANT FUNCTION IN THE PACKAGE. Headings, list items,
// table rows and HTML block elements carry their sentence boundary in the
// markup, not in punctuation. Once "## Installation" loses its '#' markers it
// is just a line of text, and every sentence splitter will happily glue it to
// the paragraph underneath: "Installation The tool ships as a single binary."
// One document-sized sentence follows from a handful of such fusions, the
// average sentence length explodes, and the reading-ease score collapses. So
// whenever markup ended a block, we re-encode that boundary as punctuation.
//
// It is idempotent, it never produces ".." (an existing terminator, optionally
// behind a closing quote or bracket, is left alone), and a trailing ':' ';' or
// ',' is promoted rather than appended to, so a heading like "Note:" does not
// become "Note:.".
func terminate(s string) string {
	s = strings.TrimRight(s, " \t")
	if s == "" || !hasProse(s) {
		return s
	}
	// Look back past closing quotes and brackets: `("Done.")` already ends a
	// sentence.
	end := len(s)
	for end > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:end])
		if !isCloser(r) {
			break
		}
		end -= size
	}
	if end == 0 {
		return s + "."
	}
	switch r, size := utf8.DecodeLastRuneInString(s[:end]); {
	case isTerminator(r):
		return s
	case r == ':' || r == ';' || r == ',':
		// Promote weak punctuation instead of stacking onto it.
		return s[:end-size] + "." + s[end:]
	default:
		return s[:end] + "." + s[end:]
	}
}

func isTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '…':
		return true
	}
	return false
}

func isCloser(r rune) bool {
	switch r {
	case '"', '\'', '’', '”', '»', ')', ']', '}', '*', '_':
		return true
	}
	return false
}

// normalizeProse is the last pass of every stripping mode: it trims each line,
// collapses runs of blank lines to a single one (paragraph boundaries are load
// bearing — they keep two paragraphs from being read as one sentence — but
// three of them are not more load bearing than one), and drops lines that no
// longer carry a word.
func normalizeProse(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	pendingBlank := false
	for _, line := range lines {
		line = collapseSpaces(line)
		if !hasProse(line) {
			// Includes genuinely blank lines and leftovers like "." or "|".
			pendingBlank = len(out) > 0
			continue
		}
		if pendingBlank {
			out = append(out, "")
			pendingBlank = false
		}
		out = append(out, line)
	}
	res := strings.Join(out, "\n")
	res = spaceBeforePunctRe.ReplaceAllString(res, "$1")
	return dupTerminatorRe.ReplaceAllString(res, "$1")
}
