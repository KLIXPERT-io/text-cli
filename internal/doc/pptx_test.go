package doc

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// pptxFile is one entry of a fixture archive. Order is preserved so a test can
// pin what happens when the archive's own order disagrees with slide order.
type pptxFile struct{ name, body string }

// pptxBuild writes a .pptx in memory. Fixtures are generated rather than
// committed so every byte a test depends on is visible in the test.
func pptxBuild(t *testing.T, files ...pptxFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatalf("create %s: %v", f.name, err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatalf("write %s: %v", f.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

const pptxPresentation = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`

// pptxSlide wraps shape XML in the p:sld/p:cSld/p:spTree envelope every slide
// part has.
func pptxSlide(shapes string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"
       xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main">
  <p:cSld><p:spTree>` + shapes + `</p:spTree></p:cSld>
</p:sld>`
}

// pptxShape renders one p:sp. ph is the placeholder type ("" for none) and each
// paragraph is given as "lvl|text".
func pptxShape(ph string, paras ...string) string {
	var sb strings.Builder
	sb.WriteString(`<p:sp><p:nvSpPr><p:cNvPr id="2" name="x"/><p:nvPr>`)
	if ph != "" {
		sb.WriteString(`<p:ph type="` + ph + `"/>`)
	}
	sb.WriteString(`</p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/>`)
	for _, p := range paras {
		lvl, text, _ := strings.Cut(p, "|")
		sb.WriteString(`<a:p>`)
		if lvl != "0" {
			sb.WriteString(`<a:pPr lvl="` + lvl + `"><a:buChar char="•"/></a:pPr>`)
		}
		sb.WriteString(`<a:r><a:rPr lang="en-US"/><a:t>` + text + `</a:t></a:r></a:p>`)
	}
	sb.WriteString(`</p:txBody></p:sp>`)
	return sb.String()
}

// pptxTitleSlide is the ordinary case: a title placeholder and a bulleted body.
func pptxTitleSlide(title string, bullets ...string) string {
	shapes := pptxShape("title", "0|"+title)
	if len(bullets) > 0 {
		shapes += pptxShape("body", bullets...)
	}
	return pptxSlide(shapes)
}

func pptxDecode(t *testing.T, data []byte) *Document {
	t.Helper()
	d := &pptxDecoder{}
	got, err := d.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return got
}

func TestPPTXDecode(t *testing.T) {
	// A deck of twelve slides written to the archive in an order that is
	// neither numeric nor lexical, so a decoder that trusts either one fails.
	scrambled := []pptxFile{{"ppt/presentation.xml", pptxPresentation}}
	for _, n := range []int{10, 2, 11, 1, 12, 3, 9, 4, 8, 5, 7, 6} {
		scrambled = append(scrambled, pptxFile{
			"ppt/slides/slide" + itoa(n) + ".xml",
			pptxTitleSlide("Heading " + itoa(n)),
		})
	}
	var slideOrder []string
	for n := 1; n <= 12; n++ {
		slideOrder = append(slideOrder, "## Heading "+itoa(n))
	}

	tests := []struct {
		name      string
		files     []pptxFile
		wantOrder []string // substrings that must appear, in this order
		wantMiss  []string
		wantTitle string
		wantPages int
	}{
		{
			name: "title placeholder becomes a level-2 heading and bullets become items",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxTitleSlide("Quarterly review", "0|Revenue is up", "1|Mostly in Europe")},
			},
			wantOrder: []string{"## Quarterly review\n", "- Revenue is up\n", "  - Mostly in Europe\n"},
			wantPages: 1,
		},
		{
			name:      "slide order follows the numeric suffix, not lexical or archive order",
			files:     scrambled,
			wantOrder: slideOrder,
			wantPages: 12,
		},
		{
			name: "speaker notes are not body content",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxTitleSlide("On the slide")},
				{"ppt/notesSlides/notesSlide1.xml", pptxSlide(pptxShape("body", "0|Only for the presenter"))},
			},
			wantOrder: []string{"## On the slide"},
			wantMiss:  []string{"Only for the presenter"},
			wantPages: 1,
		},
		{
			name: "a table becomes markdown rows",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxSlide(
					pptxShape("title", "0|Numbers") +
						`<p:graphicFrame><p:nvGraphicFramePr><p:cNvPr id="4" name="t"/></p:nvGraphicFramePr>
						<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData><a:tbl>
						<a:tr><a:tc><a:txBody><a:p><a:r><a:t>Region</a:t></a:r></a:p></a:txBody></a:tc>
						      <a:tc><a:txBody><a:p><a:r><a:t>Share</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
						<a:tr><a:tc><a:txBody><a:p><a:r><a:t>Europe</a:t></a:r></a:p></a:txBody></a:tc>
						      <a:tc><a:txBody><a:p><a:r><a:t>41 percent</a:t></a:r></a:p></a:txBody></a:tc></a:tr>
						</a:tbl></a:graphicData></a:graphic></p:graphicFrame>`)},
			},
			wantOrder: []string{"## Numbers", "| Region | Share |", "| Europe | 41 percent |"},
			wantPages: 1,
		},
		{
			name: "a manual line break keeps the words apart without splitting the bullet",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxSlide(pptxBreakShape())},
			},
			wantOrder: []string{"## First line second line"},
			wantMiss:  []string{"linesecond"},
			wantPages: 1,
		},
		{
			name: "a generated field is not counted as deck text",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxSlide(
					pptxShape("title", "0|Agenda") +
						`<p:sp><p:nvSpPr><p:nvPr><p:ph type="sldNum"/></p:nvPr></p:nvSpPr><p:txBody><a:p>
						<a:fld id="{1}" type="slidenum"><a:t>7</a:t></a:fld></a:p></p:txBody></p:sp>`)},
			},
			wantOrder: []string{"## Agenda"},
			wantMiss:  []string{"7"},
			wantPages: 1,
		},
		{
			name: "the first text box is the title when no placeholder is declared",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxSlide(pptxShape("", "0|Hand-drawn deck", "0|A second thought"))},
			},
			wantOrder: []string{"## Hand-drawn deck", "- A second thought"},
			wantPages: 1,
		},
		{
			name: "a title placeholder drawn after the body still leads its slide",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxSlide(
					pptxShape("body", "0|Body drawn first") + pptxShape("title", "0|The real title"))},
			},
			wantOrder: []string{"## The real title", "- Body drawn first"},
			wantPages: 1,
		},
		{
			name: "no slide number headings are invented",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxTitleSlide("One")},
				{"ppt/slides/slide2.xml", pptxTitleSlide("Two")},
			},
			wantOrder: []string{"## One", "## Two"},
			wantMiss:  []string{"Slide 1", "Slide 2"},
			wantPages: 2,
		},
		{
			name: "dc:title sets the document title and the first heading does not",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"docProps/core.xml", `<?xml version="1.0"?><cp:coreProperties
					xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties"
					xmlns:dc="http://purl.org/dc/elements/1.1/">
					<dc:title>Board deck  Q3</dc:title><dc:creator>Someone</dc:creator></cp:coreProperties>`},
				{"ppt/slides/slide1.xml", pptxTitleSlide("Opening")},
			},
			wantOrder: []string{"## Opening"},
			wantTitle: "Board deck Q3",
			wantPages: 1,
		},
		{
			name: "an image-only slide still counts as a page",
			files: []pptxFile{
				{"ppt/presentation.xml", pptxPresentation},
				{"ppt/slides/slide1.xml", pptxTitleSlide("Only slide with words")},
				{"ppt/slides/slide2.xml", pptxSlide(`<p:pic><p:nvPicPr><p:cNvPr id="9" name="img"/></p:nvPicPr></p:pic>`)},
			},
			wantOrder: []string{"## Only slide with words"},
			wantPages: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pptxDecode(t, pptxBuild(t, tc.files...))
			at := 0
			for _, want := range tc.wantOrder {
				i := strings.Index(got.Content[at:], want)
				if i < 0 {
					t.Fatalf("missing %q after offset %d in:\n%s", want, at, got.Content)
				}
				at += i + len(want)
			}
			for _, miss := range tc.wantMiss {
				if strings.Contains(got.Content, miss) {
					t.Errorf("content should not contain %q:\n%s", miss, got.Content)
				}
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
			if got.Pages != tc.wantPages {
				t.Errorf("Pages = %d, want %d", got.Pages, tc.wantPages)
			}
		})
	}
}

