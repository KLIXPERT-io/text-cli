package doc

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// epubZipEntry is one archive member. Order matters: the fixtures put the
// chapters in the archive in an order that deliberately contradicts the spine.
type epubZipEntry struct {
	name  string
	body  string
	store bool
}

func epubZip(t *testing.T, entries []epubZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip header %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("zip write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

const epubContainerXML = `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles>
    <rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
  </rootfiles>
</container>`

// epubBookOPF lists the chapters in reading order One, Two, Three — which is
// neither the archive order nor the lexical order of the filenames — and keeps
// the navigation document and the colophon out of it.
const epubBookOPF = `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="id">urn:uuid:test</dc:identifier>
    <dc:title>The Test Book</dc:title>
    <dc:language>en</dc:language>
  </metadata>
  <manifest>
    <item id="one" href="text/z-one.xhtml" media-type="application/xhtml+xml"/>
    <item id="two" href="text/a-two.xhtml" media-type="application/xhtml+xml"/>
    <item id="three" href="../shared/three.xhtml" media-type="application/xhtml+xml"/>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="back" href="back.xhtml" media-type="application/xhtml+xml"/>
    <item id="cover" href="images/cover.png" media-type="image/png"/>
  </manifest>
  <spine>
    <itemref idref="one"/>
    <itemref idref="two"/>
    <itemref idref="three"/>
    <itemref idref="nav"/>
    <itemref idref="back" linear="no"/>
    <itemref idref="ghost"/>
  </spine>
</package>`

const epubChapterOne = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>ignoredtitle</title><style>.drop { content: "styleword"; }</style></head>
<body>
  <h1>Chapter One</h1>
  <p>The first paragraph of the book.</p>
  <span epub:type="pagebreak" id="p12">pagebreakword</span>
  <h2>A Section</h2>
  <p>Another paragraph <em>with</em> emphasis.</p>
  <ul><li>First item</li><li>Second item</li></ul>
  <table><tr><td>Alpha</td><td>Beta</td></tr></table>
  <script>var s = "scriptword";</script>
</body>
</html>`

const epubChapterTwo = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<body>
  <h1>Chapter Two</h1>
  <nav epub:type="toc"><ol><li>navword</li></ol></nav>
  <p>Chapter two prose.</p>
</body>
</html>`

const epubChapterThree = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body><h1>Chapter Three</h1><p>Chapter three prose.</p></body>
</html>`

const epubNavDoc = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<body><h1>Table of Contents</h1><nav epub:type="toc"><ol><li>tocentry</li></ol></nav></body>
</html>`

const epubBackMatter = `<?xml version="1.0" encoding="utf-8"?>
<html xmlns="http://www.w3.org/1999/xhtml">
<body><h1>Colophon</h1><p>colophonword</p></body>
</html>`

// epubBook is a structurally real book: an uncompressed mimetype, a container
// pointing into a subdirectory, an OPF whose hrefs are relative to that
// subdirectory (one of them climbing back out of it), and chapters whose
// archive order and filenames both disagree with the spine.
func epubBook(t *testing.T) []byte {
	t.Helper()
	return epubZip(t, []epubZipEntry{
		{name: "mimetype", body: epubMimetype, store: true},
		{name: "META-INF/container.xml", body: epubContainerXML},
		{name: "OEBPS/text/a-two.xhtml", body: epubChapterTwo},
		{name: "OEBPS/text/z-one.xhtml", body: epubChapterOne},
		{name: "shared/three.xhtml", body: epubChapterThree},
		{name: "OEBPS/nav.xhtml", body: epubNavDoc},
		{name: "OEBPS/back.xhtml", body: epubBackMatter},
		{name: "OEBPS/content.opf", body: epubBookOPF},
	})
}

// epubMinimal builds a one-chapter book with a caller-chosen manifest href and
// chapter entry name, for the resolution and empty-document cases.
func epubMinimal(t *testing.T, href, entry, chapter string) []byte {
	t.Helper()
	opf := `<?xml version="1.0" encoding="utf-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Small</dc:title></metadata>
  <manifest><item id="c1" href="` + href + `" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="c1"/></spine>
</package>`
	return epubZip(t, []epubZipEntry{
		{name: "mimetype", body: epubMimetype, store: true},
		{name: "META-INF/container.xml", body: epubContainerXML},
		{name: "OEBPS/content.opf", body: opf},
		{name: entry, body: chapter},
	})
}

func TestEPUBDecode(t *testing.T) {
	tests := []struct {
		name      string
		build     func(t *testing.T) []byte
		wantCode  errs.Code
		wantTitle string
		contains  []string
		absent    []string
		order     []string
	}{
		{
			name:  "reading order follows the spine, not the archive or the filenames",
			build: epubBook,
			order: []string{"Chapter One", "Chapter Two", "Chapter Three"},
		},
		{
			name:  "structure survives as markdown blocks",
			build: epubBook,
			contains: []string{
				"# Chapter One\n",
				"## A Section\n",
				"The first paragraph of the book.",
				"- First item\n- Second item",
				"| Alpha | Beta |",
				"Another paragraph with emphasis.",
			},
		},
		{
			name:      "the opf metadata title is the document title",
			build:     epubBook,
			wantTitle: "The Test Book",
		},
		{
			name:  "navigation, scripts, styles, page breaks and non-linear items stay out",
			build: epubBook,
			absent: []string{
				"scriptword",       // <script>
				"styleword",        // <style>
				"ignoredtitle",     // <head>
				"navword",          // a <nav> element inside a chapter
				"tocentry",         // the manifest's properties="nav" document
				"Table of Content", // ditto, its heading
				"colophonword",     // linear="no"
				"pagebreakword",    // epub:type="pagebreak"
			},
		},
		{
			name: "an href resolves against the opf directory",
			build: func(t *testing.T) []byte {
				return epubMinimal(t, "text/deep.xhtml", "OEBPS/text/deep.xhtml",
					`<html><body><h1>Deep</h1><p>Nested chapter.</p></body></html>`)
			},
			contains: []string{"# Deep", "Nested chapter."},
		},
		{
			name: "a percent-encoded href names the literal entry",
			build: func(t *testing.T) []byte {
				return epubMinimal(t, "text/a%20chapter.xhtml", "OEBPS/text/a chapter.xhtml",
					`<html><body><p>Encoded chapter.</p></body></html>`)
			},
			contains: []string{"Encoded chapter."},
		},
		{
			name: "a fragment identifier is not part of the entry name",
			build: func(t *testing.T) []byte {
				return epubMinimal(t, "text/frag.xhtml#start", "OEBPS/text/frag.xhtml",
					`<html><body><p>Fragment chapter.</p></body></html>`)
			},
			contains: []string{"Fragment chapter."},
		},
		{
			name: "bytes that are not a zip are refused",
			build: func(t *testing.T) []byte {
				return []byte("This is a plain sentence, not a book.")
			},
			wantCode: errs.CodeInvalidArgs,
		},
		{
			name: "a zip without a container is refused",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{
					{name: "[Content_Types].xml", body: `<Types/>`},
					{name: "word/document.xml", body: `<w:document/>`},
				})
			},
			wantCode: errs.CodeInvalidArgs,
		},
		{
			name: "a container naming a missing package document is refused",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{
					{name: "mimetype", body: epubMimetype, store: true},
					{name: "META-INF/container.xml", body: epubContainerXML},
				})
			},
			wantCode: errs.CodeInvalidArgs,
		},
		{
			name: "a book whose spine documents hold no text is empty input",
			build: func(t *testing.T) []byte {
				return epubMinimal(t, "empty.xhtml", "OEBPS/empty.xhtml",
					`<html><head><title>Nothing</title></head><body><p>  </p></body></html>`)
			},
			wantCode: errs.CodeEmptyInput,
		},
	}

	d := &epubDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Decode(tc.build(t))
			if tc.wantCode != "" {
				var e *errs.E
				if !errors.As(err, &e) {
					t.Fatalf("want *errs.E %s, got err=%v doc=%+v", tc.wantCode, err, got)
				}
				if e.Code != tc.wantCode {
					t.Fatalf("want code %s, got %s (%s)", tc.wantCode, e.Code, e.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if tc.wantTitle != "" && got.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, tc.wantTitle)
			}
			// An EPUB reflows; a page count here would be invented.
			if got.Pages != 0 {
				t.Errorf("pages = %d, want 0", got.Pages)
			}
			for _, want := range tc.contains {
				if !strings.Contains(got.Content, want) {
					t.Errorf("content missing %q\n---\n%s", want, got.Content)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(got.Content, bad) {
					t.Errorf("content contains %q, which should have been skipped\n---\n%s", bad, got.Content)
				}
			}
			prev := -1
			for _, want := range tc.order {
				at := strings.Index(got.Content, want)
				if at < 0 {
					t.Fatalf("content missing %q\n---\n%s", want, got.Content)
				}
				if at < prev {
					t.Fatalf("%q is out of reading order\n---\n%s", want, got.Content)
				}
				prev = at
			}
		})
	}
}

