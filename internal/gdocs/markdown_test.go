package gdocs

import (
	"testing"

	docs "google.golang.org/api/docs/v1"
)

// para builds a paragraph of plain text runs, each ending the way the API ends
// them: the last run of a paragraph carries the newline.
func para(style string, runs ...*docs.TextRun) *docs.StructuralElement {
	elements := make([]*docs.ParagraphElement, 0, len(runs))
	for _, r := range runs {
		elements = append(elements, &docs.ParagraphElement{TextRun: r})
	}
	p := &docs.Paragraph{Elements: elements}
	if style != "" {
		p.ParagraphStyle = &docs.ParagraphStyle{NamedStyleType: style}
	}
	return &docs.StructuralElement{Paragraph: p}
}

func run(content string) *docs.TextRun { return &docs.TextRun{Content: content} }

func styledRun(content string, style *docs.TextStyle) *docs.TextRun {
	return &docs.TextRun{Content: content, TextStyle: style}
}

func bullet(listID string, nesting int64, content string) *docs.StructuralElement {
	el := para("NORMAL_TEXT", run(content))
	el.Paragraph.Bullet = &docs.Bullet{ListId: listID, NestingLevel: nesting}
	return el
}

func TestRenderMarkdown(t *testing.T) {
	lists := map[string]docs.List{
		"unordered": {ListProperties: &docs.ListProperties{
			NestingLevels: []*docs.NestingLevel{{GlyphType: "GLYPH_TYPE_UNSPECIFIED", GlyphSymbol: "●"}},
		}},
		"ordered": {ListProperties: &docs.ListProperties{
			NestingLevels: []*docs.NestingLevel{{GlyphType: "DECIMAL"}, {GlyphType: "ALPHA"}},
		}},
	}

	tests := []struct {
		name    string
		content []*docs.StructuralElement
		want    string
	}{
		{
			name:    "a plain paragraph loses its trailing newline",
			content: []*docs.StructuralElement{para("NORMAL_TEXT", run("Der Antrag ist zu stellen.\n"))},
			want:    "Der Antrag ist zu stellen.",
		},
		{
			// The reason this package hands markdown to the strip pass rather
			// than plain text: a heading glued to the sentence after it inflates
			// average sentence length on every document.
			name: "a heading keeps its level",
			content: []*docs.StructuralElement{
				para("HEADING_2", run("Voraussetzungen\n")),
				para("NORMAL_TEXT", run("Wer den Antrag stellt, braucht drei Dinge.\n")),
			},
			want: "## Voraussetzungen\n\nWer den Antrag stellt, braucht drei Dinge.",
		},
		{
			name:    "a title is a top-level heading",
			content: []*docs.StructuralElement{para("TITLE", run("Leitfaden\n"))},
			want:    "# Leitfaden",
		},
		{
			name: "runs of empty paragraphs collapse",
			content: []*docs.StructuralElement{
				para("NORMAL_TEXT", run("One.\n")),
				para("NORMAL_TEXT", run("\n")),
				para("NORMAL_TEXT", run("\n")),
				para("NORMAL_TEXT", run("Two.\n")),
			},
			want: "One.\n\nTwo.",
		},
		{
			name: "an unordered list",
			content: []*docs.StructuralElement{
				bullet("unordered", 0, "erstens\n"),
				bullet("unordered", 0, "zweitens\n"),
			},
			want: "- erstens\n- zweitens",
		},
		{
			name: "an ordered list is numbered",
			content: []*docs.StructuralElement{
				bullet("ordered", 0, "erstens\n"),
				bullet("ordered", 1, "ein Unterpunkt\n"),
			},
			want: "1. erstens\n  1. ein Unterpunkt",
		},
		{
			// A bullet whose list definition did not come back must still be a
			// list item; falling through to a plain paragraph would silently
			// merge a list into prose.
			name:    "an unknown list id still renders as a bullet",
			content: []*docs.StructuralElement{bullet("missing", 0, "punkt\n")},
			want:    "- punkt",
		},
		{
			name: "bold and italic wrap the word, not the space around it",
			content: []*docs.StructuralElement{para("NORMAL_TEXT",
				run("Das ist "),
				styledRun("wichtig ", &docs.TextStyle{Bold: true}),
				run("und dringend.\n"),
			)},
			want: "Das ist **wichtig** und dringend.",
		},
		{
			name: "a link becomes a markdown link",
			content: []*docs.StructuralElement{para("NORMAL_TEXT",
				run("Siehe "),
				styledRun("das Formular", &docs.TextStyle{Link: &docs.Link{Url: "https://example.com/f"}}),
				run(".\n"),
			)},
			want: "Siehe [das Formular](https://example.com/f).",
		},
		{
			// Emphasis that swallowed the newline would render literally as
			// asterisks in every markdown reader.
			name: "emphasis never wraps the paragraph's newline",
			content: []*docs.StructuralElement{para("NORMAL_TEXT",
				styledRun("Fertig.\n", &docs.TextStyle{Bold: true}),
			)},
			want: "**Fertig.**",
		},
		{
			name: "a soft line break becomes a line break",
			content: []*docs.StructuralElement{para("NORMAL_TEXT",
				run("Erste Zeile\vzweite Zeile\n"),
			)},
			want: "Erste Zeile\nzweite Zeile",
		},
		{
			// A heading is one line by definition; a soft break inside one would
			// otherwise produce a second line that is not a heading.
			name: "a soft break inside a heading stays on one line",
			content: []*docs.StructuralElement{para("HEADING_1",
				run("Teil eins\vund zwei\n"),
			)},
			want: "# Teil eins und zwei",
		},
		{
			name: "a horizontal rule",
			content: []*docs.StructuralElement{{Paragraph: &docs.Paragraph{
				Elements: []*docs.ParagraphElement{
					{HorizontalRule: &docs.HorizontalRule{}},
					{TextRun: run("\n")},
				},
			}}},
			want: "---",
		},
		{
			name: "a table becomes a pipe table",
			content: []*docs.StructuralElement{{Table: &docs.Table{TableRows: []*docs.TableRow{
				{TableCells: []*docs.TableCell{
					{Content: []*docs.StructuralElement{para("NORMAL_TEXT", run("Frist\n"))}},
					{Content: []*docs.StructuralElement{para("NORMAL_TEXT", run("Dauer\n"))}},
				}},
				{TableCells: []*docs.TableCell{
					{Content: []*docs.StructuralElement{para("NORMAL_TEXT", run("Antrag\n"))}},
					{Content: []*docs.StructuralElement{para("NORMAL_TEXT", run("4 Wochen\n"))}},
				}},
			}}}},
			want: "| Frist | Dauer |\n| --- | --- |\n| Antrag | 4 Wochen |",
		},
		{
			// A generated index of headings that are already in the document
			// would count every heading twice.
			name: "a table of contents is skipped",
			content: []*docs.StructuralElement{
				{TableOfContents: &docs.TableOfContents{Content: []*docs.StructuralElement{
					para("NORMAL_TEXT", run("Voraussetzungen\n")),
				}}},
				para("NORMAL_TEXT", run("Text.\n")),
			},
			want: "Text.",
		},
		{
			name: "a person chip is part of the sentence",
			content: []*docs.StructuralElement{{Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{
				{TextRun: run("Zuständig ist ")},
				{Person: &docs.Person{PersonProperties: &docs.PersonProperties{Name: "Ada Lovelace", Email: "ada@example.com"}}},
				{TextRun: run(".\n")},
			}}}},
			want: "Zuständig ist Ada Lovelace.",
		},
		{
			// An image measures as nothing. Inventing placeholder text for it
			// would put words in the document that nobody wrote.
			name: "an image contributes no prose",
			content: []*docs.StructuralElement{{Paragraph: &docs.Paragraph{Elements: []*docs.ParagraphElement{
				{TextRun: run("Vorher. ")},
				{InlineObjectElement: &docs.InlineObjectElement{InlineObjectId: "kix.img"}},
				{TextRun: run("Nachher.\n")},
			}}}},
			want: "Vorher. Nachher.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderMarkdown(tc.content, lists); got != tc.want {
				t.Fatalf("renderMarkdown() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestRenderPlainIsWhatTheAPIMatchesOn(t *testing.T) {
	lists := map[string]docs.List{}
	content := []*docs.StructuralElement{
		para("HEADING_1", run("Überschrift\n")),
		para("NORMAL_TEXT",
			run("Das ist "),
			styledRun("wichtig", &docs.TextStyle{Bold: true}),
			run(" und dringend.\n"),
		),
	}

	// The whole reason two renderings exist: a --find string that spans a bold
	// word matches the document, and would not match markdown with "**" in the
	// middle of it. An occurrence count computed against markdown would report
	// zero and the command would refuse a replacement that works.
	const phrase = "Das ist wichtig und dringend."
	plain := renderPlain(content, lists)
	if countOccurrences(plain, phrase, false) != 1 {
		t.Fatalf("plain text = %q, want %q to be findable in it", plain, phrase)
	}
	if countOccurrences(renderMarkdown(content, lists), phrase, false) != 0 {
		t.Fatal("markdown contains the phrase unmarked; the two renderings have stopped differing and the guard above proves nothing")
	}
	// No markers of any kind are added to the literal text.
	if got := renderPlain(content, lists); got != "Überschrift\nDas ist wichtig und dringend.\n" {
		t.Fatalf("renderPlain() = %q", got)
	}
}
