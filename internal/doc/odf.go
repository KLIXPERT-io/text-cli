package doc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatODT and FormatODS are the registry names of the two OpenDocument
// decoders.
const (
	FormatODT = "odt"
	FormatODS = "ods"
)

// The media types ODF puts in the container's first entry. They are the whole
// identity of the file: a .odt and a .ods are the same zip with the same
// content.xml name, and only this string tells them apart.
const (
	mimeODT = "application/vnd.oasis.opendocument.text"
	mimeODS = "application/vnd.oasis.opendocument.spreadsheet"
)

func init() {
	Register(FormatODT, func() (Decoder, error) { return &odfDecoder{kind: FormatODT}, nil })
	Register(FormatODS, func() (Decoder, error) { return &odfDecoder{kind: FormatODS}, nil })
}

// odfDecoder reads OpenDocument text documents and spreadsheets.
//
// One type serves both because the container is identical — a zip holding
// content.xml and meta.xml — but they are two *registered* decoders because
// the mapping to markdown is not: a text document is headings, paragraphs and
// lists, and a spreadsheet is a grid that only becomes readable as one table
// per sheet. Folding them into a single "odf" decoder would mean --from odf
// silently guessing which of two very different renderings the user wanted.
type odfDecoder struct{ kind string }

func (d *odfDecoder) Name() string { return d.kind }

// Extensions claims the packaged form and the flat single-file form. The flat
// variants are the same XML vocabulary with the parts inlined, so supporting
// them costs one branch in parts() rather than a second walker.
func (d *odfDecoder) Extensions() []string {
	if d.kind == FormatODS {
		return []string{".ods", ".fods"}
	}
	return []string{".odt", ".fodt"}
}

func (d *odfDecoder) mime() string {
	if d.kind == FormatODS {
		return mimeODS
	}
	return mimeODT
}

// Sniff reads the container's mimetype entry, never the extension.
//
// ODF requires "mimetype" to be the archive's first entry and stored
// uncompressed precisely so that a reader can identify the file from its first
// few dozen bytes; archive/zip finds it by the central directory instead,
// which costs nothing here and also accepts the writers that wrongly deflate
// it. A file with no mimetype entry at all is declined rather than guessed at:
// odt and ods would both have to claim it, and the wrong one produces a
// confidently mis-shaped document instead of an error.
func (d *odfDecoder) Sniff(data []byte) bool {
	if isZip(data) {
		r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return false
		}
		return odfZipMime(r) == d.mime()
	}
	return odfFlatMime(data) == d.mime()
}

func (d *odfDecoder) Decode(data []byte) (*Document, error) {
	content, meta, err := d.parts(data)
	if err != nil {
		return nil, err
	}
	w := &odfWalk{d: newXMLDecoder(content), md: &mdBuilder{}, kind: d.kind}
	if err := w.body(); err != nil {
		return nil, err
	}
	if w.md.Empty() {
		return nil, EmptyErr(d.kind, "")
	}
	// Pages stays 0 on purpose. meta.xml may carry a meta:page-count, but it
	// is whatever the last application to save the file happened to lay out —
	// a different page size or font makes it wrong, and an ODF document has no
	// pages of its own until something renders it.
	return &Document{Content: w.md.String(), Title: odfTitle(meta)}, nil
}

// parts locates content.xml and meta.xml, in either the packaged or the flat
// form. For a flat document both are the same bytes: office:meta and
// office:body are siblings under one root, and each walker scopes itself to
// the element it wants.
func (d *odfDecoder) parts(data []byte) (content, meta []byte, err error) {
	if !isZip(data) {
		m := odfFlatMime(data)
		if m == "" {
			return nil, nil, errs.Newf(errs.CodeInvalidArgs,
				"not a %s file: it is neither a zip container nor flat OpenDocument XML", d.kind)
		}
		if m != d.mime() {
			return nil, nil, odfWrongMime(d.kind, m)
		}
		return data, data, nil
	}

	r, err := openZip(d.kind, data)
	if err != nil {
		return nil, nil, err
	}
	// An absent mimetype entry is tolerated here although Sniff refuses it:
	// reaching Decode means the user named the format, by --from or by the
	// file's extension, and their answer beats the missing declaration.
	if m := odfZipMime(r); m != "" && m != d.mime() {
		return nil, nil, odfWrongMime(d.kind, m)
	}
	// content.xml and meta.xml share one budget: they are the two parts this
	// decoder inflates, and the pair is what one document may cost.
	budget := zipNewBudget()
	content, ok, err := zipRead(r, "content.xml", budget)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, errs.Newf(errs.CodeInvalidArgs, "not a %s file: the zip has no content.xml", d.kind).
			WithHint("OpenDocument files always contain content.xml. Check the file is not a renamed archive.")
	}
	// meta.xml is optional and its absence is not an error: it holds only the
	// title, which is optional itself.
	meta, _, _ = zipRead(r, "meta.xml", budget)
	return content, meta, nil
}

