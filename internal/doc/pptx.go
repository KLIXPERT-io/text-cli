package doc

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatPPTX is the registry name of the PowerPoint decoder.
const FormatPPTX = "pptx"

// pptxSlidePrefix is the path every slide part shares. Layouts and masters live
// under ppt/slideLayouts/ and ppt/slideMasters/, so matching this prefix picks
// out the authored slides and nothing that was inherited from a template.
const pptxSlidePrefix = "ppt/slides/slide"

func init() {
	Register(FormatPPTX, func() (Decoder, error) { return &pptxDecoder{}, nil })
}

// pptxDecoder reads the prose out of a PowerPoint deck.
//
// A deck is the format where the difference between markdown and flattened text
// is largest: the words on a slide are almost all short bullet fragments, and a
// decoder that joins them hands the tokenizer one sentence of two hundred words
// and reports a reading level nobody's deck has. Each bullet is emitted as its
// own list item, each slide title as its own heading, and the strip pass turns
// both into terminated sentences.
type pptxDecoder struct{}

func (d *pptxDecoder) Name() string { return FormatPPTX }

// Extensions covers the macro-enabled variant too: .pptm is the same OOXML
// package with a VBA part this decoder never looks at.
func (d *pptxDecoder) Extensions() []string { return []string{".pptx", ".pptm"} }

// Sniff looks inside, because the PK magic is shared by every OOXML, ODF and
// EPUB file. ppt/presentation.xml is the part that only a presentation has.
func (d *pptxDecoder) Sniff(data []byte) bool {
	if !isZip(data) {
		return false
	}
	r, err := openZip(FormatPPTX, data)
	if err != nil {
		return false
	}
	return zipHas(r, "ppt/presentation.xml")
}

func (d *pptxDecoder) Decode(data []byte) (*Document, error) {
	r, err := openZip(FormatPPTX, data)
	if err != nil {
		return nil, err
	}
	if !zipHas(r, "ppt/presentation.xml") {
		return nil, errs.New(errs.CodeInvalidArgs, "not a PowerPoint file: the zip has no ppt/presentation.xml").
			WithHint("DOCX, ODT and EPUB are zip containers too — use --from auto to detect the real format.")
	}

	// Speaker notes (ppt/notesSlides/) are deliberately not read. They are the
	// presenter's script, not the deck: scoring them mixes two documents with
	// different audiences into one average, and an author asking whether their
	// slides are readable is not asking about the crib sheet under them.
	names := pptxSlideOrder(zipNames(r, pptxSlidePrefix, ".xml"))

	// One budget for the whole deck: a per-slide bound would let a thousand
	// slides inflate a thousand times the limit between them.
	budget := zipNewBudget()

	var b mdBuilder
	for _, name := range names {
		raw, ok, err := zipRead(r, name, budget)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		blocks, err := pptxSlideBlocks(raw)
		if err != nil {
			return nil, err
		}
		pptxRender(&b, blocks)
	}
	if b.Empty() {
		return nil, EmptyErr(FormatPPTX, "")
	}

	return &Document{
		Content: b.String(),
		Title:   pptxCoreTitle(r),
		// A slide is exactly one paginated unit — unlike a DOCX, whose pages do
		// not exist until a printer driver lays it out — so this is the one
		// OOXML format where a page count is a fact and not a guess. Every slide
		// part counts, including the image-only ones that contributed no text:
		// the number is the deck's length, not the text's.
		Pages: len(names),
	}, nil
}

// pptxSlideOrder sorts slide parts by the number in their file name.
//
// Lexical order is wrong on any deck of ten slides or more — "slide10.xml"
// sorts before "slide2.xml" — and that is the common case, not the edge case.
// The fully correct order lives in p:sldIdLst in ppt/presentation.xml, whose
// r:id values resolve through ppt/_rels/presentation.xml.rels to these paths;
// that is two more XML parses and a relationship model this decoder needs for
// nothing else. The trade is deliberate: the manifest differs from the part
// numbers only when slides were reordered after they were created, and when it
// does, the cost is the reading order of the markdown — every word, every
// sentence and therefore every score is identical either way.
func pptxSlideOrder(names []string) []string {
	sort.SliceStable(names, func(i, j int) bool {
		ni, oki := pptxSlideNum(names[i])
		nj, okj := pptxSlideNum(names[j])
		if oki != okj {
			// A part that does not follow the naming scheme keeps its place at
			// the back in archive order rather than being interleaved on a
			// number it does not have.
			return oki
		}
		if !oki {
			return false
		}
		return ni < nj
	})
	return names
}

