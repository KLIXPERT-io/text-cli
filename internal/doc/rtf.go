package doc

import (
	"bytes"
	"strings"
	"unicode/utf16"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatRTF is the registry name of the RTF decoder.
const FormatRTF = "rtf"

func init() {
	Register(FormatRTF, func() (Decoder, error) { return &rtfDecoder{}, nil })
}

// rtfDecoder reads Rich Text Format.
//
// RTF is the odd one out in this package: not a zip, not XML, but a stream of
// 7-bit ASCII in which brace-delimited groups carry backslash control words. So
// this decoder shares nothing with container.go and is a small hand-written
// parser — and here the parser is the whole feature, because RTF keeps the
// prose and the machinery in the same stream. Font names, colour tables, style
// definitions and the hex of an embedded PNG are all "text" until something
// decides they are not. That something is destination skipping, and it is the
// only reason this decoder returns prose instead of
// "Times New Roman;Arial;010009000003".
type rtfDecoder struct{}

func (d *rtfDecoder) Name() string { return FormatRTF }

func (d *rtfDecoder) Extensions() []string { return []string{".rtf"} }

// rtfMagic is the only opening an RTF reader accepts. The version digit that
// follows is not part of the test: every writer emits \rtf1 and a future \rtf2
// would still be worth attempting.
var rtfMagic = []byte(`{\rtf`)

// Sniff accepts a leading BOM and leading whitespace before the magic.
//
// The specification allows neither, but a UTF-8 BOM is what a text editor adds
// when it saves an .rtf, and a leading newline is what a shell pipeline adds.
// Refusing over a byte no reader would print is a worse failure than tolerating
// it. Nothing else is tolerated: the magic is distinctive, and a false positive
// here would send a readable text file down a parser that returns nothing.
func (d *rtfDecoder) Sniff(data []byte) bool {
	return bytes.HasPrefix(rtfTrimLead(data), rtfMagic)
}

func rtfTrimLead(data []byte) []byte {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	return bytes.TrimLeft(data, " \t\r\n\v\f")
}

func (d *rtfDecoder) Decode(data []byte) (*Document, error) {
	if !d.Sniff(data) {
		return nil, errs.New(errs.CodeInvalidArgs, `not an RTF file: it does not start with {\rtf`).
			WithHint("Check the file; if it is already prose, pass --from text.")
	}

	p := &rtfParser{data: rtfTrimLead(data), cur: rtfState{dest: rtfDestBody, uc: 1}, outline: -1}
	p.run()
	// A file truncated before its last \par still holds a paragraph, and a table
	// truncated before its \row still holds cells. Both are worth keeping.
	p.endRow()
	p.endPara()

	content := p.md.String()
	if strings.TrimSpace(content) == "" {
		return nil, EmptyErr(FormatRTF, "")
	}
	return &Document{
		Content: content,
		Title:   cleanInline(p.title.String()),
		Format:  FormatRTF,
		// No page count: RTF stores \page break hints, not pagination. The page
		// a reader sees is produced by whatever laid the document out, so any
		// number here would be invented.
	}, nil
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// rtfDest is what the text in the current group is for.
//
// It is a destination in the RTF sense: a control word can redirect the rest of
// its group away from the document body, and everything nested inside inherits
// that redirection. Modelling it as one inherited value per group is what makes
// skipping total — a \pict cannot leak its hex through a nested group, because
// the nested group starts out skipped too.
type rtfDest int

const (
	rtfDestBody  rtfDest = iota // the document's prose: emitted
	rtfDestSkip                 // a destination we do not read: dropped entirely
	rtfDestInfo                 // {\info}: dropped, but watched for \title
	rtfDestTitle                // {\info{\title …}}: captured as Document.Title
)

// rtfSkipDest are the destinations that never hold body prose.
//
// Three groups of them, and each one is a bug if it is missing. The tables
// (fonttbl, colortbl, stylesheet, listtable, revtbl…) hold names and
// definitions that read as words: without this the output starts with a list of
// font names. The embedded objects (pict, object, and the wrappers Word puts
// around them) hold megabytes of hex that tokenizes as one enormous word. The
// non-body text (headers, footers, footnotes, comments, index and TOC entries)
// is real prose, but it is not the prose being measured, and a running header
// repeated once per page would dominate any per-document average.
//
// A destination this list does not name is still skipped when it is marked
// \* — that is what \* means, and it covers the open-ended half of the format.
var rtfSkipDest = map[string]bool{
	// Tables and definitions.
	"fonttbl": true, "filetbl": true, "colortbl": true, "stylesheet": true,
	"listtable": true, "listoverridetable": true, "revtbl": true,
	"rsidtbl": true, "generator": true,
	// Embedded objects and pictures.
	"pict": true, "object": true, "objdata": true, "shppict": true,
	"nonshppict": true,
	// Text that is not the body.
	"header": true, "headerl": true, "headerr": true, "headerf": true,
	"footer": true, "footerl": true, "footerr": true, "footerf": true,
	"footnote": true, "annotation": true, "atnauthor": true, "atnid": true,
	"atndate": true, "atnref": true, "atnparent": true,
	"xe": true, "tc": true, "tcn": true,
}

type rtfState struct {
	dest rtfDest
	// uc is how many characters follow a \u as its non-Unicode substitute. It
	// is group-scoped by the specification, which is why it lives here and is
	// restored by a closing brace rather than tracked globally.
	uc int
}

type rtfParser struct {
	data []byte
	pos  int

	cur   rtfState
	stack []rtfState

	md    mdBuilder
	para  strings.Builder
	title strings.Builder

	// cells buffers one table row between \cell and \row.
	cells []string
	// outline is the current paragraph's \outlinelevel, or -1 for body text. It
	// survives \par and is reset by \pard, because that is how RTF scopes
	// paragraph properties: consecutive headings need not repeat them.
	outline int
	// high is a stashed UTF-16 high surrogate waiting for its partner. RTF has
	// no way to write a code point above the BMP except as two \u values.
	high rune
}

func (p *rtfParser) run() {
	for p.pos < len(p.data) {
		switch c := p.data[p.pos]; c {
		case '{':
			p.pos++
			p.stack = append(p.stack, p.cur)
		case '}':
			p.pos++
			if n := len(p.stack); n > 0 {
				p.cur = p.stack[n-1]
				p.stack = p.stack[:n-1]
				continue
			}
			// A closing brace with nothing open is a damaged or truncated file.
			// Returning to the top-level state is the only reading that keeps
			// the rest of the document: staying in whatever destination we were
			// in would silently drop everything that follows.
			p.cur = rtfState{dest: rtfDestBody, uc: 1}
		case '\r', '\n':
			// Line breaks in the stream lay out the RTF source, not the prose.
			p.pos++
		case '\\':
			p.control()
		default:
			start := p.pos
			for p.pos < len(p.data) && !rtfSpecial(p.data[p.pos]) {
				p.pos++
			}
			p.write(p.data[start:p.pos])
		}
	}
}

func rtfSpecial(c byte) bool {
	return c == '{' || c == '}' || c == '\\' || c == '\r' || c == '\n'
}

// control consumes one control word or control symbol and acts on it.
func (p *rtfParser) control() {
	p.pos++ // the backslash
	if p.pos >= len(p.data) {
		return
	}
	if c := p.data[p.pos]; !rtfAlpha(c) {
		p.pos++
		p.symbol(c)
		return
	}

	start := p.pos
	for p.pos < len(p.data) && rtfAlpha(p.data[p.pos]) {
		p.pos++
	}
	word := string(p.data[start:p.pos])

	neg := false
	if p.pos < len(p.data) && p.data[p.pos] == '-' {
		neg = true
		p.pos++
	}
	param, hasParam := 0, false
	for p.pos < len(p.data) && rtfDigit(p.data[p.pos]) {
		hasParam = true
		if param < 1<<20 { // a parameter this large is already nonsense; stop growing
			param = param*10 + int(p.data[p.pos]-'0')
		}
		p.pos++
	}
	if neg {
		if hasParam {
			param = -param
		} else {
			p.pos-- // not a sign after all; leave the byte to be read as text
		}
	}
	// Exactly one space may follow, and it belongs to the control word rather
	// than to the prose. Eating a second one, or eating a tab, would delete a
	// space the document really contains.
	if p.pos < len(p.data) && p.data[p.pos] == ' ' {
		p.pos++
	}

	p.word(word, param, hasParam)
}

// symbol handles a control symbol: a backslash followed by one non-letter.
func (p *rtfParser) symbol(c byte) {
	switch c {
	case '\\', '{', '}':
		// The three escapes. Note that unlike a control word, a control symbol
		// does not absorb a following space.
		p.text(string(rune(c)))
	case '\'':
		p.hexChar()
	case '*':
		// The marker for "a destination the reader may not understand". We
		// understand none of them, so this is the rule that keeps field
		// instructions, bookmarks, themes and every vendor extension out of the
		// prose without naming them one by one.
		p.cur.dest = rtfDestSkip
	case '~':
		p.text(" ") // non-breaking space
	case '_':
		p.text("-") // non-breaking hyphen
	case '-':
		// Optional hyphen: invisible unless the line happens to break there, so
		// keeping it would split one word into two for the tokenizer.
	case '\r', '\n':
		// A backslash before a line break is the legacy spelling of \par, and
		// some writers still emit it.
		p.endPara()
	}
	// Anything else (\:, \|, \#) marks a position rather than a character.
}

// hexChar decodes \'hh, one byte in the document's code page.
func (p *rtfParser) hexChar() {
	if p.pos+1 >= len(p.data) {
		return
	}
	hi, lo := rtfHex(p.data[p.pos]), rtfHex(p.data[p.pos+1])
	if hi < 0 || lo < 0 {
		return
	}
	p.pos += 2
	p.text(rtfCP1252(byte(hi<<4 | lo)))
}

func (p *rtfParser) word(word string, param int, hasParam bool) {
	// A destination control word redirects the rest of its group, wherever in
	// the group it appears.
	if rtfSkipDest[word] {
		p.cur.dest = rtfDestSkip
		return
	}

	switch word {
	case "info":
		// Document metadata: dropped from the prose, but \title inside it is
		// the document's own title, which is exactly what Document.Title is
		// for. Everything else in the block stays dropped.
		p.cur.dest = rtfDestInfo
	case "title":
		if p.cur.dest == rtfDestInfo {
			p.cur.dest = rtfDestTitle
		}
	case "uc":
		if hasParam && param >= 0 {
			p.cur.uc = param
		}
	case "u":
		p.unicode(param)
		p.skipSubstitute(p.cur.uc)
	case "bin":
		// \binN is followed by N raw bytes. Skipping them by count is not an
		// optimisation: picture data contains braces, and letting the group
		// tracker see them tears the document structure apart from here on.
		if hasParam && param > 0 {
			if p.pos+param > len(p.data) {
				p.pos = len(p.data)
			} else {
				p.pos += param
			}
		}
	case "outlinelevel":
		// The one heading signal worth trusting. It is an explicit statement by
		// the writer that this paragraph is level N of the outline, unlike an
		// \sN style reference, whose meaning lives in the stylesheet — the
		// destination we deliberately skip — and whose numbering is arbitrary
		// per document. Levels 0-8 are headings 1-9; 9 and above mean body text
		// in the writers that emit it at all.
		if param >= 0 && param <= 8 {
			p.outline = param
		} else {
			p.outline = -1
		}
	case "par", "line", "sect", "page", "column":
		// \line is a break inside a paragraph, but markdown as this package
		// emits it has no soft break: cleanInline folds a newline into a space.
		// Ending the block is the better of the two available readings — the
		// lines of an address or a poem stay separate sentences instead of
		// being glued into one.
		p.endPara()
	case "pard", "sectd":
		p.endPara()
		p.outline = -1
	case "cell", "nestcell":
		p.endCell()
	case "row", "nestrow":
		p.endRow()
	case "trowd":
		p.endPara()
		p.cells = nil
	case "tab":
		p.text(" ")
	case "emdash":
		p.text("—")
	case "endash":
		p.text("–")
	case "bullet":
		p.text("•")
	case "lquote":
		p.text("‘")
	case "rquote":
		p.text("’")
	case "ldblquote":
		p.text("“")
	case "rdblquote":
		p.text("”")
	}
	// Every other control word is formatting. Producing nothing is the correct
	// handling for the several hundred of them this CLI does not care about.
}

// unicode emits one \uN code point.
//
// The parameter is a signed 16-bit value, so a code point above U+7FFF arrives
// negative and has to be wrapped — the classic RTF reading bug, and the reason
// a document full of typographic dashes decodes into nothing when it is missed.
// Anything above the BMP arrives as a surrogate pair of such values.
func (p *rtfParser) unicode(param int) {
	if param < 0 {
		param += 1 << 16
	}
	if param < 0 || param > 0x10FFFF {
		return
	}
	r := rune(param)

	if p.high != 0 {
		high := p.high
		p.high = 0
		if r >= 0xDC00 && r <= 0xDFFF {
			p.text(string(utf16.DecodeRune(high, r)))
			return
		}
		p.text("�") // an orphaned half-pair; the reader should see something
	}
	switch {
	case r >= 0xD800 && r <= 0xDBFF:
		p.high = r
	case r >= 0xDC00 && r <= 0xDFFF:
		p.text("�")
	default:
		p.text(string(r))
	}
}

// skipSubstitute consumes the n characters that follow a \u for readers that
// cannot do Unicode — usually a single "?", but \ucN may declare more, and a
// writer may spell them as \'3f escapes.
//
// A brace is never consumed: the substitute count is a promise about characters
// in this group, and eating a group boundary to honour a wrong count would
// desynchronise the whole document.
func (p *rtfParser) skipSubstitute(n int) {
	for i := 0; i < n && p.pos < len(p.data); i++ {
		c := p.data[p.pos]
		if c == '{' || c == '}' {
			return
		}
		if c != '\\' {
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.data) {
			return
		}
		switch {
		case p.data[p.pos] == '\'':
			p.pos++
			for j := 0; j < 2 && p.pos < len(p.data) && rtfHex(p.data[p.pos]) >= 0; j++ {
				p.pos++
			}
		case rtfAlpha(p.data[p.pos]):
			for p.pos < len(p.data) && rtfAlpha(p.data[p.pos]) {
				p.pos++
			}
			if p.pos < len(p.data) && p.data[p.pos] == '-' {
				p.pos++
			}
			for p.pos < len(p.data) && rtfDigit(p.data[p.pos]) {
				p.pos++
			}
			if p.pos < len(p.data) && p.data[p.pos] == ' ' {
				p.pos++
			}
		default:
			p.pos++
		}
	}
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// sink is where text goes right now, or nil when the current destination is one
// this decoder drops.
func (p *rtfParser) sink() *strings.Builder {
	switch p.cur.dest {
	case rtfDestBody:
		return &p.para
	case rtfDestTitle:
		return &p.title
	}
	return nil
}

func (p *rtfParser) text(s string) {
	if b := p.sink(); b != nil {
		b.WriteString(s)
	}
}

// write appends raw stream bytes unchanged.
//
// They are not decoded through the code page on purpose. The specification says
// this part of the stream is 7-bit ASCII, so the only bytes above 0x7F that
// ever appear here were put there by a writer that meant UTF-8 — and passing
// those through keeps them intact, while running them through cp1252 would
// mojibake every one of them.
func (p *rtfParser) write(raw []byte) {
	if b := p.sink(); b != nil {
		b.Write(raw)
	}
}

func (p *rtfParser) endPara() {
	if p.cur.dest != rtfDestBody {
		return
	}
	text := p.para.String()
	p.para.Reset()
	if strings.TrimSpace(text) == "" {
		return
	}
	if p.outline >= 0 {
		p.md.Heading(p.outline+1, text)
		return
	}
	if item, ok := rtfBulletItem(text); ok {
		p.md.Item(0, false, item)
		return
	}
	p.md.Para(text)
}

func (p *rtfParser) endCell() {
	if p.cur.dest != rtfDestBody {
		return
	}
	p.cells = append(p.cells, p.para.String())
	p.para.Reset()
}

func (p *rtfParser) endRow() {
	if len(p.cells) == 0 {
		return
	}
	p.md.Row(p.cells)
	p.cells = nil
}

// rtfBulletBullets are the characters a bullet list actually starts with.
//
// RTF has no list markup a reader must honour: the marker is literal text, put
// there by \listtext or \pntext as "{\listtext\f3\'b7\tab}". \'b7 is a middle
// dot in cp1252 and a bullet in the Symbol font the writer selected, and since
// this decoder does not read font tables it sees the middle dot — so both
// characters are on the list, along with the shapes Word uses for nested
// levels. Recovering the list is worth the small table: a paragraph starting
// with a stray "·" reads as a sentence fragment to every downstream metric,
// while "- item" is a list item the strip pass already knows how to terminate.
var rtfBulletBullets = []string{"•", "·", "●", "▪", "◦", "‣", "⁃"}

// rtfBulletItem reports whether a paragraph is a bullet item, and returns it
// without its marker.
func rtfBulletItem(text string) (string, bool) {
	t := strings.TrimLeft(text, " \t")
	for _, b := range rtfBulletBullets {
		if strings.HasPrefix(t, b) {
			rest := strings.TrimLeft(strings.TrimPrefix(t, b), " \t")
			if rest == "" {
				return "", false // a lone marker is not an item
			}
			return rest, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Characters
// ---------------------------------------------------------------------------

func rtfAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func rtfDigit(c byte) bool { return c >= '0' && c <= '9' }

func rtfHex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// rtfCP1252High is the part of Windows-1252 that is not Latin-1: 0x80-0x9F,
// where Latin-1 has control characters and Windows put the typographic
// punctuation. Zero means the byte is unassigned in cp1252.
var rtfCP1252High = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡',
	'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—',
	'˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}

// rtfCP1252 decodes one \'hh byte.
//
// Windows-1252 is the code page of \ansicpg1252, which is what every Windows
// writer emits and the reason German umlauts and typographic quotes come out
// right. A document declaring another \ansicpg is decoded with this table
// anyway: above 0xA0 cp1252 is Latin-1, so a Central European or Cyrillic
// document gets its ASCII and its punctuation right and its accents wrong,
// which is a far better outcome than refusing the file — and shipping a table
// per legacy code page is weight this CLI would carry forever for documents it
// will almost never see.
func rtfCP1252(b byte) string {
	if b < 0x80 {
		return string(rune(b))
	}
	if b < 0xA0 {
		if r := rtfCP1252High[b-0x80]; r != 0 {
			return string(r)
		}
		return "" // unassigned: printing U+FFFD would add a word to the prose
	}
	return string(rune(b))
}