// odfZipMime returns the declared media type, or "" if there is none.
func odfZipMime(r *zip.Reader) string {
	b, ok, err := zipEntry(r, "mimetype")
	if err != nil || !ok {
		return ""
	}
	if len(b) > 128 {
		b = b[:128]
	}
	return strings.TrimSpace(string(b))
}

// odfFlatMime returns the office:mimetype attribute of a flat document.
//
// The attribute appears once, on the root element, and no other ODF construct
// uses that attribute name — so a byte scan of the header is exact enough and
// avoids parsing a whole document just to answer Sniff. The window is generous
// because the root element declares twenty-odd namespaces before it.
func odfFlatMime(data []byte) string {
	head := data
	if len(head) > 8192 {
		head = head[:8192]
	}
	i := bytes.Index(head, []byte("office:mimetype="))
	if i < 0 {
		return ""
	}
	rest := head[i+len("office:mimetype="):]
	if len(rest) == 0 {
		return ""
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		return ""
	}
	rest = rest[1:]
	end := bytes.IndexByte(rest, quote)
	if end < 0 {
		return ""
	}
	return string(rest[:end])
}

// odfWrongMime names the format the file actually is, because the two ODF
// types are one keystroke apart and the user's next command is the fix.
func odfWrongMime(kind, got string) *errs.E {
	e := errs.Newf(errs.CodeInvalidArgs, "not a %s file: its OpenDocument type is %q", kind, got)
	switch got {
	case mimeODT:
		return e.WithHint("It is an OpenDocument text document. Use --from odt.")
	case mimeODS:
		return e.WithHint("It is an OpenDocument spreadsheet. Use --from ods.")
	}
	return e.WithHint("Only OpenDocument text (.odt) and spreadsheets (.ods) are read here.")
}

// odfTitle returns dc:title from meta.xml, if the author filled one in.
//
// The search is scoped to office:meta so that a flat document, where the
// metadata and the body share a root, cannot pick up a "title" element from
// somewhere in the prose.
func odfTitle(meta []byte) string {
	if len(meta) == 0 {
		return ""
	}
	d := newXMLDecoder(meta)
	inMeta := false
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch localName(t.Name) {
			case "meta":
				inMeta = true
			case "title":
				if !inMeta {
					continue
				}
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return ""
				}
				return cleanInline(s)
			}
		case xml.EndElement:
			if localName(t.Name) == "meta" {
				return ""
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The content.xml walk
// ---------------------------------------------------------------------------

// odfMaxDepth bounds recursion into the element tree. ODF nests legitimately —
// a list in a cell in a frame in a section — but never a hundred deep, and a
// hand-built file that does would take the stack down with it.
const odfMaxDepth = 64

// odfMaxCols bounds one rendered row. A spreadsheet's last cell routinely
// declares number-columns-repeated in the thousands to pad out the sheet; that
// padding is dropped rather than expanded, and this only guards the pathological
// case of a *non-empty* cell repeated that many times.
const odfMaxCols = 512

// odfMaxRepeat bounds number-rows-repeated for a row that has content. A run
// of identical rows is real data, a run of a million of them is a grid.
const odfMaxRepeat = 512

type odfWalk struct {
	d    *xml.Decoder
	md   *mdBuilder
	kind string
}

// body finds office:body and hands its subtree to the right walker.
//
// Scoping to the body is what keeps header, footer and page-style prose out of
// the score: in a flat document those live in office:master-styles, a sibling
// of the body, and they are boilerplate repeated on every page rather than
// text the author wrote once.
func (w *odfWalk) body() error {
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				// No body element. Decode reports the empty document; there is
				// nothing more specific to say about a well-formed file that
				// simply holds no content.
				return nil
			}
			return w.xmlErr(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || localName(se.Name) != "body" {
			continue
		}
		if w.kind == FormatODS {
			return w.sheets(0)
		}
		return w.blocks(0, 0)
	}
}