func pptxSlideNum(name string) (int, bool) {
	s := strings.TrimSuffix(strings.TrimPrefix(name, pptxSlidePrefix), ".xml")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// pptxPara is one a:p: its text and the outline depth from a:pPr/@lvl.
type pptxPara struct {
	level int
	text  string
}

// pptxBlock is one shape's text body or one table. A block is never both.
type pptxBlock struct {
	title bool
	paras []pptxPara
	rows  [][]string
}

// pptxSlideBlocks walks one slide's DrawingML into blocks in document order.
//
// The walk dispatches on a:txBody rather than on p:sp so that text in a
// connector, a grouped shape or a picture's caption is picked up the same way;
// the shape types differ but every one of them puts its prose in a text body.
func pptxSlideBlocks(data []byte) ([]pptxBlock, error) {
	dec := newXMLDecoder(data)
	var blocks []pptxBlock
	title := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, pptxMalformed(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch name := localName(se.Name); {
		case name == "txBody":
			paras, err := pptxTextBody(dec)
			if err != nil {
				return nil, pptxMalformed(err)
			}
			if len(paras) > 0 {
				blocks = append(blocks, pptxBlock{title: title, paras: paras})
			}
			title = false
		case name == "tbl":
			rows, err := pptxTable(dec)
			if err != nil {
				return nil, pptxMalformed(err)
			}
			if len(rows) > 0 {
				blocks = append(blocks, pptxBlock{rows: rows})
			}
		case name == "ph":
			// A placeholder declaration precedes the body it describes, so the
			// flag is carried forward to the next text body.
			title = pptxIsTitlePlaceholder(se)
		case strings.HasPrefix(name, "nv") && strings.HasSuffix(name, "Pr"):
			// Every shape opens with its non-visual properties (p:nvSpPr,
			// p:nvPicPr, …), which is where the flag is cleared: an empty title
			// placeholder must not label the next shape's text as the title.
			title = false
		}
	}
	return blocks, nil
}

// pptxIsTitlePlaceholder reports whether a p:ph declares the slide title.
// "ctrTitle" is the centred title of a section or cover layout; both carry the
// same meaning to a reader.
func pptxIsTitlePlaceholder(se xml.StartElement) bool {
	for _, a := range se.Attr {
		if localName(a.Name) != "type" {
			continue
		}
		switch a.Value {
		case "title", "ctrTitle":
			return true
		}
	}
	return false
}

// pptxTextBody collects the paragraphs of one a:txBody, consuming its end tag.
func pptxTextBody(dec *xml.Decoder) ([]pptxPara, error) {
	var out []pptxPara
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "p" {
				p, err := pptxParagraph(dec)
				if err != nil {
					return nil, err
				}
				if p.text != "" {
					out = append(out, p)
				}
				continue
			}
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return out, nil
			}
		}
	}
}

// pptxParagraph reads one a:p, consuming its end tag.
func pptxParagraph(dec *xml.Decoder) (pptxPara, error) {
	var p pptxPara
	var sb strings.Builder
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return p, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t.Name) {
			case "pPr":
				p.level = pptxLevel(t)
				depth++
			case "t":
				s, err := pptxRunText(dec)
				if err != nil {
					return p, err
				}
				sb.WriteString(s)
			case "br":
				// a:br is a manual line break used to control where a bullet
				// wraps, not a new bullet. A space keeps the words on either
				// side from being glued into one token while leaving the
				// paragraph as the single block the author wrote.
				sb.WriteByte(' ')
				depth++
			case "fld":
				// A field is generated chrome — the slide number, the date, the
				// footer — rendered into a run at open time. Counting "7" as a
				// word of the deck skews a short slide measurably.
				if err := dec.Skip(); err != nil {
					return p, err
				}
			default:
				depth++
			}
		case xml.EndElement:
			if depth--; depth == 0 {
				p.text = sb.String()
				return p, nil
			}
		}
	}
}

// pptxLevel reads a:pPr/@lvl, the zero-based outline depth of a bullet.
func pptxLevel(se xml.StartElement) int {
	for _, a := range se.Attr {
		if localName(a.Name) != "lvl" {
			continue
		}
		if n, err := strconv.Atoi(a.Value); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// pptxRunText collects the character data of one a:t, consuming its end tag.
func pptxRunText(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return sb.String(), nil
			}
		}
	}
}