// pptxBreakShape is a title paragraph split by a manual line break.
func pptxBreakShape() string {
	return `<p:sp><p:nvSpPr><p:nvPr><p:ph type="ctrTitle"/></p:nvPr></p:nvSpPr><p:txBody><a:p>
		<a:r><a:t>First line</a:t></a:r><a:br/><a:r><a:t>second line</a:t></a:r></a:p></p:txBody></p:sp>`
}

func TestPPTXDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want errs.Code
	}{
		{
			name: "plain text is not a zip container",
			data: []byte("This is a memo, not a deck.\n"),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a zip without ppt/presentation.xml is not a presentation",
			data: pptxBuild(t,
				pptxFile{"[Content_Types].xml", `<Types/>`},
				pptxFile{"word/document.xml", `<w:document xmlns:w="x"><w:body/></w:document>`}),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a deck whose slides carry no text is empty input",
			data: pptxBuild(t,
				pptxFile{"ppt/presentation.xml", pptxPresentation},
				pptxFile{"ppt/slides/slide1.xml", pptxSlide(`<p:pic><p:nvPicPr><p:cNvPr id="2" name="img"/></p:nvPicPr></p:pic>`)},
				pptxFile{"ppt/slides/slide2.xml", pptxSlide(pptxShape("body", "0|   "))}),
			want: errs.CodeEmptyInput,
		},
		{
			name: "a presentation with no slide parts is empty input",
			data: pptxBuild(t, pptxFile{"ppt/presentation.xml", pptxPresentation}),
			want: errs.CodeEmptyInput,
		},
		{
			name: "a slide that ends mid-element is reported, not half-decoded",
			data: pptxBuild(t,
				pptxFile{"ppt/presentation.xml", pptxPresentation},
				pptxFile{"ppt/slides/slide1.xml", `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>cut`}),
			want: errs.CodeInvalidArgs,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &pptxDecoder{}
			_, err := d.Decode(tc.data)
			if err == nil {
				t.Fatal("expected an error")
			}
			e, ok := err.(*errs.E)
			if !ok {
				t.Fatalf("error is %T, want *errs.E: %v", err, err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %q, want %q (%s)", e.Code, tc.want, e.Message)
			}
			// Every error this package reports names a next step; a code
			// without a hint is a dead end for the caller.
			if e.Hint == "" {
				t.Errorf("no hint on: %s", e.Message)
			}
		})
	}
}

