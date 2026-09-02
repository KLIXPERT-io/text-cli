package doc

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// odfEntry is one file in a built container. store mirrors the spec's
// requirement that "mimetype" be written uncompressed, so both halves of that
// can be exercised.
type odfEntry struct {
	name  string
	body  string
	store bool
}

func odfArchive(t *testing.T, entries ...odfEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		f, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip header %s: %v", e.name, err)
		}
		if _, err := f.Write([]byte(e.body)); err != nil {
			t.Fatalf("zip write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

const odfNS = `xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
	`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" ` +
	`xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" ` +
	`xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" ` +
	`xmlns:dc="http://purl.org/dc/elements/1.1/"`

func odfContent(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-content ` + odfNS + `>` +
		`<office:automatic-styles><style:style style:name="P1"/></office:automatic-styles>` +
		`<office:body>` + inner + `</office:body>` +
		`</office:document-content>`
}

func odfMeta(title string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document-meta ` + odfNS + `><office:meta>` +
		`<meta:generator xmlns:meta="urn:oasis:names:tc:opendocument:xmlns:meta:1.0">Test</meta:generator>` +
		`<dc:title>` + title + `</dc:title>` +
		`</office:meta></office:document-meta>`
}

// odtFile builds a text document whose office:text holds body.
func odtFile(t *testing.T, body, title string) []byte {
	t.Helper()
	entries := []odfEntry{
		{name: "mimetype", body: mimeODT, store: true},
		{name: "content.xml", body: odfContent(`<office:text>` + body + `</office:text>`)},
	}
	if title != "" {
		entries = append(entries, odfEntry{name: "meta.xml", body: odfMeta(title)})
	}
	return odfArchive(t, entries...)
}

// odsFile builds a spreadsheet whose office:spreadsheet holds body.
func odsFile(t *testing.T, body string) []byte {
	t.Helper()
	return odfArchive(t,
		odfEntry{name: "mimetype", body: mimeODS, store: true},
		odfEntry{name: "content.xml", body: odfContent(`<office:spreadsheet>` + body + `</office:spreadsheet>`)},
	)
}

// odfFlat builds the single-file variant: meta, master styles and body under
// one root, with the media type as an attribute instead of an entry.
func odfFlat(mime, body string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<office:document ` + odfNS + ` office:mimetype="` + mime + `">` +
		`<office:meta><dc:title>Flat Title</dc:title></office:meta>` +
		`<office:master-styles><style:master-page><style:header>` +
		`<text:p>Header boilerplate</text:p>` +
		`</style:header></style:master-page></office:master-styles>` +
		`<office:body><office:text>` + body + `</office:text></office:body>` +
		`</office:document>`)
}

func odfCode(t *testing.T, err error) errs.Code {
	t.Helper()
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("want *errs.E, got %T: %v", err, err)
	}
	return e.Code
}

func TestODFDecode(t *testing.T) {
	tests := []struct {
		name    string
		format  string
		data    []byte
		want    []string
		notWant []string
		exact   string
		title   string
		errCode errs.Code
	}{
		{
			name:   "odt outline level becomes heading depth",
			format: FormatODT,
			data: odtFile(t, `<text:h text:outline-level="1">Chapter One</text:h>`+
				`<text:p>An opening paragraph.</text:p>`+
				`<text:h text:outline-level="3">A Deep Section</text:h>`+
				`<text:p>Body with a <text:span text:style-name="T1">styled</text:span> word.</text:p>`, ""),
			want: []string{
				"# Chapter One\n",
				"### A Deep Section\n",
				"An opening paragraph.",
				"Body with a styled word.",
			},
			notWant: []string{"#### "},
		},
		{
			name:    "odt heading without outline level defaults to level one",
			format:  FormatODT,
			data:    odtFile(t, `<text:h>Untitled Level</text:h>`, ""),
			want:    []string{"# Untitled Level"},
			notWant: []string{"## Untitled Level"},
		},
		{
			name:   "odt nested list indents by nesting depth",
			format: FormatODT,
			data: odtFile(t, `<text:list><text:list-item><text:p>Outer item</text:p>`+
				`<text:list><text:list-item><text:p>Inner item</text:p></text:list-item></text:list>`+
				`</text:list-item><text:list-item><text:p>Second outer</text:p></text:list-item></text:list>`, ""),
			exact: "- Outer item\n  - Inner item\n- Second outer\n",
		},
		{
			name:   "odt table becomes markdown rows",
			format: FormatODT,
			data: odtFile(t, `<table:table table:name="Table1">`+
				`<table:table-row>`+
				`<table:table-cell><text:p>Metric</text:p></table:table-cell>`+
				`<table:table-cell><text:p>Value</text:p></table:table-cell>`+
				`</table:table-row>`+
				`<table:table-row>`+
				`<table:table-cell><text:p>Flesch</text:p></table:table-cell>`+
				`<table:table-cell><text:p>62</text:p></table:table-cell>`+
				`</table:table-row></table:table>`, ""),
			exact: "| Metric | Value |\n| Flesch | 62 |\n",
		},
		{
			name:   "odt drops footnotes and comments",
			format: FormatODT,
			data: odtFile(t, `<text:p>Body text<text:note text:note-class="footnote"><text:note-citation>1</text:note-citation>`+
				`<text:note-body><text:p>Footnote prose.</text:p></text:note-body></text:note> continues here.</text:p>`+
				`<office:annotation><text:p>Reviewer comment.</text:p></office:annotation>`+
				`<text:tracked-changes><text:changed-region><text:deletion><text:p>Deleted sentence.</text:p></text:deletion></text:changed-region></text:tracked-changes>`, ""),
			want:    []string{"Body text continues here."},
			notWant: []string{"Footnote prose.", "Reviewer comment.", "Deleted sentence."},
		},
		{
			name:   "odt space and tab elements are whitespace",
			format: FormatODT,
			data:   odtFile(t, `<text:p>A<text:s text:c="4"/>B<text:tab/>C<text:line-break/>D</text:p>`, ""),
			exact:  "A B C D\n",
		},
		{
			name:   "odt paragraph inside a frame is still a paragraph",
			format: FormatODT,
			data: odtFile(t, `<text:p>Before.</text:p>`+
				`<draw:frame xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0">`+
				`<draw:text-box><text:p>Inside the box.</text:p></draw:text-box></draw:frame>`, ""),
			want: []string{"Before.\n\nInside the box."},
		},
		{
			name:   "odt with a deflated mimetype entry still decodes",
			format: FormatODT,
			data: odfArchive(t,
				odfEntry{name: "mimetype", body: mimeODT},
				odfEntry{name: "content.xml", body: odfContent(`<office:text><text:p>Compressed mimetype.</text:p></office:text>`)},
			),
			want: []string{"Compressed mimetype."},
		},
		{
			name:   "odt title comes from meta.xml",
			format: FormatODT,
			data:   odtFile(t, `<text:p>Some prose.</text:p>`, "Quarterly Report"),
			want:   []string{"Some prose."},
			title:  "Quarterly Report",
		},
		{
			name:   "flat odt decodes and ignores master styles",
			format: FormatODT,
			data: odfFlat(mimeODT, `<text:h text:outline-level="2">Flat Heading</text:h>`+
				`<text:p>Flat body.</text:p>`),
			want:    []string{"## Flat Heading", "Flat body."},
			notWant: []string{"Header boilerplate"},
			title:   "Flat Title",
		},
		{
			name:   "ods sheet name becomes a heading above its rows",
			format: FormatODS,
			data: odsFile(t, `<table:table table:name="Sales">`+
				`<table:table-header-rows><table:table-row>`+
				`<table:table-cell><text:p>Quarter</text:p></table:table-cell>`+
				`<table:table-cell><text:p>Revenue</text:p></table:table-cell>`+
				`</table:table-row></table:table-header-rows>`+
				`<table:table-row>`+
				`<table:table-cell><text:p>Q1</text:p></table:table-cell>`+
				`<table:table-cell><text:p>10</text:p></table:table-cell>`+
				`</table:table-row></table:table>`),
			exact: "## Sales\n\n| Quarter | Revenue |\n| Q1 | 10 |\n",
		},
		{
			name:   "ods expands number-columns-repeated",
			format: FormatODS,
			data: odsFile(t, `<table:table table:name="Grid">`+
				`<table:table-row>`+
				`<table:table-cell table:number-columns-repeated="3"><text:p>x</text:p></table:table-cell>`+
				`<table:table-cell><text:p>y</text:p></table:table-cell>`+
				`</table:table-row>`+
				`<table:table-row>`+
				`<table:table-cell><text:p>a</text:p></table:table-cell>`+
				`<table:table-cell table:number-columns-repeated="2"/>`+
				`<table:table-cell><text:p>d</text:p></table:table-cell>`+
				`</table:table-row></table:table>`),
			exact: "## Grid\n\n| x | x | x | y |\n| a |  |  | d |\n",
		},
		{
			name:   "ods drops padding rows and trailing empty columns",
			format: FormatODS,
			data: odsFile(t, `<table:table table:name="Data">`+
				`<table:table-column table:number-columns-repeated="16384"/>`+
				`<table:table-row>`+
				`<table:table-cell><text:p>only</text:p></table:table-cell>`+
				`<table:table-cell table:number-columns-repeated="16383"/>`+
				`</table:table-row>`+
				`<table:table-row table:number-rows-repeated="1048575">`+
				`<table:table-cell table:number-columns-repeated="16384"/>`+
				`</table:table-row></table:table>`),
			exact: "## Data\n\n| only |\n",
		},
		{
			name:   "ods repeats a row that carries data",
			format: FormatODS,
			data: odsFile(t, `<table:table table:name="Repeat">`+
				`<table:table-row table:number-rows-repeated="2">`+
				`<table:table-cell><text:p>same</text:p></table:table-cell>`+
				`</table:table-row></table:table>`),
			exact: "## Repeat\n\n| same |\n| same |\n",
		},
		{
			name:   "ods skips a sheet that holds nothing",
			format: FormatODS,
			data: odsFile(t, `<table:table table:name="Empty">`+
				`<table:table-row table:number-rows-repeated="1048576">`+
				`<table:table-cell table:number-columns-repeated="16384"/>`+
				`</table:table-row></table:table>`+
				`<table:table table:name="Real">`+
				`<table:table-row><table:table-cell><text:p>value</text:p></table:table-cell></table:table-row>`+
				`</table:table>`),
			exact: "## Real\n\n| value |\n",
		},
		{
			name:    "spreadsheet read as odt is a clean error",
			format:  FormatODT,
			data:    odsFile(t, `<table:table table:name="S"><table:table-row><table:table-cell><text:p>v</text:p></table:table-cell></table:table-row></table:table>`),
			errCode: errs.CodeInvalidArgs,
		},
		{
			name:    "text document read as ods is a clean error",
			format:  FormatODS,
			data:    odtFile(t, `<text:p>Prose.</text:p>`, ""),
			errCode: errs.CodeInvalidArgs,
		},
		{
			name:    "plain bytes are not an odt",
			format:  FormatODT,
			data:    []byte("This is just a sentence in a text file."),
			errCode: errs.CodeInvalidArgs,
		},
		{
			name:    "zip without content.xml is not an odt",
			format:  FormatODT,
			data:    odfArchive(t, odfEntry{name: "mimetype", body: mimeODT, store: true}),
			errCode: errs.CodeInvalidArgs,
		},
		{
			name:    "odt with no prose is an empty input",
			format:  FormatODT,
			data:    odtFile(t, `<text:p/><text:p>   </text:p>`, ""),
			errCode: errs.CodeEmptyInput,
		},
		{
			name:    "ods with no populated cell is an empty input",
			format:  FormatODS,
			data:    odsFile(t, `<table:table table:name="Sheet1"><table:table-row><table:table-cell/></table:table-row></table:table>`),
			errCode: errs.CodeEmptyInput,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(tc.format)
			if err != nil {
				t.Fatalf("Open(%q): %v", tc.format, err)
			}
			got, err := d.Decode(tc.data)
			if tc.errCode != "" {
				if err == nil {
					t.Fatalf("want error %s, got document %q", tc.errCode, got.Content)
				}
				if code := odfCode(t, err); code != tc.errCode {
					t.Fatalf("want code %s, got %s (%v)", tc.errCode, code, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if tc.exact != "" && got.Content != tc.exact {
				t.Fatalf("content:\ngot  %q\nwant %q", got.Content, tc.exact)
			}
			for _, w := range tc.want {
				if !strings.Contains(got.Content, w) {
					t.Errorf("content %q does not contain %q", got.Content, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(got.Content, w) {
					t.Errorf("content %q must not contain %q", got.Content, w)
				}
			}
			if got.Title != tc.title {
				t.Errorf("title: got %q, want %q", got.Title, tc.title)
			}
		})
	}
}

func TestODFSniff(t *testing.T) {
	odt := odtFile(t, `<text:p>Prose.</text:p>`, "")
	ods := odsFile(t, `<table:table table:name="S"><table:table-row><table:table-cell><text:p>v</text:p></table:table-cell></table:table-row></table:table>`)

	tests := []struct {
		name   string
		format string
		data   []byte
		want   bool
	}{
		{name: "odt claims an odt container", format: FormatODT, data: odt, want: true},
		{name: "ods declines an odt container", format: FormatODS, data: odt, want: false},
		{name: "ods claims an ods container", format: FormatODS, data: ods, want: true},
		{name: "odt declines an ods container", format: FormatODT, data: ods, want: false},
		{
			name:   "a deflated mimetype entry is still recognised",
			format: FormatODT,
			data: odfArchive(t,
				odfEntry{name: "mimetype", body: mimeODT},
				odfEntry{name: "content.xml", body: odfContent(`<office:text><text:p>p</text:p></office:text>`)},
			),
			want: true,
		},
		{
			name:   "a zip with no mimetype entry is declined",
			format: FormatODT,
			data:   odfArchive(t, odfEntry{name: "content.xml", body: odfContent(`<office:text><text:p>p</text:p></office:text>`)}),
			want:   false,
		},
		{name: "prose is not an odt", format: FormatODT, data: []byte("Just a sentence."), want: false},
		{name: "flat odt is claimed by odt", format: FormatODT, data: odfFlat(mimeODT, `<text:p>p</text:p>`), want: true},
		{name: "flat odt is declined by ods", format: FormatODS, data: odfFlat(mimeODT, `<text:p>p</text:p>`), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(tc.format)
			if err != nil {
				t.Fatalf("Open(%q): %v", tc.format, err)
			}
			if got := d.Sniff(tc.data); got != tc.want {
				t.Fatalf("Sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestODFRouting(t *testing.T) {
	odt := odtFile(t, `<text:h text:outline-level="1">Routed</text:h>`, "")
	ods := odsFile(t, `<table:table table:name="S"><table:table-row><table:table-cell><text:p>v</text:p></table:table-cell></table:table-row></table:table>`)

	tests := []struct {
		name string
		path string
		data []byte
		want string
	}{
		{name: "extension routes an odt", path: "report.odt", data: odt, want: FormatODT},
		{name: "extension routes a flat odt", path: "report.fodt", data: odfFlat(mimeODT, `<text:p>p</text:p>`), want: FormatODT},
		{name: "extension routes an ods", path: "sheet.ods", data: ods, want: FormatODS},
		{name: "a nameless stream is routed by its mimetype entry", path: "", data: ods, want: FormatODS},
		{name: "the extension wins over the mimetype entry", path: "sheet.ods", data: odt, want: FormatODS},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForFile(tc.path, tc.data); got != tc.want {
				t.Fatalf("ForFile(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestODFDecodeStampsFormat pins that the package-level entry point fills in
// the provenance field, since a score is only traceable through it.
func TestODFDecodeStampsFormat(t *testing.T) {
	data := odtFile(t, `<text:p>Traceable prose.</text:p>`, "")
	got, err := Decode("auto", "report.odt", data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Format != FormatODT {
		t.Fatalf("format: got %q, want %q", got.Format, FormatODT)
	}
	if got.Pages != 0 {
		t.Fatalf("pages: got %d, want 0 — an ODF document has no pages of its own", got.Pages)
	}
}
