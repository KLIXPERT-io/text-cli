package doc

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatDocx is the registry name of the Word decoder.
const FormatDocx = "docx"

// The two parts this decoder reads. Everything else in a .docx — styles,
// numbering, relationships, media, headers and footers — is layout or
// provenance, and none of it is body prose.
const (
	docxBodyPart = "word/document.xml"
	docxCorePart = "docProps/core.xml"
)

func init() {
	Register(FormatDocx, func() (Decoder, error) { return &docxDecoder{}, nil })
}

// docxDecoder reads WordprocessingML: .docx and its macro-enabled twin .docm,
// which differ only in the content type of parts this decoder never opens.
//
// It reads the body and nothing else. Footnotes, endnotes and comments live in
// their own parts (word/footnotes.xml, word/comments.xml) and are deliberately
// left there: a footnote is not a sentence of the document, and splicing one
// into the flow would put a citation in the middle of a paragraph and move
// every per-sentence average that depends on it. `text docs comments` is the
// command for reading commentary; this is the command for reading the text.
type docxDecoder struct{}

func (d *docxDecoder) Name() string { return FormatDocx }

func (d *docxDecoder) Extensions() []string { return []string{".docx", ".docm"} }

// Sniff opens the container and looks for the body part. The zip magic alone
// is worthless here — DOCX, PPTX, ODT and EPUB all start with "PK\x03\x04" —
// so the entry name is the only honest test, and it is read from the central
// directory without inflating anything.
func (d *docxDecoder) Sniff(data []byte) bool {
	if !isZip(data) {
		return false
	}
	r, err := openZip(FormatDocx, data)
	if err != nil {
		return false
	}
	return zipHas(r, docxBodyPart)
}

func (d *docxDecoder) Decode(data []byte) (*Document, error) {
	r, err := openZip(FormatDocx, data)
	if err != nil {
		return nil, err
	}
	body, ok, err := zipRead(r, docxBodyPart, zipNewBudget())
	if err != nil {
		return nil, err
	}
	if !ok {
		// A zip that is not a Word file: an ODT, an EPUB, or a plain archive.
		// The extension pointed here, so name what was actually missing.
		return nil, errs.Newf(errs.CodeInvalidArgs, "not a Word file: the zip container has no %s", docxBodyPart).
			WithHint("Use --from to name the right format, or --from auto to detect it: " + strings.Join(FormatNames(), " "))
	}

	md := &mdBuilder{}
	if err := docxWalkBody(newXMLDecoder(body), md); err != nil {
		return nil, err
	}
	if md.Empty() {
		return nil, EmptyErr(FormatDocx, "")
	}
	return &Document{Content: md.String(), Title: docxTitle(r)}, nil
}

// ---------------------------------------------------------------------------
// Body
// ---------------------------------------------------------------------------

// docxWalkBody streams word/document.xml and emits one markdown block per
// WordprocessingML block.
//
// Unrecognised elements are descended into rather than skipped, because Word
// wraps content in containers this decoder has no reason to know about —
// w:sdt/w:sdtContent for a content control, w:customXml for a bound field —
// and a paragraph inside one is still a paragraph. Only w:p and w:tbl produce
// output, so descending costs a few tokens and buys immunity to the wrappers.
func docxWalkBody(dec *xml.Decoder, md *mdBuilder) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return docxXMLErr(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch localName(se.Name) {
		case "p":
			p, err := docxReadPara(dec)
			if err != nil {
				return docxXMLErr(err)
			}
			p.emit(md)
		case "tbl":
			if err := docxReadTable(dec, md); err != nil {
				return docxXMLErr(err)
			}
		}
	}
}

// docxParagraph is one w:p, reduced to what decides its markdown block.
type docxParagraph struct {
	text  strings.Builder
	style string // w:pStyle w:val, the style id, not the display name
	list  bool   // has w:numPr
	depth int    // w:ilvl
}

// emit turns the paragraph into exactly one block.
//
// A numbered heading is a heading: Word represents "1.2 Scope" as a Heading2
// paragraph carrying a w:numPr, and demoting it to a list item would lose the
// document's only structural signal for the sake of its numbering.
func (p *docxParagraph) emit(md *mdBuilder) {
	text := p.text.String()
	if strings.TrimSpace(text) == "" {
		return
	}
	if level := docxHeadingLevel(p.style); level > 0 {
		md.Heading(level, text)
		return
	}
	if p.list {
		// Unordered is the defensible default: ordered-ness is not in the
		// paragraph, it is in numbering.xml — w:numId resolves to an
		// abstractNum whose w:lvl carries w:numFmt — and the mapping is
		// several indirections and an override table away. Guessing "1." for a
		// bulleted list would print numbers that are not in the document,
		// while the marker itself is stripped downstream either way. The depth
		// is the part that carries meaning, and that one w:ilvl states outright.
		md.Item(p.depth, false, text)
		return
	}
	md.Para(text)
}

