package doc

import (
	"archive/zip"
	"encoding/xml"
	"net/url"
	"path"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatEPUB is the registered name of this decoder.
const FormatEPUB = "epub"

// epubMimetype is the exact string the specification requires in the archive's
// first, uncompressed "mimetype" entry.
const epubMimetype = "application/epub+zip"

// epubContainerPath is the one entry whose location the specification fixes.
// Everything else in a book — the package document, the chapters — is found by
// following it, which is why a book with no container is not a book.
const epubContainerPath = "META-INF/container.xml"

func init() {
	Register(FormatEPUB, func() (Decoder, error) { return &epubDecoder{}, nil })
}

// epubDecoder reads an EPUB 2 or 3 book as markdown.
//
// The work that matters here is reading order. A book's chapters are a set of
// XHTML files in a zip, and neither the archive's own order nor the filenames
// tell you how to read them: the order lives in the package document's spine,
// and the spine references manifest ids, and the manifest's hrefs are relative
// to the package document's own directory rather than to the archive root.
// Skipping any of those three steps produces prose in the wrong order, which
// scores identically and reads as nonsense.
type epubDecoder struct{}

func (d *epubDecoder) Name() string { return FormatEPUB }

func (d *epubDecoder) Extensions() []string { return []string{".epub"} }

// Sniff looks inside the zip, because the four zip-based formats in this
// package share one magic number.
//
// The mimetype entry is the specification's own answer and is checked first.
// The container fallback is not defensiveness for its own sake: writers that
// deflate the mimetype entry, misspell it, or leave it out entirely are common
// enough that every reader tolerates it, and a book that reaches this CLI has
// usually already been opened somewhere else. A stray zip cannot be caught by
// the fallback, since META-INF/container.xml belongs to EPUB alone.
func (d *epubDecoder) Sniff(data []byte) bool {
	if !isZip(data) {
		return false
	}
	r, err := openZip(FormatEPUB, data)
	if err != nil {
		return false
	}
	if b, ok, err := zipEntry(r, "mimetype"); err == nil && ok {
		if strings.TrimSpace(string(b)) == epubMimetype {
			return true
		}
	}
	return zipHas(r, epubContainerPath)
}

func (d *epubDecoder) Decode(data []byte) (*Document, error) {
	r, err := openZip(FormatEPUB, data)
	if err != nil {
		return nil, err
	}

	opfPath, err := epubRootfile(r)
	if err != nil {
		return nil, err
	}

	// One budget for the whole book, shared by the package document and every
	// chapter: see zipMaxText for why the bound is on bytes rather than on a
	// number of spine items.
	budget := zipNewBudget()
	opfData, ok, err := zipRead(r, opfPath, budget)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.Newf(errs.CodeInvalidArgs, "the epub names %q as its package document but the archive has no such entry", opfPath).
			WithHint("The book is malformed. Re-export it, or open and re-save it in an EPUB editor.")
	}
	var pkg epubPackage
	if err := epubUnmarshal(opfData, &pkg); err != nil {
		return nil, errs.Newf(errs.CodeInvalidArgs, "the epub package document %q is unreadable: %s", opfPath, err.Error())
	}

	// Manifest ids are what the spine references; hrefs are resolved against
	// the package document's directory, not the archive root.
	items := make(map[string]epubItem, len(pkg.Manifest.Items))
	for _, it := range pkg.Manifest.Items {
		if id := strings.TrimSpace(it.ID); id != "" {
			items[id] = it
		}
	}
	base := path.Dir(opfPath)

	var md mdBuilder
	for _, ref := range pkg.Spine.Items {
		// linear="no" is the book saying this document is not part of the
		// reading sequence — a cover, an ad, a colophon. Counting it changes
		// the numbers for text the reader may never see.
		if strings.EqualFold(strings.TrimSpace(ref.Linear), "no") {
			continue
		}
		it, ok := items[strings.TrimSpace(ref.IDRef)]
		if !ok {
			// A spine entry with no manifest item is a broken book, not an
			// unreadable one. Dropping the reference costs one document;
			// failing costs the whole book.
			continue
		}
		if mt := strings.ToLower(it.MediaType); mt != "" && !strings.Contains(mt, "html") {
			continue
		}
		// EPUB 3 marks the navigation document in the manifest, and some books
		// leave it in the spine as linear content. Its prose is a chapter list;
		// including it adds a table of contents to the word count.
		if epubHasProperty(it.Properties, "nav") {
			continue
		}
		name := epubResolve(base, it.Href)
		if name == "" {
			continue
		}
		b, ok, err := zipRead(r, name, budget)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		epubXHTML(&md, b)
	}

	if md.Empty() {
		return nil, EmptyErr(FormatEPUB, "")
	}
	return &Document{
		Content: md.String(),
		Title:   cleanInline(pkg.Metadata.Title),
		// Pages stays 0. An EPUB reflows: its page count is a property of the
		// reader's screen and font size, so any number here would be invented.
	}, nil
}

// ---------------------------------------------------------------------------
// Container and package document
// ---------------------------------------------------------------------------

// epubContainer is META-INF/container.xml: the pointer to the package document.
type epubContainer struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