func TestPPTXSniff(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{
			name: "a deck is claimed",
			data: pptxBuild(t,
				pptxFile{"[Content_Types].xml", `<Types/>`},
				pptxFile{"ppt/presentation.xml", pptxPresentation},
				pptxFile{"ppt/slides/slide1.xml", pptxTitleSlide("Hello")}),
			want: true,
		},
		{
			name: "a Word package shares the magic but is not claimed",
			data: pptxBuild(t,
				pptxFile{"[Content_Types].xml", `<Types/>`},
				pptxFile{"word/document.xml", `<w:document xmlns:w="x"><w:body/></w:document>`}),
			want: false,
		},
		{
			name: "prose is not claimed",
			data: []byte("# A markdown file\n\nWith a paragraph.\n"),
			want: false,
		},
		{
			name: "a truncated zip is not claimed rather than half-claimed",
			data: pptxBuild(t, pptxFile{"ppt/presentation.xml", pptxPresentation})[:20],
			want: false,
		},
		{
			name: "empty input is not claimed",
			data: nil,
			want: false,
		},
	}

	d := &pptxDecoder{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Sniff(tc.data); got != tc.want {
				t.Errorf("Sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPPTXRegistration(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "a .pptx path selects this decoder", path: "deck.pptx", want: FormatPPTX},
		{name: "a macro-enabled deck selects this decoder", path: "/tmp/Q3.PPTM", want: FormatPPTX},
		{name: "an unrelated extension does not", path: "notes.txt", want: FormatText},
	}

	if !Registered(FormatPPTX) {
		t.Fatalf("%q is not registered", FormatPPTX)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForFile(tc.path, nil); got != tc.want {
				t.Errorf("ForFile(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
