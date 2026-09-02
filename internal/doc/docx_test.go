package doc

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// docxZip builds a .docx in memory. Only the entries the decoder actually
// opens are written: committing a real Word file as testdata would be a binary
// blob nobody can regenerate, and generating one needs an office suite this
// repo does not have.
func docxZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// docxWith wraps body markup in the document part, and returns the whole file.
func docxWith(t *testing.T, body string) []byte {
	t.Helper()
	return docxZip(t, map[string]string{docxBodyPart: docxPart(body)})
}

func docxPart(body string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/></w:sectPr></w:body></w:document>`
}

func docxCore(title string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"` +
		` xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:title>` + title + `</dc:title><dc:creator>Ada</dc:creator></cp:coreProperties>`
}

func TestDocxDecode(t *testing.T) {
	tests := []struct {
		name      string
		file      []byte
		want      string
		wantTitle string
	}{
		{
			name: "heading styles become markdown headings",
			file: docxWith(t, `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Installation</w:t></w:r></w:p>
<w:p><w:r><w:t>Run the installer.</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Details</w:t></w:r></w:p>
<w:p><w:r><w:t>It takes a minute.</w:t></w:r></w:p>`),
			want: "# Installation\n\nRun the installer.\n\n## Details\n\nIt takes a minute.\n",
		},
		{
			name: "localised heading style ids are recognised",
			file: docxWith(t, `
<w:p><w:pPr><w:pStyle w:val="berschrift2"/></w:pPr><w:r><w:t>Einleitung</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Titre1"/></w:pPr><w:r><w:t>Introduction</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t>Report</w:t></w:r></w:p>`),
			want: "## Einleitung\n\n# Introduction\n\n# Report\n",
		},
		{
			name: "a non-heading style stays a paragraph",
			file: docxWith(t, `
<w:p><w:pPr><w:pStyle w:val="ListParagraph2"/></w:pPr><w:r><w:t>Not a heading.</w:t></w:r></w:p>`),
			want: "Not a heading.\n",
		},
		{
			name: "numbered paragraphs become list items at their indent depth",
			file: docxWith(t, `
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>First</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>Nested</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="3"/></w:numPr></w:pPr><w:r><w:t>Second</w:t></w:r></w:p>`),
			want: "- First\n  - Nested\n- Second\n",
		},
		{
			name: "a numbered heading stays a heading",
			file: docxWith(t, `
<w:p><w:pPr><w:pStyle w:val="Heading2"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>
<w:r><w:t>Scope</w:t></w:r></w:p>`),
			want: "## Scope\n",
		},
		{
			name: "a table becomes one markdown row per w:tr",
			file: docxWith(t, `
<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/></w:tblPr>
<w:tr><w:tc><w:p><w:r><w:t>a</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>b</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>c</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>d</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>`),
			want: "| a | b |\n| c | d |\n",
		},
		{
			name: "a multi-paragraph cell is joined into one cell",
			file: docxWith(t, `
<w:tbl><w:tr>
<w:tc><w:p><w:r><w:t>one</w:t></w:r></w:p><w:p><w:r><w:t>two</w:t></w:r></w:p></w:tc>
<w:tc><w:p><w:r><w:t>three</w:t></w:r></w:p></w:tc>
</w:tr></w:tbl>`),
			want: "| one two | three |\n",
		},
		{
			name: "xml:space preserve keeps the space, runs otherwise join without one",
			file: docxWith(t, `
<w:p><w:r><w:t xml:space="preserve">Hello </w:t></w:r><w:r><w:t>world</w:t></w:r></w:p>
<w:p><w:r><w:t>Split</w:t></w:r><w:r><w:t>Word</w:t></w:r></w:p>`),
			want: "Hello world\n\nSplitWord\n",
		},
		{
			name: "tabs and breaks stay inside the paragraph",
			file: docxWith(t, `
<w:p><w:r><w:t>a</w:t><w:tab/><w:t>b</w:t><w:br/><w:t>c</w:t></w:r></w:p>`),
			want: "a b c\n",
		},
		{
			name: "tab stops in the paragraph properties add no text",
			file: docxWith(t, `
<w:p><w:pPr><w:tabs><w:tab w:val="left" w:pos="720"/><w:tab w:val="left" w:pos="1440"/></w:tabs></w:pPr>
<w:r><w:t>Text</w:t></w:r></w:p>`),
			want: "Text\n",
		},
		{
			name: "deleted text is dropped and inserted text is kept",
			file: docxWith(t, `
<w:p><w:r><w:t xml:space="preserve">Kept </w:t></w:r>
<w:del w:id="1" w:author="Ada"><w:r><w:delText>removed </w:delText></w:r></w:del>
<w:ins w:id="2" w:author="Ada"><w:r><w:t>added</w:t></w:r></w:ins></w:p>`),
			want: "Kept added\n",
		},
		{
			name: "field codes are not prose",
			file: docxWith(t, `
<w:p><w:r><w:instrText xml:space="preserve"> PAGE \* MERGEFORMAT </w:instrText></w:r><w:r><w:t>Body text.</w:t></w:r></w:p>`),
			want: "Body text.\n",
		},
		{
			name: "german umlauts survive the decode",
			file: docxWith(t, `
<w:p><w:pPr><w:pStyle w:val="berschrift1"/></w:pPr><w:r><w:t>Größe der Schrift</w:t></w:r></w:p>
<w:p><w:r><w:t>Änderungen über Nacht.</w:t></w:r></w:p>`),
			want: "# Größe der Schrift\n\nÄnderungen über Nacht.\n",
		},
		{
			name: "content inside a content control is still prose",
			file: docxWith(t, `
<w:sdt><w:sdtContent><w:p><w:r><w:t>Inside a content control.</w:t></w:r></w:p></w:sdtContent></w:sdt>`),
			want: "Inside a content control.\n",
		},
		{
			name: "dc:title becomes the document title",
			file: docxZip(t, map[string]string{
				docxBodyPart: docxPart(`<w:p><w:r><w:t>Body.</w:t></w:r></w:p>`),
				docxCorePart: docxCore("Quarterly Report"),
			}),
			want:      "Body.\n",
			wantTitle: "Quarterly Report",
		},
		{
			name: "an empty dc:title leaves the title unset",
			file: docxZip(t, map[string]string{
				docxBodyPart: docxPart(`<w:p><w:r><w:t>Body.</w:t></w:r></w:p>`),
				docxCorePart: docxCore(""),
			}),
			want: "Body.\n",
		},
	}

	d := &docxDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Decode(tc.file)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got.Content != tc.want {
				t.Errorf("Content =\n%q\nwant\n%q", got.Content, tc.want)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Pages != 0 {
				t.Errorf("Pages = %d, want 0: a docx has no pages until it is laid out", got.Pages)
			}
		})
	}
}

func TestDocxDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		file []byte
		want errs.Code
	}{
		{
			name: "a zip without the body part is not a Word file",
			file: docxZip(t, map[string]string{
				"mimetype":               "application/epub+zip",
				"OEBPS/content.opf":      "<package/>",
				"META-INF/container.xml": "<container/>",
			}),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "bytes that are not a zip at all",
			file: []byte("This is a plain text file, not a container."),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a truncated container",
			file: docxWith(t, `<w:p><w:r><w:t>Body.</w:t></w:r></w:p>`)[:40],
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a document whose paragraphs hold no text",
			file: docxWith(t, `<w:p><w:pPr><w:pStyle w:val="Normal"/></w:pPr></w:p><w:p><w:r><w:t>   </w:t></w:r></w:p>`),
			want: errs.CodeEmptyInput,
		},
		{
			name: "a document with a body but no blocks",
			file: docxWith(t, ``),
			want: errs.CodeEmptyInput,
		},
		{
			name: "a document that is only a deleted paragraph",
			file: docxWith(t, `<w:p><w:del w:id="1"><w:r><w:delText>gone</w:delText></w:r></w:del></w:p>`),
			want: errs.CodeEmptyInput,
		},
	}

	d := &docxDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Decode(tc.file)
			if err == nil {
				t.Fatal("Decode() error = nil, want an error")
			}
			var e *errs.E
			if !errors.As(err, &e) {
				t.Fatalf("Decode() error = %v, want an *errs.E", err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %q, want %q (%v)", e.Code, tc.want, err)
			}
		})
	}
}

func TestDocxSniff(t *testing.T) {
	tests := []struct {
		name string
		file []byte
		want bool
	}{
		{
			name: "a zip carrying word/document.xml",
			file: docxWith(t, `<w:p><w:r><w:t>Body.</w:t></w:r></w:p>`),
			want: true,
		},
		{
			name: "a zip carrying something else",
			file: docxZip(t, map[string]string{"content.xml": "<office/>"}),
			want: false,
		},
		{
			name: "plain text",
			file: []byte("# A markdown file\n\nWith prose in it.\n"),
			want: false,
		},
		{
			name: "empty input",
			file: nil,
			want: false,
		},
		{
			name: "the zip magic with nothing behind it",
			file: []byte("PK\x03\x04broken"),
			want: false,
		},
	}

	d := &docxDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Sniff(tc.file); got != tc.want {
				t.Errorf("Sniff() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDocxHeadingLevel(t *testing.T) {
	tests := []struct {
		name  string
		style string
		want  int
	}{
		{name: "english heading", style: "Heading1", want: 1},
		{name: "english heading with a space", style: "heading 3", want: 3},
		{name: "german style id without the umlaut", style: "berschrift2", want: 2},
		{name: "german style name with the umlaut", style: "Überschrift 4", want: 4},
		{name: "french", style: "Titre1", want: 1},
		{name: "spanish style id with the accent stripped", style: "Ttulo3", want: 3},
		{name: "italian heading outranks the italian title", style: "Titolo2", want: 2},
		{name: "the title style is level one", style: "Title", want: 1},
		{name: "the italian title style is level one", style: "Titolo", want: 1},
		{name: "beyond markdown's six levels, still a heading", style: "Heading7", want: 7},
		{name: "a numbered non-heading style is not promoted", style: "ListParagraph2", want: 0},
		{name: "body text is not a heading", style: "BodyText", want: 0},
		{name: "no style at all", style: "", want: 0},
		{name: "a level of zero is not a heading", style: "Heading0", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := docxHeadingLevel(tc.style); got != tc.want {
				t.Errorf("docxHeadingLevel(%q) = %d, want %d", tc.style, got, tc.want)
			}
		})
	}
}

func TestDocxRegistration(t *testing.T) {
	if !Registered(FormatDocx) {
		t.Fatalf("%s is not registered", FormatDocx)
	}
	d, err := Open(FormatDocx)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", FormatDocx, err)
	}
	if d.Name() != FormatDocx {
		t.Errorf("Name() = %q, want %q", d.Name(), FormatDocx)
	}
	// The extension is what ForFile matches first, and it is what install docs
	// and error hints list; a rename here silently breaks `text lint x.docx`.
	want := map[string]bool{".docx": true, ".docm": true}
	for _, ext := range d.Extensions() {
		if !want[ext] {
			t.Errorf("unexpected extension %q", ext)
		}
		delete(want, ext)
	}
	for ext := range want {
		t.Errorf("missing extension %q", ext)
	}
}