// blocks walks the block-level children of the current element and emits one
// markdown block per ODF block.
//
// It returns at the first EndElement it sees, which is always its own parent's:
// every branch below consumes the element it opened, so no child's end can
// reach this loop.
func (w *odfWalk) blocks(listDepth, depth int) error {
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return w.xmlErr(err)
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil
			}
			continue
		}
		switch localName(t.Name) {
		case "h":
			text, err := w.text(depth)
			if err != nil {
				return err
			}
			w.md.Heading(odfNumber(t, "outline-level", 1), text)
		case "p":
			text, err := w.text(depth)
			if err != nil {
				return err
			}
			// Inside a list a paragraph is the item's body, not a paragraph of
			// its own; ODF has no separate "item text" element.
			if listDepth > 0 {
				w.md.Item(listDepth-1, false, text)
			} else {
				w.md.Para(text)
			}
		case "list":
			// Ordered or bulleted is a property of the list *style*, which
			// lives in another part of the package and would have to be
			// resolved through a style-name chain to answer. It is not worth
			// it: the strip pass reads "- " and "1. " identically, so the only
			// thing at stake is a marker nobody downstream sees.
			if err := w.descend(func() error { return w.blocks(listDepth+1, depth+1) }, depth); err != nil {
				return err
			}
		case "list-item", "list-header":
			if err := w.descend(func() error { return w.blocks(listDepth, depth+1) }, depth); err != nil {
				return err
			}
		case "table":
			if err := w.descend(func() error { return w.rows(depth + 1) }, depth); err != nil {
				return err
			}
		case "note", "annotation", "tracked-changes":
			// Footnotes and comments are apparatus, not body prose, and
			// tracked changes hold text the author already deleted. Scoring
			// any of them mixes two documents into one average.
			if err := w.d.Skip(); err != nil {
				return w.xmlErr(err)
			}
		default:
			// Sections, frames, text boxes, tables of contents: transparent
			// containers whose children are blocks. Recursing is what makes a
			// paragraph inside a text box count as a paragraph.
			if err := w.descend(func() error { return w.blocks(listDepth, depth+1) }, depth); err != nil {
				return err
			}
		}
	}
}

// descend runs a nested walk, or skips the element once the depth guard trips.
func (w *odfWalk) descend(fn func() error, depth int) error {
	if depth >= odfMaxDepth {
		if err := w.d.Skip(); err != nil {
			return w.xmlErr(err)
		}
		return nil
	}
	return fn()
}

// text collects the inline text of the current element.
func (w *odfWalk) text(depth int) (string, error) {
	var b strings.Builder
	err := w.inline(&b, depth)
	return b.String(), err
}

// inline flattens one element's character content, following spans and links
// and stopping at the element's own end.
func (w *odfWalk) inline(b *strings.Builder, depth int) error {
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return w.xmlErr(err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.EndElement:
			return nil
		case xml.StartElement:
			switch localName(t.Name) {
			case "s", "tab", "line-break":
				// ODF encodes runs of whitespace as elements so that XML
				// collapsing cannot eat them. One space is enough of each:
				// cleanInline collapses runs anyway, so text:c is cosmetic.
				b.WriteByte(' ')
				if err := w.d.Skip(); err != nil {
					return w.xmlErr(err)
				}
			case "note", "annotation":
				if err := w.d.Skip(); err != nil {
					return w.xmlErr(err)
				}
			default:
				// A cell or a text box carries whole paragraphs inside what is
				// otherwise an inline walk; without a separator their text
				// would be glued into one word.
				if n := localName(t.Name); n == "p" || n == "h" {
					if b.Len() > 0 {
						b.WriteByte(' ')
					}
				}
				if err := w.descend(func() error { return w.inline(b, depth+1) }, depth); err != nil {
					return err
				}
			}
		}
	}
}

// rows renders a table:table inside a text document as markdown rows.
func (w *odfWalk) rows(depth int) error {
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return w.xmlErr(err)
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil
			}
			continue
		}
		switch localName(t.Name) {
		case "table-row":
			cells, err := w.cells(depth + 1)
			if err != nil {
				return err
			}
			// mdBuilder.Row drops an all-empty row for us.
			w.md.Row(cells)
		case "table-header-rows", "table-rows", "table-row-group":
			if err := w.descend(func() error { return w.rows(depth + 1) }, depth); err != nil {
				return err
			}
		default:
			// Column definitions and style references carry no text.
			if err := w.d.Skip(); err != nil {
				return w.xmlErr(err)
			}
		}
	}
}