// pptxTable reads an a:tbl into rows of cell text, consuming its end tag.
func pptxTable(dec *xml.Decoder) ([][]string, error) {
	var rows [][]string
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "tr" {
				row, err := pptxRow(dec)
				if err != nil {
					return nil, err
				}
				if len(row) > 0 {
					rows = append(rows, row)
				}
				continue
			}
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return rows, nil
			}
		}
	}
}

func pptxRow(dec *xml.Decoder) ([]string, error) {
	var cells []string
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "tc" {
				cell, err := pptxCell(dec)
				if err != nil {
					return nil, err
				}
				// Empty cells are kept: dropping one would shift every later
				// cell in the row under the wrong column.
				cells = append(cells, cell)
				continue
			}
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return cells, nil
			}
		}
	}
}

// pptxCell flattens a cell's paragraphs into one string. A markdown row has no
// room for a block boundary, and a cell holding two paragraphs is a wrapped
// sentence far more often than it is two ideas.
func pptxCell(dec *xml.Decoder) (string, error) {
	var parts []string
	depth := 1
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if localName(t.Name) == "p" {
				p, err := pptxParagraph(dec)
				if err != nil {
					return "", err
				}
				if p.text != "" {
					parts = append(parts, p.text)
				}
				continue
			}
			depth++
		case xml.EndElement:
			if depth--; depth == 0 {
				return strings.Join(parts, " "), nil
			}
		}
	}
}

// pptxRender writes one slide's blocks as markdown.
//
// There is no "Slide 3" heading: the deck does not contain those words, and a
// decoder that adds them adds words to every count downstream. The slide's own
// title is the heading, and a slide without one simply starts with its items.
func pptxRender(b *mdBuilder, blocks []pptxBlock) {
	bi, pi := pptxTitleAt(blocks)
	if bi >= 0 {
		// Emitted before the rest regardless of where the shape sits in the
		// tree: shape order is z-order, and a title box drawn last would
		// otherwise land in the middle of its own slide.
		b.Heading(2, blocks[bi].paras[pi].text)
	}
	for i, blk := range blocks {
		if len(blk.rows) > 0 {
			for _, row := range blk.rows {
				b.Row(row)
			}
			continue
		}
		for j, p := range blk.paras {
			if i == bi && j == pi {
				continue
			}
			// Every body paragraph becomes a list item, not a paragraph.
			// Bulleted text is what a deck is, and whether a given paragraph
			// actually shows a bullet is decided by a:buNone/a:buChar inherited
			// from the layout and the master — neither of which this decoder
			// reads. Guessing "plain paragraph" from the slide alone would be
			// wrong more often than right, and the strip pass terminates an
			// item and a paragraph identically.
			b.Item(p.level, false, p.text)
		}
	}
}

// pptxTitleAt locates the paragraph that is the slide's title, or (-1, -1).
//
// A declared title placeholder wins. Failing that the first text on the slide
// is the title, which is what a deck built by dragging a text box onto a blank
// layout looks like — common enough that skipping the fallback would leave
// whole decks headingless.
func pptxTitleAt(blocks []pptxBlock) (int, int) {
	for i, blk := range blocks {
		if blk.title && len(blk.paras) > 0 {
			return i, 0
		}
	}
	for i, blk := range blocks {
		if len(blk.paras) > 0 {
			return i, 0
		}
	}
	return -1, -1
}

// pptxCoreTitle reads dc:title from the package's core properties.
//
// core.xml carries exactly one element whose local name is "title", so matching
// on the local name alone cannot pick up a neighbouring field. A missing or
// unreadable core.xml is not an error: the title is provenance, and a deck
// without one is still a deck.
func pptxCoreTitle(r *zip.Reader) string {
	raw, ok, err := zipEntry(r, "docProps/core.xml")
	if err != nil || !ok {
		return ""
	}
	dec := newXMLDecoder(raw)
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		se, ok := tok.(xml.StartElement)
		if !ok || localName(se.Name) != "title" {
			continue
		}
		var v string
		if err := dec.DecodeElement(&v, &se); err != nil {
			return ""
		}
		return cleanInline(v)
	}
}

// pptxMalformed reports XML this decoder could not walk. It is invalid_args
// rather than a decode failure because the practical cause is a truncated or
// hand-edited package, and the fix belongs to whoever produced the file.
func pptxMalformed(err error) *errs.E {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errs.New(errs.CodeInvalidArgs, "pptx slide XML ends mid-element").
			WithHint("The file looks truncated. Re-export or re-download the deck.")
	}
	return errs.Newf(errs.CodeInvalidArgs, "pptx slide XML is malformed: %s", err.Error()).
		WithHint("Open the deck in PowerPoint and save it again to rewrite the package.")
}
