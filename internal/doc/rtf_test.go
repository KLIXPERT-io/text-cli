package doc

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// RTF is plain text, so every fixture here is a readable literal. They are
// written the way a real writer emits them — control words running into their
// arguments, no space where the delimiter was eaten — because the spacing rules
// are half of what this parser has to get right.

func TestRTFDecode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "headings, paragraphs and bullets survive as markdown",
			in: `{\rtf1\ansi\ansicpg1252\deff0
{\fonttbl{\f0\froman Times New Roman;}{\f1\fswiss Arial;}}
{\colortbl;\red0\green0\blue0;}
{\stylesheet{\s1 heading 1;}}
{\info{\title Mein Bericht}{\author Florian Narr}}
{\*\generator Riched20 10.0.19041;}
\pard\outlinelevel0 Einleitung\par
\pard Der Bericht ist {\b kurz} und lesbar.\par
\pard{\listtext\f1\'b7\tab}Erster Punkt\par
\pard{\listtext\f1\'b7\tab}Zweiter Punkt\par
}`,
			want: "# Einleitung\n\nDer Bericht ist kurz und lesbar.\n\n- Erster Punkt\n- Zweiter Punkt\n",
		},
		{
			name: "outline levels below the first become deeper headings",
			in:   `{\rtf1\pard\outlinelevel2 Details\par\pard Text.\par}`,
			want: "### Details\n\nText.\n",
		},
		{
			name: "par separates paragraphs",
			in:   `{\rtf1 Eins.\par Zwei.\par Drei.\par}`,
			want: "Eins.\n\nZwei.\n\nDrei.\n",
		},
		{
			name: "line ends the block so the lines stay separate sentences",
			in:   `{\rtf1 Zeile eins\line Zeile zwei\par}`,
			want: "Zeile eins\n\nZeile zwei\n",
		},
		{
			name: "sect ends a section",
			in:   `{\rtf1 Kapitel eins.\sect Kapitel zwei.\par}`,
			want: "Kapitel eins.\n\nKapitel zwei.\n",
		},
		{
			name: "hex escapes decode through cp1252",
			in:   `{\rtf1\ansi\ansicpg1252 M\'e4dchen an der Stra\'dfe \'96 gro\'df.\par}`,
			want: "Mädchen an der Straße – groß.\n",
		},
		{
			name: "unicode escapes decode and the substitute character is dropped",
			in:   `{\rtf1 Gr\u252?\u223?e aus M\u252?nchen.\par}`,
			want: "Grüße aus München.\n",
		},
		{
			name: "uc sets how many substitute characters follow",
			in:   `{\rtf1\uc2 \u228\'3f\'3fhnlich und \u252\'3f\'3fblich.\par}`,
			want: "ähnlich und üblich.\n",
		},
		{
			name: "uc0 means no substitute follows",
			in:   `{\rtf1\uc0\u228hnlich\par}`,
			want: "ähnlich\n",
		},
		{
			name: "a negative unicode value wraps to 16 bits",
			in:   `{\rtf1 Wir \u-1279?nden es.\par}`,
			want: "Wir ﬁnden es.\n",
		},
		{
			name: "two negative unicode values rejoin as a surrogate pair",
			in:   `{\rtf1 Hallo \u-10179?\u-8704? Welt\par}`,
			want: "Hallo \U0001F600 Welt\n",
		},
		{
			name: "the font, colour, style and info tables are not prose",
			in: `{\rtf1\ansi{\fonttbl{\f0\froman Times New Roman;}{\f1\fnil Wingdings;}}` +
				`{\colortbl;\red255\green0\blue0;}` +
				`{\stylesheet{\s0 Standard;}{\s1 berschrift 1;}}` +
				`{\info{\title Geheimer Titel}{\author Florian Narr}{\company KLIXPERT}}` +
				`{\*\generator Riched20 10.0.19041;}` +
				`Nur dieser Satz.\par}`,
			want: "Nur dieser Satz.\n",
		},
		{
			name: "an unknown starred destination is skipped with everything nested in it",
			in:   `{\rtf1 A{\*\weirddest Geheim{\b noch mehr}}B\par}`,
			want: "AB\n",
		},
		{
			name: "field instructions are skipped and the field result is kept",
			in:   `{\rtf1 Siehe {\field{\*\fldinst HYPERLINK "https://example.com" }{\fldrslt die Seite}}.\par}`,
			want: "Siehe die Seite.\n",
		},
		{
			name: "headers, footers and footnotes are not body prose",
			in: `{\rtf1{\header Kopfzeile mit Seitenzahl}{\footer Fu\'dfzeile}` +
				`Der Text.{\footnote Eine Anmerkung.}\par}`,
			want: "Der Text.\n",
		},
		{
			name: "picture hex never reaches the prose",
			in:   `{\rtf1 {\pict\pngblip\picw100 89504e470d0a1a0a0000000d49484452}Nach dem Bild.\par}`,
			want: "Nach dem Bild.\n",
		},
		{
			name: "bin skips raw bytes by count so braces inside a picture cannot break the groups",
			in:   `{\rtf1 {\pict\wmetafile8\bin6 }X}Y}Z} Sichtbar.\par}`,
			want: "Sichtbar.\n",
		},
		{
			name: "control symbols are literal characters and do not eat the next space",
			in:   `{\rtf1 Kosten \{100\} \\ netto\par}`,
			want: "Kosten {100} \\ netto\n",
		},
		{
			name: "tab becomes a space and a control word eats exactly one delimiter",
			in:   `{\rtf1 Spalte\tab Wert\par}`,
			want: "Spalte Wert\n",
		},
		{
			name: "typographic control words decode",
			in:   `{\rtf1 \ldblquote Ja\rdblquote \emdash sagte er\endash kurz.\par}`,
			want: "“Ja”—sagte er–kurz.\n",
		},
		{
			name: "table cells become a markdown row",
			in:   `{\rtf1\trowd\cellx4000\cellx8000\pard\intbl Name\cell\pard\intbl Wert\cell\row\pard\par}`,
			want: "| Name | Wert |\n",
		},
		{
			name: "a paragraph without a closing par is still emitted",
			in:   `{\rtf1 Kein Absatzende`,
			want: "Kein Absatzende\n",
		},
		{
			name: "a missing closing brace does not hang or lose the text",
			in:   `{\rtf1 {\b Fett\par {\i Kursiv\par`,
			want: "Fett\n\nKursiv\n",
		},
		{
			name: "stray closing braces return to the body instead of dropping the rest",
			in:   `{\rtf1 Hallo\par}}} Welt\par`,
			want: "Hallo\n\nWelt\n",
		},
		{
			name: "a leading BOM and whitespace are tolerated",
			in:   "\ufeff\n  {\\rtf1 Trotzdem lesbar.\\par}",
			want: "Trotzdem lesbar.\n",
		},
		{
			name: "raw UTF-8 in the stream is passed through unchanged",
			in:   `{\rtf1 Schöne Grüße.\par}`,
			want: "Schöne Grüße.\n",
		},
	}

	var d rtfDecoder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Decode([]byte(tc.in))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got.Content != tc.want {
				t.Errorf("Content =\n%q\nwant\n%q", got.Content, tc.want)
			}
			if got.Format != FormatRTF {
				t.Errorf("Format = %q, want %q", got.Format, FormatRTF)
			}
			if got.Pages != 0 {
				t.Errorf("Pages = %d, want 0: RTF does not paginate", got.Pages)
			}
		})
	}
}

func TestRTFTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "the info title becomes the document title",
			in:   `{\rtf1{\info{\title Quartalsbericht}{\author Florian Narr}}Text.\par}`,
			want: "Quartalsbericht",
		},
		{
			name: "a document without an info block has no title",
			in:   `{\rtf1 Text.\par}`,
			want: "",
		},
		{
			name: "a title outside the info block is not metadata",
			in:   `{\rtf1{\title Nicht das hier}Text.\par}`,
			want: "",
		},
	}

	var d rtfDecoder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Decode([]byte(tc.in))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if got.Title != tc.want {
				t.Errorf("Title = %q, want %q", got.Title, tc.want)
			}
			if strings.Contains(got.Content, "Quartalsbericht") || strings.Contains(got.Content, "Florian") {
				t.Errorf("info metadata leaked into Content: %q", got.Content)
			}
		})
	}
}

func TestRTFDecodeErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want errs.Code
	}{
		{
			name: "plain prose is not an RTF file",
			in:   "Guten Morgen, Welt.",
			want: errs.CodeInvalidArgs,
		},
		{
			name: "another brace-and-backslash format is not an RTF file",
			in:   `{\ansi Sieht fast so aus.}`,
			want: errs.CodeInvalidArgs,
		},
		{
			name: "a zip container is not an RTF file",
			in:   "PK\x03\x04rest of the archive",
			want: errs.CodeInvalidArgs,
		},
		{
			name: "empty input is not an RTF file",
			in:   "",
			want: errs.CodeInvalidArgs,
		},
		{
			name: "an RTF holding only a font table has no text",
			in:   `{\rtf1\ansi\deff0{\fonttbl{\f0\froman Times New Roman;}{\f1\fswiss Arial;}}}`,
			want: errs.CodeEmptyInput,
		},
		{
			name: "an RTF holding only a picture has no text",
			in:   `{\rtf1\ansi{\pict\pngblip 89504e470d0a1a0a}}`,
			want: errs.CodeEmptyInput,
		},
	}

	var d rtfDecoder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.Decode([]byte(tc.in))
			if err == nil {
				t.Fatalf("Decode() = %+v, want error %s", got, tc.want)
			}
			e, ok := err.(*errs.E)
			if !ok {
				t.Fatalf("Decode() error is %T, want *errs.E", err)
			}
			if e.Code != tc.want {
				t.Errorf("code = %s, want %s", e.Code, tc.want)
			}
			if e.Hint == "" {
				t.Error("error carries no hint")
			}
		})
	}
}