// docxReadPara consumes one w:p, the opening tag already read.
func docxReadPara(dec *xml.Decoder) (*docxParagraph, error) {
	p := &docxParagraph{}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return p, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t.Name) {
			case "pPr":
				if err := docxReadParaProps(dec, p); err != nil {
					return p, err
				}
			case "t":
				// Never trimmed, whatever xml:space says. Word writes
				// xml:space="preserve" exactly when a run's leading or trailing
				// space is significant, and writers that omit it on significant
				// space are common enough that trimming would glue words
				// together across run boundaries. Keeping the space costs
				// nothing: cleanInline collapses whitespace runs anyway.
				s, err := docxCharData(dec)
				if err != nil {
					return p, err
				}
				p.text.WriteString(s)
			case "tab":
				p.text.WriteByte(' ')
				if err := dec.Skip(); err != nil {
					return p, err
				}
			case "br", "cr":
				// A line break inside a paragraph, not a paragraph break: it
				// stays inside this block and cleanInline folds it to a space,
				// so a soft-wrapped address does not become five sentences.
				p.text.WriteByte('\n')
				if err := dec.Skip(); err != nil {
					return p, err
				}
			case "del", "delText", "instrText":
				// Deleted text is what the author removed, and a field code
				// (PAGE, TOC, HYPERLINK "…") is instruction, not prose. Both
				// would be counted as words by every metric downstream.
				// w:ins is absent from this list on purpose: an accepted-but-
				// unmerged insertion is the document.
				if err := dec.Skip(); err != nil {
					return p, err
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return p, nil
}

// docxReadParaProps consumes one w:pPr, reading only the two properties that
// change the block's shape.
//
// It is a separate walk rather than a case in the paragraph loop because w:pPr
// contains elements whose local names collide with run content: w:tabs/w:tab
// declares tab stops and would otherwise inject a space per stop, and
// w:rPr/w:del marks the paragraph mark as deleted without deleting the text.
func docxReadParaProps(dec *xml.Decoder, p *docxParagraph) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t.Name) {
			case "pStyle":
				// First one wins: a later pStyle can only come from a
				// w:pPrChange, which is the formatting this paragraph had
				// before the tracked change, not the one it has now.
				if p.style == "" {
					p.style = docxAttr(t, "val")
				}
				if err := dec.Skip(); err != nil {
					return err
				}
			case "numPr":
				p.list = true
				depth++ // descend: w:ilvl is inside
			case "ilvl":
				p.depth = docxNum(docxAttr(t, "val"))
				if err := dec.Skip(); err != nil {
					return err
				}
			default:
				if err := dec.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Tables
// ---------------------------------------------------------------------------

// docxReadTable consumes one w:tbl and emits one row per w:tr.
func docxReadTable(dec *xml.Decoder, md *mdBuilder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "tr" {
				cells, err := docxReadRow(dec)
				if err != nil {
					return err
				}
				md.Row(cells)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return nil
}

// docxReadRow consumes one w:tr and returns its cells' text.
func docxReadRow(dec *xml.Decoder) ([]string, error) {
	var cells []string
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return cells, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "tc" {
				cell, err := docxReadCell(dec)
				if err != nil {
					return cells, err
				}
				cells = append(cells, cell)
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return cells, nil
}

// docxReadCell consumes one w:tc and joins its paragraphs into one cell.
//
// A cell is a whole block container — it can hold several paragraphs, a list,
// or another table — and a markdown row has one line per row. Everything in the
// cell is therefore flattened into its cell, including a nested table's
// paragraphs, which is the one place this decoder loses structure. The
// alternative, breaking the outer row apart to emit the inner table's rows,
// would scramble the reading order of both.
func docxReadCell(dec *xml.Decoder) (string, error) {
	var parts []string
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return strings.Join(parts, " "), err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "p" {
				p, err := docxReadPara(dec)
				if err != nil {
					return strings.Join(parts, " "), err
				}
				if s := strings.TrimSpace(p.text.String()); s != "" {
					parts = append(parts, s)
				}
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return strings.Join(parts, " "), nil
}

// ---------------------------------------------------------------------------
// Styles and metadata
// ---------------------------------------------------------------------------

// docxHeadingPrefixes are the style ids Word gives its built-in heading styles,
// per UI language, minus the trailing level digit.
//
// This is the compromise in this decoder. A style id is not a stable
// identifier: Word writes it in the language of the build that created the
// document — Heading1, berschrift1, Titre1 — and strips the characters that
// are not ASCII letters while doing so, which is why the German id has lost its
// "Ü" and the Spanish one its accent. Resolving the id through word/styles.xml
// to the w:name it is based on would be exact, but a document that redefines a
// style still names it in its own language, so the lookup ends at the same
// guess one part later.
//
// Matching instead on "any style whose id ends in a digit" was the other
// option and is worse: ListParagraph2, BodyText3 and TableGrid1 are all real
// style ids that are not headings, and promoting them would fabricate document
// structure. A curated prefix list under-detects on a language nobody added
// yet — the paragraph stays a paragraph, which is the safe failure — and this
// list is one line longer whenever that happens.
var docxHeadingPrefixes = []string{
	"heading",     // en
	"überschrift", // de, as written
	"berschrift",  // de, as Word writes the id
	"uberschrift", // de, transliterated by some writers
	"titre",       // fr
	"titolo",      // it
	"título",      // es/pt, as written
	"ttulo",       // es/pt, as Word writes the id
	"titulo",      // es/pt, transliterated
	"encabezado",  // es, some builds
	"kop",         // nl
	"rubrik",      // sv
	"overskrift",  // da/nb
	"otsikko",     // fi
	"nagłówek",    // pl, as written
	"nagwek",      // pl, as Word writes the id
}

// docxTitleStyles are the ids of the document Title style, which carries no
// level digit. It maps to level 1: it is the document's own name and outranks
// every heading under it. Several of these are also heading prefixes in their
// language — Italian "Titolo" is the title and "Titolo1" is Heading 1 — which
// is exactly why the digit decides first.
var docxTitleStyles = []string{
	"title", "titel", "titre", "titolo", "título", "ttulo", "titulo", "otsikko",
}

// docxHeadingLevel maps a w:pStyle w:val to a markdown heading level, or 0 for
// a style that is not a heading.
func docxHeadingLevel(style string) int {
	s := docxNormStyle(style)
	if s == "" {
		return 0
	}
	base, digits := docxSplitTrailingDigits(s)
	if digits == "" {
		for _, t := range docxTitleStyles {
			if s == t {
				return 1
			}
		}
		return 0
	}
	level, err := strconv.Atoi(digits)
	if err != nil || level < 1 {
		return 0
	}
	for _, p := range docxHeadingPrefixes {
		if base == p {
			// Word offers nine levels and markdown six; mdBuilder clamps. A
			// Heading 7 reads as the deepest heading rather than as a
			// paragraph, which is what it is.
			return level
		}
	}
	return 0
}

// docxNormStyle lower-cases a style id and drops the separators writers vary
// on: "Heading 1", "heading-1" and "Heading1" are one style.
func docxNormStyle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case ' ', '-', '_', '.':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func docxSplitTrailingDigits(s string) (base, digits string) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	return s[:i], s[i:]
}

// docxTitle reads dc:title out of docProps/core.xml.
//
// The part is optional and routinely absent from documents produced by
// converters, and an author who never opened File > Properties leaves it
// empty. Neither is a failure: a missing title means the caller prints none,
// not that the document is unreadable. The first "title" element wins —
// cp:coreProperties has exactly one, and dc:title is the only element in that
// part with the name.
func docxTitle(r *zip.Reader) string {
	data, ok, err := zipEntry(r, docxCorePart)
	if err != nil || !ok {
		return ""
	}
	dec := newXMLDecoder(data)
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		se, ok := tok.(xml.StartElement)
		if !ok || localName(se.Name) != "title" {
			continue
		}
		s, err := docxCharData(dec)
		if err != nil {
			return ""
		}
		return cleanInline(s)
	}
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// docxCharData consumes an element and returns its character data, including
// the data of any child element, so that a w:t split by a proofing mark still
// reads as one string.
func docxCharData(dec *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return b.String(), err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			b.Write(t)
		}
	}
	return b.String(), nil
}

// docxAttr returns an attribute by local name, ignoring its namespace prefix.
func docxAttr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if localName(a.Name) == name {
			return a.Value
		}
	}
	return ""
}

// docxNum parses a non-negative attribute value, defaulting to 0. An
// unparsable w:ilvl means "top level", never an error: a bad indent depth is
// not worth refusing a document over.
func docxNum(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// docxXMLErr reports a malformed body part. It is invalid args rather than a
// decode failure because the cause is a broken or truncated file, and the fix
// is on the user's side.
func docxXMLErr(err error) *errs.E {
	return errs.Newf(errs.CodeInvalidArgs, "%s is not readable XML: %s", docxBodyPart, err.Error()).
		WithHint("The file may be corrupt. Open it in Word and save a fresh copy.")
}