// cells reads one table:table-row into its cell texts.
//
// table:number-columns-repeated is expanded because it is how ODF writes a run
// of identical cells; ignoring it shifts every column to its right. Trailing
// empty cells are the exception: a sheet pads its last row out to the grid
// width with one repeated empty cell, and expanding that would turn every row
// into hundreds of empty columns. They are held back and dropped at the end of
// the row, which is also what makes an empty column at the right edge
// disappear.
func (w *odfWalk) cells(depth int) ([]string, error) {
	var out []string
	pendingEmpty := 0
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, w.xmlErr(err)
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return out, nil
			}
			continue
		}
		switch localName(t.Name) {
		case "table-cell", "covered-table-cell":
			text, err := w.text(depth)
			if err != nil {
				return nil, err
			}
			repeat := odfNumber(t, "number-columns-repeated", 1)
			if repeat > odfMaxCols {
				repeat = odfMaxCols
			}
			if strings.TrimSpace(text) == "" {
				pendingEmpty += repeat
				continue
			}
			for ; pendingEmpty > 0 && len(out) < odfMaxCols; pendingEmpty-- {
				out = append(out, "")
			}
			pendingEmpty = 0
			for i := 0; i < repeat && len(out) < odfMaxCols; i++ {
				out = append(out, text)
			}
		default:
			if err := w.d.Skip(); err != nil {
				return nil, w.xmlErr(err)
			}
		}
	}
}

// sheets walks a spreadsheet body, one table:table at a time.
func (w *odfWalk) sheets(depth int) error {
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return w.xmlErr(err)
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return nil
			}
			continue
		}
		if localName(t.Name) == "table" {
			if err := w.descend(func() error { return w.sheet(odfAttr(t, "name"), depth+1) }, depth); err != nil {
				return err
			}
			continue
		}
		if err := w.descend(func() error { return w.sheets(depth + 1) }, depth); err != nil {
			return err
		}
	}
}

// sheet renders one sheet: its name as a heading, then its rows.
//
// The name is emitted as content because the user typed it — "Q3 forecast" is
// as much part of the document as a heading in a text file. It is emitted only
// when the sheet turns out to hold something, because a spreadsheet ships with
// empty sheets nobody named on purpose and a heading over nothing is a
// sentence the author never wrote.
func (w *odfWalk) sheet(name string, depth int) error {
	rows, err := w.sheetRows(depth)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	w.md.Heading(2, name)
	for _, r := range rows {
		w.md.Row(r)
	}
	return nil
}

// sheetRows collects the rows that carry data. Empty rows are dropped as they
// are read: a sheet declares its full grid, so the tail of every ODS is a
// single row element repeated a million times, and one markdown row per grid
// row would bury the data in blanks.
func (w *odfWalk) sheetRows(depth int) ([][]string, error) {
	var rows [][]string
	for {
		tok, err := w.d.Token()
		if err != nil {
			if err == io.EOF {
				return rows, nil
			}
			return nil, w.xmlErr(err)
		}
		t, ok := tok.(xml.StartElement)
		if !ok {
			if _, end := tok.(xml.EndElement); end {
				return rows, nil
			}
			continue
		}
		switch localName(t.Name) {
		case "table-row":
			cells, err := w.cells(depth + 1)
			if err != nil {
				return nil, err
			}
			if len(cells) == 0 {
				continue
			}
			repeat := odfNumber(t, "number-rows-repeated", 1)
			if repeat > odfMaxRepeat {
				repeat = odfMaxRepeat
			}
			for i := 0; i < repeat; i++ {
				rows = append(rows, cells)
			}
		case "table-header-rows", "table-rows", "table-row-group":
			nested, err := w.sheetRows(depth + 1)
			if err != nil {
				return nil, err
			}
			rows = append(rows, nested...)
		default:
			if err := w.d.Skip(); err != nil {
				return nil, w.xmlErr(err)
			}
		}
	}
}

func (w *odfWalk) xmlErr(err error) error {
	return errs.Newf(errs.CodeInvalidArgs, "%s content is not well-formed XML: %s", w.kind, err.Error()).
		WithHint("The file may be truncated. Try opening it in an editor and saving it again.")
}

// odfAttr returns an attribute by local name, ignoring its namespace prefix —
// the prefixes are writer-dependent, the local names are not.
func odfAttr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if localName(a.Name) == local {
			return a.Value
		}
	}
	return ""
}

// odfNumber reads a numeric attribute, falling back to def for a missing,
// unparseable or nonsensical value.
func odfNumber(se xml.StartElement, local string, def int) int {
	v := strings.TrimSpace(odfAttr(se, local))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	return n
}
