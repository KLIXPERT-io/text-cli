package doc

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// ---------------------------------------------------------------------------
// Fixtures
//
// The PDFs below are assembled byte by byte rather than checked in, for the
// same reason the sibling decoders build their zips in memory: a binary fixture
// cannot be reviewed, and the thing under test here is geometry, so a test
// needs to state coordinates rather than hide them in a file someone exported
// once.
// ---------------------------------------------------------------------------

// pdfDraw is one Tj: a string placed at a point, in a size.
type pdfDraw struct {
	x, y, size float64
	text       string
}

// pdfSpec describes a whole document.
type pdfSpec struct {
	pages   [][]pdfDraw
	title   string
	winansi bool // encode the text as WinAnsi rather than leaving it raw
	widths  bool // give the font a Widths array, so glyph advances resolve
}

// pdfFixture assembles a syntactically valid PDF.
func pdfFixture(t *testing.T, spec pdfSpec) []byte {
	t.Helper()

	n := len(spec.pages)
	const fontObj = 3
	pageObj := func(i int) int { return 4 + i }
	streamObj := func(i int) int { return 4 + n + i }

	font := "<</Type/Font/Subtype/Type1/BaseFont/Helvetica"
	if spec.winansi {
		font += "/Encoding/WinAnsiEncoding"
	}
	if spec.widths {
		// A uniform half-em advance. What matters is only that Widths exists,
		// because that is what makes the reader track the pen per glyph and so
		// exercises the other half of pdfRun.
		font += "/FirstChar 32/LastChar 126/Widths[" + strings.TrimSpace(strings.Repeat("500 ", 95)) + "]"
	}
	font += ">>"

	kids := make([]string, 0, n)
	for i := range spec.pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObj(i)))
	}

	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", strings.Join(kids, " "), n),
		font,
	}
	for i := range spec.pages {
		objs = append(objs, fmt.Sprintf(
			"<</Type/Page/Parent 2 0 R/Resources<</Font<</F1 %d 0 R>>>>/Contents %d 0 R>>",
			fontObj, streamObj(i)))
	}
	for _, draws := range spec.pages {
		var c bytes.Buffer
		c.WriteString("BT\n")
		for _, d := range draws {
			fmt.Fprintf(&c, "/F1 %g Tf\n1 0 0 1 %g %g Tm\n(%s) Tj\n",
				d.size, d.x, d.y, pdfEscape(d.text, spec.winansi))
		}
		c.WriteString("ET\n")
		objs = append(objs, fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", c.Len(), c.String()))
	}

	trailer := ""
	if spec.title != "" {
		objs = append(objs, fmt.Sprintf("<</Title(%s)>>", pdfEscape(spec.title, spec.winansi)))
		trailer = fmt.Sprintf("/Info %d 0 R", len(objs))
	}
	return pdfAssemble(objs, trailer)
}

// pdfAssemble writes the objects out with a correct cross-reference table.
func pdfAssemble(objs []string, trailerExtra string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")

	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	// Each entry is exactly twenty bytes; the reader counts on it.
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<</Size %d/Root 1 0 R%s>>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, trailerExtra, start)
	return buf.Bytes()
}