// epubPackage is the OPF: the metadata, the id→href manifest, and the spine
// that orders them. Field tags carry no namespace on purpose — EPUB 2 and
// EPUB 3 declare different OPF and Dublin Core namespaces, and encoding/xml
// matches on the local name when the tag omits one.
type epubPackage struct {
	Metadata struct {
		Title string `xml:"title"`
	} `xml:"metadata"`
	Manifest struct {
		Items []epubItem `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// epubRootfile returns the path of the package document.
//
// The first rootfile declaring the OPF media type wins; a book may list several
// renditions of the same content, and the first is the one a reader opens.
func epubRootfile(r *zip.Reader) (string, error) {
	data, ok, err := zipEntry(r, epubContainerPath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errs.Newf(errs.CodeInvalidArgs, "not an epub: the archive has no %s", epubContainerPath).
			WithHint("This is a zip, but not a book. If it is another zip-based document, name it with --from.")
	}
	var c epubContainer
	if err := epubUnmarshal(data, &c); err != nil {
		return "", errs.Newf(errs.CodeInvalidArgs, "the epub %s is unreadable: %s", epubContainerPath, err.Error())
	}
	fallback := ""
	for _, rf := range c.Rootfiles {
		p := epubResolve("", rf.FullPath)
		if p == "" {
			continue
		}
		if strings.Contains(strings.ToLower(rf.MediaType), "oebps-package") {
			return p, nil
		}
		if fallback == "" {
			fallback = p
		}
	}
	if fallback != "" {
		return fallback, nil
	}
	return "", errs.Newf(errs.CodeInvalidArgs, "the epub %s names no package document", epubContainerPath).
		WithHint("The book is malformed. Re-export it from the tool that produced it.")
}

// epubResolve turns a manifest href into an archive entry name.
//
// Three things happen here and all three are load-bearing. The href is relative
// to the package document's directory, so "text/ch1.xhtml" in an OPF under
// OEBPS/ is "OEBPS/text/ch1.xhtml" — this is the step a naive reader skips, and
// it fails on every book that keeps its OPF in a subdirectory, which is most of
// them. A fragment identifier addresses a position inside a document, not
// another file. And an href is a URI, so a space is written "%20" while the zip
// entry it names holds the literal space.
func epubResolve(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	if i := strings.IndexAny(href, "#?"); i >= 0 {
		href = href[:i]
	}
	if unescaped, err := url.PathUnescape(href); err == nil {
		href = unescaped
	}
	if base != "" && base != "." && !strings.HasPrefix(href, "/") {
		href = path.Join(base, href)
	}
	// Clean resolves the "../" a book uses to climb out of its own directory;
	// zip entry names are always root-relative, so the leading slash goes.
	return strings.TrimPrefix(path.Clean(href), "/")
}

// epubHasProperty reports whether an OPF properties attribute — a
// space-separated list — carries one property.
func epubHasProperty(properties, want string) bool {
	for _, p := range strings.Fields(properties) {
		if strings.EqualFold(p, want) {
			return true
		}
	}
	return false
}

// epubUnmarshal parses one of the book's XML files, tolerating the encodings
// and the loose markup real books ship. See newXMLDecoder for the charset half.
func epubUnmarshal(data []byte, v any) error {
	d := newXMLDecoder(data)
	d.Entity = xml.HTMLEntity
	return d.Decode(v)
}

// ---------------------------------------------------------------------------
// XHTML → markdown
// ---------------------------------------------------------------------------

// epubSkip are the subtrees that hold no prose.
//
// nav is the interesting one: an EPUB 3 navigation element is the table of
// contents, and a chapter list is not text the author wrote — counted, it adds
// a few dozen title-case fragments with no verbs and drags every per-sentence
// average with it. script and style would contribute source code, and head
// contributes the document title, which the OPF already gives us properly.
var epubSkip = map[string]bool{
	"script": true,
	"style":  true,
	"head":   true,
	"nav":    true,
}

// epubHeading maps a heading element to its level.
var epubHeading = map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

// epubBlock are the elements that end a block of prose. Container elements
// (div, section) are included because a book that wraps every paragraph in a
// div and puts the text straight inside it is common, and without them that
// book decodes as one enormous paragraph.
var epubBlock = map[string]bool{
	"p": true, "div": true, "blockquote": true, "section": true,
	"article": true, "aside": true, "header": true, "footer": true,
	"main": true, "figure": true, "figcaption": true, "pre": true,
	"dd": true, "dt": true, "caption": true, "body": true,
}

// epubXHTML appends one spine document's blocks to md.
//
// A parse error stops this document and returns; it does not fail the book. A
// malformed chapter costs the tail of that chapter, and refusing the whole
// volume over one bad tag would make the decoder less useful than the reader
// the book was written for.
func epubXHTML(md *mdBuilder, data []byte) {
	w := epubWalker{md: md}
	d := newXMLDecoder(data)
	d.Entity = xml.HTMLEntity
	// Real books ship HTML-style void tags. Non-strict mode invents the missing
	// end tag either way; AutoClose does it at the right place.
	d.AutoClose = xml.HTMLAutoClose
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			w.start(t)
		case xml.EndElement:
			w.end(t)
		case xml.CharData:
			w.text(string(t))
		}
	}
	w.flush()
}

// epubWalker turns XHTML events into mdBuilder blocks.
//
// It carries the little state a document's structure needs and nothing more:
// how deep the current list is, whether a cell or a heading is open, and how
// many levels of a skipped subtree are still to be closed.
type epubWalker struct {
	md  *mdBuilder
	buf strings.Builder
	// skip counts open elements inside a skipped subtree, so a nested <script>
	// inside a <nav> does not re-open the document at its end tag.
	skip  int
	head  int
	lists []bool
	cells []string
	// inCell holds a table cell's text together: a <p> inside a <td> is part of
	// the cell, not a paragraph of its own.
	inCell bool
	inItem bool
}

func (w *epubWalker) text(s string) {
	if w.skip > 0 {
		return
	}
	w.buf.WriteString(s)
}

func (w *epubWalker) take() string {
	s := w.buf.String()
	w.buf.Reset()
	return s
}

// closeBlock emits the pending text as whatever block encloses it.
func (w *epubWalker) closeBlock(level int) {
	if w.inCell {
		// Keep the cell's text in one piece, but do not let two nested blocks
		// run their last and first words together.
		w.buf.WriteByte(' ')
		return
	}
	text := w.take()
	switch {
	case level > 0:
		w.md.Heading(level, text)
	case w.inItem && len(w.lists) > 0:
		w.md.Item(len(w.lists)-1, w.lists[len(w.lists)-1], text)
	default:
		w.md.Para(text)
	}
}

// flush closes any text left pending by markup that never ended.
func (w *epubWalker) flush() {
	if strings.TrimSpace(w.buf.String()) == "" {
		w.buf.Reset()
		return
	}
	w.closeBlock(0)
}

func (w *epubWalker) start(el xml.StartElement) {
	if w.skip > 0 {
		w.skip++
		return
	}
	name := strings.ToLower(localName(el.Name))
	if epubSkip[name] || epubPageBreak(el) {
		w.skip = 1
		return
	}
	if level, ok := epubHeading[name]; ok {
		w.flush()
		w.head = level
		return
	}
	switch name {
	case "ul", "ol":
		// A nested list interrupts its own item: the text before it belongs to
		// the parent item and has to be emitted before the children are.
		w.flush()
		w.lists = append(w.lists, name == "ol")
		w.inItem = false
	case "li":
		w.flush()
		w.inItem = true
	case "tr":
		w.flush()
		w.cells = nil
	case "td", "th":
		w.flush()
		w.inCell = true
	case "br":
		// A line break inside a heading, an item or a cell is still that one
		// block; anywhere else it is what separates two lines of verse or of an
		// address, and gluing those together produces one unpunctuated
		// document-long sentence.
		if w.head > 0 || w.inCell || w.inItem {
			w.buf.WriteByte(' ')
			return
		}
		w.flush()
	case "hr":
		w.flush()
	default:
		if epubBlock[name] {
			w.flush()
		}
	}
}

func (w *epubWalker) end(el xml.EndElement) {
	if w.skip > 0 {
		w.skip--
		return
	}
	name := strings.ToLower(localName(el.Name))
	if level, ok := epubHeading[name]; ok {
		w.closeBlock(level)
		w.head = 0
		return
	}
	switch name {
	case "ul", "ol":
		w.flush()
		if n := len(w.lists); n > 0 {
			w.lists = w.lists[:n-1]
		}
		w.inItem = false
	case "li":
		if !w.inCell {
			depth := len(w.lists) - 1
			if depth < 0 {
				depth = 0
			}
			ordered := len(w.lists) > 0 && w.lists[len(w.lists)-1]
			w.md.Item(depth, ordered, w.take())
		}
		w.inItem = false
	case "td", "th":
		w.inCell = false
		w.cells = append(w.cells, w.take())
	case "tr":
		w.md.Row(w.cells)
		w.cells = nil
	case "table":
		if len(w.cells) > 0 {
			w.md.Row(w.cells)
			w.cells = nil
		}
		w.flush()
	default:
		if epubBlock[name] {
			w.closeBlock(0)
		}
	}
}

// epubPageBreak reports whether an element is a page-break marker.
//
// A print book's page numbers are carried in the reflowed text as
// epub:type="pagebreak" (EPUB 3) or role="doc-pagebreak" (the ARIA spelling
// some books use instead). The content is a bare numeral: left in, it becomes a
// one-word sentence every few hundred words and quietly moves every average.
func epubPageBreak(el xml.StartElement) bool {
	for _, a := range el.Attr {
		switch strings.ToLower(localName(a.Name)) {
		case "type":
			// Namespaced: epub:type. An unnamespaced type= belongs to a form
			// control or a stylesheet link and says nothing about page breaks.
			if a.Name.Space != "" && strings.Contains(strings.ToLower(a.Value), "pagebreak") {
				return true
			}
		case "role":
			if strings.Contains(strings.ToLower(a.Value), "doc-pagebreak") {
				return true
			}
		}
	}
	return false
}