func TestEPUBSniff(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T) []byte
		want  bool
	}{
		{
			name:  "a well-formed book",
			build: epubBook,
			want:  true,
		},
		{
			name: "a book whose writer got the mimetype entry wrong",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{
					{name: "mimetype", body: "application/zip"},
					{name: "META-INF/container.xml", body: epubContainerXML},
					{name: "OEBPS/content.opf", body: epubBookOPF},
				})
			},
			want: true,
		},
		{
			name: "a book with no mimetype entry at all",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{
					{name: "META-INF/container.xml", body: epubContainerXML},
					{name: "OEBPS/content.opf", body: epubBookOPF},
				})
			},
			want: true,
		},
		{
			name: "another zip-based office format",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{
					{name: "mimetype", body: "application/vnd.oasis.opendocument.text", store: true},
					{name: "content.xml", body: `<office:document-content/>`},
				})
			},
			want: false,
		},
		{
			name: "a plain zip",
			build: func(t *testing.T) []byte {
				return epubZip(t, []epubZipEntry{{name: "notes.txt", body: "hello"}})
			},
			want: false,
		},
		{
			name:  "plain text",
			build: func(t *testing.T) []byte { return []byte("PK is not the start of this sentence.") },
			want:  false,
		},
		{
			name:  "nothing",
			build: func(t *testing.T) []byte { return nil },
			want:  false,
		},
	}

	d := &epubDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Sniff(tc.build(t)); got != tc.want {
				t.Errorf("Sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEPUBRegistration(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "the decoder is registered under its own name",
			run: func(t *testing.T) {
				d, err := Open(FormatEPUB)
				if err != nil {
					t.Fatalf("Open: %v", err)
				}
				if d.Name() != FormatEPUB {
					t.Errorf("Name = %q, want %q", d.Name(), FormatEPUB)
				}
			},
		},
		{
			name: "a .epub file routes to this decoder",
			run: func(t *testing.T) {
				if got := ForFile("/books/novel.EPUB", nil); got != FormatEPUB {
					t.Errorf("ForFile = %q, want %q", got, FormatEPUB)
				}
			},
		},
		{
			name: "it claims exactly one extension",
			run: func(t *testing.T) {
				d := &epubDecoder{}
				if got := d.Extensions(); len(got) != 1 || got[0] != ".epub" {
					t.Errorf("Extensions = %v, want [.epub]", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.run(t) })
	}
}

func TestEPUBXHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a nested list keeps its depth",
			in:   `<body><ul><li>outer<ul><li>inner</li></ul></li><li>second</li></ul></body>`,
			want: "- outer\n  - inner\n- second\n",
		},
		{
			name: "an ordered list is numbered",
			in:   `<body><ol><li>one</li><li>two</li></ol></body>`,
			want: "1. one\n1. two\n",
		},
		{
			name: "a line break ends a block, so verse is not one sentence",
			in:   `<body><p>Roses are red<br/>violets are blue</p></body>`,
			want: "Roses are red\n\nviolets are blue\n",
		},
		{
			name: "a break inside an item keeps the item whole",
			in:   `<body><ul><li>first<br/>still first</li></ul></body>`,
			want: "- first still first\n",
		},
		{
			name: "a paragraph inside a cell stays in the cell",
			in:   `<body><table><tr><td><p>a</p></td><td>b</td></tr></table></body>`,
			want: "| a | b |\n",
		},
		{
			name: "text loose in a container is still a block",
			in:   `<body><div>Loose text</div><div>More text</div></body>`,
			want: "Loose text\n\nMore text\n",
		},
		{
			name: "html entities are resolved, not left as words",
			in:   `<body><p>caf&eacute; &amp; cr&egrave;me</p></body>`,
			want: "café & crème\n",
		},
		{
			name: "a page break marker with an undeclared epub prefix is still skipped",
			in:   `<body><p>text</p><span epub:type="pagebreak">241</span></body>`,
			want: "text\n",
		},
		{
			name: "the aria spelling of a page break is skipped too",
			in:   `<body><p>text</p><span role="doc-pagebreak">241</span></body>`,
			want: "text\n",
		},
		{
			name: "an unclosed paragraph still produces its block",
			in:   `<body><p>dangling`,
			want: "dangling\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var md mdBuilder
			epubXHTML(&md, []byte(tc.in))
			if got := md.String(); got != tc.want {
				t.Errorf("markdown =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestEPUBResolve(t *testing.T) {
	tests := []struct {
		name string
		base string
		href string
		want string
	}{
		{name: "relative to the opf directory", base: "OEBPS", href: "text/ch1.xhtml", want: "OEBPS/text/ch1.xhtml"},
		{name: "climbing out of the opf directory", base: "OEBPS", href: "../shared/a.xhtml", want: "shared/a.xhtml"},
		{name: "an opf at the archive root", base: ".", href: "ch1.xhtml", want: "ch1.xhtml"},
		{name: "a fragment addresses a position, not a file", base: "OEBPS", href: "ch1.xhtml#p3", want: "OEBPS/ch1.xhtml"},
		{name: "a percent-encoded space", base: "OEBPS", href: "a%20b.xhtml", want: "OEBPS/a b.xhtml"},
		{name: "a root-relative href", base: "OEBPS", href: "/text/ch1.xhtml", want: "text/ch1.xhtml"},
		{name: "an empty href", base: "OEBPS", href: "  ", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := epubResolve(tc.base, tc.href); got != tc.want {
				t.Errorf("epubResolve(%q, %q) = %q, want %q", tc.base, tc.href, got, tc.want)
			}
		})
	}
}