// pdfEscape renders a Go string as a PDF literal string, optionally down to
// WinAnsi bytes so an umlaut arrives as the single byte the encoding expects.
func pdfEscape(s string, winansi bool) string {
	var b bytes.Buffer
	for _, r := range s {
		if winansi {
			if c, ok := pdfWinAnsi[r]; ok {
				b.WriteByte(c)
				continue
			}
		}
		switch r {
		case '\\', '(', ')':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pdfWinAnsi covers only the characters the tests below actually need.
var pdfWinAnsi = map[rune]byte{
	'ä': 0xE4, 'ö': 0xF6, 'ü': 0xFC, 'ß': 0xDF,
	'Ä': 0xC4, 'Ö': 0xD6, 'Ü': 0xDC, '§': 0xA7, '·': 0xB7,
	'•': 0x95,
}

// pdfContent decodes a spec and returns the markdown.
func pdfContent(t *testing.T, spec pdfSpec) string {
	t.Helper()
	d, err := pdfDecoder{}.Decode(pdfFixture(t, spec))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return d.Content
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

func TestPDFDecode(t *testing.T) {
	tests := []struct {
		name string
		spec pdfSpec
		want string
	}{
		{
			name: "lines one leading apart are one paragraph",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Der erste Teil des Satzes"},
				{50, 685, 10, "und der zweite Teil davon."},
			}}},
			want: "Der erste Teil des Satzes und der zweite Teil davon.\n",
		},
		{
			name: "a wide vertical gap ends the paragraph",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Erster Absatz, erste Zeile"},
				{50, 685, 10, "und dessen zweite Zeile."},
				{50, 620, 10, "Ein zweiter Absatz."},
			}}},
			want: "Erster Absatz, erste Zeile und dessen zweite Zeile.\n\nEin zweiter Absatz.\n",
		},
		{
			name: "a larger font is a heading",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 20, "Die Ueberschrift"},
				{50, 660, 10, "Der Fliesstext dieses Abschnitts, der die Groesse vorgibt."},
				{50, 645, 10, "Denn er traegt die meiste Schrift auf dieser Seite."},
			}}},
			want: "# Die Ueberschrift\n\nDer Fliesstext dieses Abschnitts, der die Groesse vorgibt. Denn er traegt die meiste Schrift auf dieser Seite.\n",
		},
		{
			name: "heading levels rank by size, not by absolute points",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 20, "Titel"},
				{50, 660, 14, "Abschnitt"},
				{50, 620, 10, "Der Fliesstext dieses Abschnitts, der die Groesse vorgibt."},
				{50, 605, 10, "Denn er traegt die meiste Schrift auf dieser Seite."},
			}}},
			want: "# Titel\n\n## Abschnitt\n\nDer Fliesstext dieses Abschnitts, der die Groesse vorgibt. Denn er traegt die meiste Schrift auf dieser Seite.\n",
		},
		{
			name: "a column gap on one line becomes a space",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 9, "Abs. 3"},
				{200, 700, 9, "Mittleres Unternehmen"},
				{380, 700, 9, "Mindestens 50 Mitarbeiter."},
			}}},
			want: "Abs. 3 Mittleres Unternehmen Mindestens 50 Mitarbeiter.\n",
		},
		{
			name: "runs that continue a word are not split by a space",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Vertrags"},
				{95, 700, 10, "recht"},
			}}},
			want: "Vertragsrecht\n",
		},
		{
			name: "a hyphenated line break is rejoined",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Der Beruf-"},
				{50, 685, 10, "skraftfahrer faehrt."},
			}}},
			want: "Der Berufskraftfahrer faehrt.\n",
		},
		{
			name: "a suspended hyphen keeps its hyphen",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Brief-"},
				{50, 685, 10, "und Paketlogistik."},
			}}},
			want: "Brief- und Paketlogistik.\n",
		},
		{
			name: "a bullet becomes a markdown item",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "• Erster Punkt"},
				{50, 685, 10, "• Zweiter Punkt"},
			}}, winansi: true},
			want: "- Erster Punkt\n- Zweiter Punkt\n",
		},
		{
			name: "a number and a dot become an ordered item",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "1. Erster Punkt"},
				{50, 685, 10, "2. Zweiter Punkt"},
			}}},
			want: "1. Erster Punkt\n1. Zweiter Punkt\n",
		},
		{
			name: "a decimal number is not an ordered item",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "1.5 Millionen Euro sind faellig."},
			}}},
			want: "1.5 Millionen Euro sind faellig.\n",
		},
		{
			name: "lines drawn bottom first are read top down",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 100, 10, "Der letzte Absatz."},
				{50, 700, 10, "Der erste Absatz."},
			}}},
			want: "Der erste Absatz.\n\nDer letzte Absatz.\n",
		},
		{
			name: "umlauts survive the decode",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Größe, Übermaß und Prüfung nach § 25."},
			}}, winansi: true},
			want: "Größe, Übermaß und Prüfung nach § 25.\n",
		},
		{
			name: "a sentence that runs over a page break stays one paragraph",
			spec: pdfSpec{pages: [][]pdfDraw{
				{{50, 700, 10, "Der Satz beginnt hier und"}},
				{{50, 700, 10, "laeuft auf der naechsten Seite weiter."}},
			}},
			want: "Der Satz beginnt hier und laeuft auf der naechsten Seite weiter.\n",
		},
		{
			name: "a finished sentence does not run over a page break",
			spec: pdfSpec{pages: [][]pdfDraw{
				{{50, 700, 10, "Der Satz endet hier."}},
				{{50, 700, 10, "Ein neuer Absatz beginnt."}},
			}},
			want: "Der Satz endet hier.\n\nEin neuer Absatz beginnt.\n",
		},
		{
			name: "resolvable glyph widths do not fabricate spaces",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Ein ganz gewoehnlicher Satz mit Woertern."},
			}}, widths: true},
			want: "Ein ganz gewoehnlicher Satz mit Woertern.\n",
		},
		{
			name: "resolvable glyph widths still space a column gap",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Links"},
				{300, 700, 10, "Rechts"},
			}}, widths: true},
			want: "Links Rechts\n",
		},
		{
			// The gap a TeX-produced PDF leaves between two words: it encodes no
			// space character at all, so a word break is nothing but a quarter of
			// an em of empty space. Measured against the old 0.6em tolerance the
			// whole line arrived as one word, and every score computed over it was
			// meaningless. 500/1000 widths put the pen at 75 after "Links".
			name: "a word-sized gap is a space, not a compound word",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Links"},
				{77.5, 700, 10, "Rechts"},
			}}, widths: true},
			want: "Links Rechts\n",
		},
		{
			// The other side of the same threshold: a kern is an order of
			// magnitude smaller than a space and must not split a word.
			name: "a kern-sized gap does not split a word",
			spec: pdfSpec{pages: [][]pdfDraw{{
				{50, 700, 10, "Links"},
				{75.5, 700, 10, "seitig"},
			}}, widths: true},
			want: "Linksseitig\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfContent(t, tc.spec); got != tc.want {
				t.Errorf("content =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestPDFDecodeMetadata(t *testing.T) {
	spec := pdfSpec{
		title: "Der Leitfaden",
		pages: [][]pdfDraw{
			{{50, 700, 10, "Seite eins."}},
			{{50, 700, 10, "Seite zwei."}},
			{{50, 700, 10, "Seite drei."}},
		},
	}
	d, err := pdfDecoder{}.Decode(pdfFixture(t, spec))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if d.Title != "Der Leitfaden" {
		t.Errorf("Title = %q, want %q", d.Title, "Der Leitfaden")
	}
	// A PDF is the one format in this package that really is paginated, so
	// unlike its siblings it reports a page count rather than 0.
	if d.Pages != 3 {
		t.Errorf("Pages = %d, want 3", d.Pages)
	}
	if got := Decode; got == nil {
		t.Fatal("unreachable")
	}
	full, err := Decode("auto", "leitfaden.pdf", pdfFixture(t, spec))
	if err != nil {
		t.Fatalf("Decode(auto): %v", err)
	}
	if full.Format != FormatPDF {
		t.Errorf("Format = %q, want %q", full.Format, FormatPDF)
	}
}

// ---------------------------------------------------------------------------
// Running headers and footers
// ---------------------------------------------------------------------------

func TestPDFRunningFurniture(t *testing.T) {
	// Five lines a page, so the margin band is a real band rather than the
	// whole page: the header and the first body line at the top, the last body
	// line and the footer at the foot.
	page := func(n int, body ...string) []pdfDraw {
		draws := []pdfDraw{{50, 800, 8, "ACME REPORT 2026"}}
		y := 700.0
		for _, b := range body {
			draws = append(draws, pdfDraw{50, y, 10, b})
			y -= 15
		}
		return append(draws, pdfDraw{50, 40, 8, fmt.Sprintf("acme.eu %d / 4", n)})
	}
	spec := pdfSpec{pages: [][]pdfDraw{
		page(1, "Der erste Abschnitt beginnt.", "Er hat eine zweite Zeile.", "Und eine dritte Zeile hier."),
		page(2, "Der zweite Abschnitt folgt.", "Auch er hat zwei Zeilen.", "Sowie eine dritte Zeile."),
		page(3, "Der dritte Abschnitt kommt.", "Mit einer weiteren Zeile.", "Und noch einer Zeile dazu."),
		page(4, "Der vierte Abschnitt endet.", "Die letzte Zeile steht hier.", "Ganz zuletzt diese Zeile."),
	}}

	got := pdfContent(t, spec)
	for _, gone := range []string{"ACME REPORT 2026", "acme.eu"} {
		if strings.Contains(got, gone) {
			t.Errorf("running furniture %q survived:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "Der dritte Abschnitt kommt.") {
		t.Errorf("body text was dropped with the furniture:\n%s", got)
	}
}

func TestPDFShortDocumentKeepsItsMargins(t *testing.T) {
	// Two pages is not evidence that anything repeats on purpose, so nothing is
	// dropped. Deleting a line that appears twice in a two-page document would
	// be guessing.
	spec := pdfSpec{pages: [][]pdfDraw{
		{{50, 800, 8, "ACME REPORT"}, {50, 700, 10, "Der erste Abschnitt."}},
		{{50, 800, 8, "ACME REPORT"}, {50, 700, 10, "Der zweite Abschnitt."}},
	}}
	if got := pdfContent(t, spec); !strings.Contains(got, "ACME REPORT") {
		t.Errorf("a repeat over two pages was treated as furniture:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

func TestPDFDecodeErrors(t *testing.T) {
	valid := pdfFixture(t, pdfSpec{pages: [][]pdfDraw{{{50, 700, 10, "Ein Satz."}}}})

	tests := []struct {
		name string
		data []byte
		want errs.Code
	}{
		{
			name: "prose is not a PDF",
			data: []byte("Dies ist eine Notiz, kein PDF.\n"),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "nothing is not a PDF",
			data: nil,
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a truncated PDF is reported, not half-decoded",
			data: valid[:len(valid)/2],
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a PDF with no pages is reported",
			data: pdfAssemble([]string{
				"<</Type/Catalog/Pages 2 0 R>>",
				"<</Type/Pages/Kids[]/Count 0>>",
			}, ""),
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a page that draws no text is empty input",
			data: pdfFixture(t, pdfSpec{pages: [][]pdfDraw{{}}}),
			want: errs.CodeEmptyInput,
		},
		{
			name: "a page of whitespace is empty input",
			data: pdfFixture(t, pdfSpec{pages: [][]pdfDraw{{{50, 700, 10, "   "}}}}),
			want: errs.CodeEmptyInput,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pdfDecoder{}.Decode(tc.data)
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
			if e.Hint == "" {
				t.Errorf("no hint on: %s", e.Message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func TestPDFSniff(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"a PDF header", []byte("%PDF-1.4\n1 0 obj"), true},
		{"a PDF 2.0 header", []byte("%PDF-2.0\n1 0 obj"), true},
		{"a zip container", []byte{'P', 'K', 0x03, 0x04, 0, 0}, false},
		{"prose", []byte("Dies ist ein Satz."), false},
		{"a header that is only most of one", []byte("%PDF"), false},
		{"nothing", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (pdfDecoder{}).Sniff(tc.data); got != tc.want {
				t.Errorf("Sniff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPDFRegistration(t *testing.T) {
	if !Registered(FormatPDF) {
		t.Fatalf("%q is not registered", FormatPDF)
	}
	body := pdfFixture(t, pdfSpec{pages: [][]pdfDraw{{{50, 700, 10, "Ein Satz."}}}})

	tests := []struct {
		name string
		path string
		data []byte
		want string
	}{
		{"by extension", "bericht.pdf", nil, FormatPDF},
		{"by upper-case extension", "BERICHT.PDF", nil, FormatPDF},
		{"by sniff when there is no name", "", body, FormatPDF},
		{"a text file is left alone", "notiz.txt", nil, FormatText},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForFile(tc.path, tc.data); got != tc.want {
				t.Errorf("ForFile = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPDF20IsReadable pins the version rewrite end to end: a PDF 2.0 file is a
// file this CLI must read, and the reader underneath only accepts 1.x.
func TestPDF20IsReadable(t *testing.T) {
	data := pdfFixture(t, pdfSpec{pages: [][]pdfDraw{{{50, 700, 10, "Ein Satz im neuen Format."}}}})
	data[5], data[6], data[7] = '2', '.', '0'

	d, err := pdfDecoder{}.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := "Ein Satz im neuen Format.\n"; d.Content != want {
		t.Errorf("content = %q, want %q", d.Content, want)
	}
}

// ---------------------------------------------------------------------------
// Units
// ---------------------------------------------------------------------------

func TestPDFNormalizeVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a 2.0 header is rewritten", "%PDF-2.0\nrest", "%PDF-1.7\nrest"},
		{"a 1.x header is untouched", "%PDF-1.4\nrest", "%PDF-1.4\nrest"},
		{"a non-PDF is untouched", "hello there", "hello there"},
		{"a short input is untouched", "%PDF-", "%PDF-"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(pdfNormalizeVersion([]byte(tc.in)))
			if got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
			// Length has to be preserved or every xref offset moves.
			if len(got) != len(tc.in) {
				t.Errorf("length changed: %d -> %d", len(tc.in), len(got))
			}
		})
	}
}

func TestPDFJoin(t *testing.T) {
	tests := []struct {
		name       string
		text, next string
		want       string
	}{
		{"a hyphenated word is rejoined", "Der Beruf-", "skraftfahrer faehrt", "Der Berufskraftfahrer faehrt"},
		{"a suspended hyphen is kept before und", "Brief-", "und Paketlogistik", "Brief- und Paketlogistik"},
		{"a suspended hyphen is kept before oder", "Ein-", "oder Ausgang", "Ein- oder Ausgang"},
		{"a suspended hyphen is kept before sowie", "Netz-", "sowie Systeme", "Netz- sowie Systeme"},
		{"a typographic hyphen is rejoined too", "Ver‐", "trag gilt", "Vertrag gilt"},
		{"an upper-case continuation is not a hyphenation", "Nord-", "Sued-Achse", "Nord- Sued-Achse"},
		{"a hyphen after a digit is not a hyphenation", "2026-", "regeln", "2026- regeln"},
		{"an ordinary wrap gets a space", "Der Satz geht", "hier weiter", "Der Satz geht hier weiter"},
		{"an empty accumulator takes the line", "", "Erste Zeile", "Erste Zeile"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfJoin(tc.text, tc.next); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPDFMarker(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantMarker  bool
		wantOrdered bool
		wantRest    string
	}{
		{"a bullet", "• Erster Punkt", true, false, "Erster Punkt"},
		{"an en dash used as a bullet", "– Erster Punkt", true, false, "Erster Punkt"},
		{"a number and a dot", "1. Erster Punkt", true, true, "Erster Punkt"},
		{"a number and a paren", "2) Zweiter Punkt", true, true, "Zweiter Punkt"},
		{"a bullet with no space is not a marker", "•Text", false, false, "•Text"},
		{"a decimal is not a marker", "1.5 Millionen", false, false, "1.5 Millionen"},
		{"a date is not a marker", "15.07.2026 war der Tag", false, false, "15.07.2026 war der Tag"},
		{"prose is not a marker", "Der Satz beginnt.", false, false, "Der Satz beginnt."},
		{"a single character is not a marker", "•", false, false, "•"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			marker, ordered, rest := pdfMarker(tc.in)
			if marker != tc.wantMarker || ordered != tc.wantOrdered || rest != tc.wantRest {
				t.Errorf("= (%v, %v, %q), want (%v, %v, %q)",
					marker, ordered, rest, tc.wantMarker, tc.wantOrdered, tc.wantRest)
			}
		})
	}
}

func TestPDFFurnitureKey(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"page numbers are masked", "acme.eu 4 / 13", "acme.eu 5 / 13", true},
		{"a run of digits masks to one mark", "Seite 9", "Seite 128", true},
		{"case is ignored", "ACME REPORT", "Acme Report", true},
		{"different words stay different", "Kapitel eins", "Kapitel zwei", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfFurnitureKey(tc.a) == pdfFurnitureKey(tc.b); got != tc.same {
				t.Errorf("same = %v, want %v (%q vs %q)",
					got, tc.same, pdfFurnitureKey(tc.a), pdfFurnitureKey(tc.b))
			}
		})
	}
}

func TestPDFGlyphText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary text passes through", "a", "a"},
		{"an unmapped glyph is dropped", "�", ""},
		{"an unmapped glyph is dropped from a run", "Abs�", "Abs"},
		{"nothing stays nothing", "", ""},
		// A PDF records what was printed, and a typesetter prints
		// "specification" with one ﬁ glyph. Left alone it is a character the
		// document does not contain and a vowel the syllable counter cannot see.
		{"an fi ligature expands", "speciﬁcation", "specification"},
		{"an fl ligature expands", "ﬂag", "flag"},
		{"a three-letter ligature expands", "oﬃce", "office"},
		{"an ff ligature expands", "Schaﬀung", "Schaffung"},
		{"a ligature beside an unmapped glyph", "ﬁle�", "file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pdfGlyphText(tc.in); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The declared page count
// ---------------------------------------------------------------------------

// pdfWithCount builds a one-page PDF that declares a different page count, and
// pads it to a chosen size.
//
// The count is a number in the file, not a fact about it: NumPage returns
// /Root/Pages/Count verbatim, and a corrupt or crafted one sends the decode loop
// round as many times as it says. The padding is how a test says how much file
// there is to back the claim.
func pdfWithCount(t *testing.T, count, pad int) []byte {
	t.Helper()
	stream := "BT\n/F1 10 Tf\n1 0 0 1 50 700 Tm\n(Ein Satz.) Tj\nET\n"
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		fmt.Sprintf("<</Type/Pages/Kids[4 0 R]/Count %d>>", count),
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
		"<</Type/Page/Parent 2 0 R/Resources<</Font<</F1 3 0 R>>>>/Contents 5 0 R>>",
		fmt.Sprintf("<</Length %d>>\nstream\n%s\nendstream", len(stream), stream),
	}
	if pad > 0 {
		objs = append(objs, "<</Pad("+strings.Repeat("x", pad)+")>>")
	}
	return pdfAssemble(objs, "")
}

// A page count the file is too small to back is refused, and quickly.
//
// Believing it is not a slow path, it is a hang: two billion phantom pages is
// two billion walks of a page tree that holds one, with no allocation, no panic
// and no output — a CLI that has stopped responding for a 482-byte file. The
// deadline is what this test is really asserting; the error code is the shape
// the refusal has to take.
func TestPDFDeclaredPageCount(t *testing.T) {
	tests := []struct {
		name    string
		count   int
		pad     int
		wantErr bool
	}{
		{
			name:  "an honest count decodes",
			count: 1,
		},
		{
			name:  "a count the file could just about back is attempted, not refused",
			count: 200,
			pad:   64 << 10,
		},
		{
			name:    "a tiny file declaring two billion pages is refused",
			count:   2_000_000_000,
			wantErr: true,
		},
		{
			name:    "a count just past what the bytes can hold is refused",
			count:   1 << 20,
			pad:     1 << 10,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := pdfWithCount(t, tc.count, tc.pad)

			done := make(chan struct{})
			var doc *Document
			var err error
			go func() {
				defer close(done)
				doc, err = pdfDecoder{}.Decode(data)
			}()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("Decode did not return for a %d-byte PDF declaring %d pages", len(data), tc.count)
			}

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if !strings.Contains(doc.Content, "Ein Satz.") {
					t.Fatalf("content = %q, want the page's text", doc.Content)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			e, ok := err.(*errs.E)
			if !ok {
				t.Fatalf("error is %T, want *errs.E: %v", err, err)
			}
			if e.Code != errs.CodeInvalidArgs {
				t.Errorf("code = %q, want %q (%s)", e.Code, errs.CodeInvalidArgs, e.Message)
			}
			if e.Hint == "" {
				t.Errorf("no hint on: %s", e.Message)
			}
		})
	}
}

// A count within the structural bound but past the end of the page tree stops
// at the run of absent pages instead of probing every declared index.
func TestPDFStopsAtTheEndOfThePageTree(t *testing.T) {
	// Padded far past what a million pages would need, so the bound in
	// pdfNumPages lets the count through and only pdfMissingRun can end the read.
	data := pdfWithCount(t, 1_000_000, 9<<20)

	done := make(chan struct{})
	var doc *Document
	var err error
	go func() {
		defer close(done)
		doc, err = pdfDecoder{}.Decode(data)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Decode did not return for a PDF declaring a million pages it does not have")
	}
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !strings.Contains(doc.Content, "Ein Satz.") {
		t.Fatalf("content = %q, want the one real page's text", doc.Content)
	}
}