func TestRTFSniff(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"the magic alone", `{\rtf`, true},
		{"a real document header", `{\rtf1\ansi\ansicpg1252\deff0`, true},
		{"a future version digit", `{\rtf2\ansi`, true},
		{"leading whitespace", "\n\n   {\\rtf1\\ansi", true},
		{"a UTF-8 BOM", "\ufeff{\\rtf1\\ansi", true},
		{"another control word", `{\ansi\rtf1`, false},
		{"a truncated magic", `{\rt`, false},
		{"a group but no rtf", `{\*\generator}`, false},
		{"plain prose", "Guten Morgen.", false},
		{"markdown", "# Überschrift\n\nText.", false},
		{"a zip container", "PK\x03\x04", false},
		{"nothing at all", "", false},
	}

	var d rtfDecoder
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.Sniff([]byte(tc.in)); got != tc.want {
				t.Errorf("Sniff(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRTFRegistration(t *testing.T) {
	if !Registered(FormatRTF) {
		t.Fatalf("%q is not registered", FormatRTF)
	}
	d, err := Open(FormatRTF)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", FormatRTF, err)
	}
	if d.Name() != FormatRTF {
		t.Errorf("Name() = %q, want %q", d.Name(), FormatRTF)
	}
	if got := d.Extensions(); len(got) != 1 || got[0] != ".rtf" {
		t.Errorf("Extensions() = %v, want [.rtf]", got)
	}
	if got := ForFile("bericht.rtf", nil); got != FormatRTF {
		t.Errorf("ForFile(bericht.rtf) = %q, want %q", got, FormatRTF)
	}
}
