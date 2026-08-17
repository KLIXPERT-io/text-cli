package gdocs

import (
	"strings"

	docs "google.golang.org/api/docs/v1"
)

// This file turns a Docs API document into two strings, and the difference
// between them is the whole point:
//
//   - Markdown is what a human and every downstream command read. Headings stay
//     headings until internal/strip reduces them, which is what keeps a
//     document's readability score identical whether it arrived through
//     `text docs read` or through `--url`.
//   - PlainText is the document's literal character content, with no markers
//     added. It is what an occurrence count for `docs replace --find` must be
//     computed against: a search string that spans a bold word matches the
//     document, and would not match markdown that has "**" in the middle of it.
//
// Both walk the same tree, so a change to one cannot silently skip content in
// the other.

// orderedGlyphs are the list glyph types that number their items. Anything else
// — a disc, a square, a custom symbol — is an unordered list.
var orderedGlyphs = map[string]bool{
	"DECIMAL":       true,
	"ZERO_DECIMAL":  true,
	"UPPER_ALPHA":   true,
	"ALPHA":         true,
	"UPPER_ROMAN":   true,
	"ROMAN":         true,
	"HEBREW_LETTER": true,
	"KOREAN_LETTER": true,
}

// headingPrefixes maps a Docs named style onto its markdown marker.
//
// TITLE and HEADING_1 both become a single "#". They are different styles in
// Docs but the same level of document structure, and inventing a level above
// H1 to tell them apart would add a heading the document does not have.
var headingPrefixes = map[string]string{
	"TITLE":     "# ",
	"HEADING_1": "# ",
	"HEADING_2": "## ",
	"HEADING_3": "### ",
	"HEADING_4": "#### ",
	"HEADING_5": "##### ",
	"HEADING_6": "###### ",
}

// renderer walks one tab's body.
type renderer struct {
	// lists resolves a paragraph's bullet to its glyph type, which is what says
	// whether the item is numbered. The map belongs to the tab, not the
	// document: each tab carries its own list definitions.
	lists map[string]docs.List
	// markdown selects the output form. See the file comment.
	markdown bool
}

// block is one rendered element plus the one thing the joiner needs to know
// about it: whether it is a list item. Two adjacent items belong on consecutive
// lines, and everything else is separated by a blank line.
type block struct {
	text string
	list bool
}

// Markdown renders a tab body as markdown.
func renderMarkdown(content []*docs.StructuralElement, lists map[string]docs.List) string {
	r := renderer{lists: lists, markdown: true}
	return joinBlocks(r.walk(content))
}

// renderPlain renders a tab body as its literal text.
func renderPlain(content []*docs.StructuralElement, lists map[string]docs.List) string {
	r := renderer{lists: lists}
	var sb strings.Builder
	for _, b := range r.walk(content) {
		sb.WriteString(b.text)
	}
	return sb.String()
}

// joinBlocks separates markdown blocks by a blank line, keeps consecutive list
// items together, and drops the empty ones — which is how a run of empty
// paragraphs in the source collapses into one separation instead of a column of
// whitespace.
func joinBlocks(blocks []block) string {
	var sb strings.Builder
	prevList := false
	first := true
	for _, b := range blocks {
		if strings.TrimSpace(b.text) == "" {
			continue
		}
		switch {
		case first:
			first = false
		case b.list && prevList:
			sb.WriteString("\n")
		default:
			sb.WriteString("\n\n")
		}
		sb.WriteString(b.text)
		prevList = b.list
	}
	return sb.String()
}

// walk renders each structural element.
func (r renderer) walk(content []*docs.StructuralElement) []block {
	out := make([]block, 0, len(content))
	for _, el := range content {
		switch {
		case el == nil:
			continue
		case el.Paragraph != nil:
			out = append(out, block{text: r.paragraph(el.Paragraph), list: el.Paragraph.Bullet != nil})
		case el.Table != nil:
			out = append(out, block{text: r.table(el.Table)})
		case el.TableOfContents != nil:
			// A table of contents is generated from the headings that are
			// already in the text. Rendering it would count every heading
			// twice — once as a heading and once as a line of the index —
			// which moves a readability score for no reason.
			continue
		}
	}
	return out
}

// paragraph renders one paragraph, including its list or heading prefix.
func (r renderer) paragraph(p *docs.Paragraph) string {
	var sb strings.Builder
	for _, el := range p.Elements {
		sb.WriteString(r.element(el))
	}
	text := sb.String()

	if !r.markdown {
		return text
	}

	// A vertical tab is what Docs stores for a soft line break (Shift+Enter).
	// It is a line break in the document, so it has to become one here rather
	// than an invisible control character inside a sentence.
	text = strings.ReplaceAll(text, "\v", "\n")
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		// An empty paragraph that carried a horizontal rule is the rule.
		if hasHorizontalRule(p) {
			return "---"
		}
		return ""
	}

	if p.Bullet != nil {
		indent := strings.Repeat("  ", int(p.Bullet.NestingLevel))
		return indent + r.bulletMarker(p.Bullet) + strings.TrimLeft(text, " \t")
	}
	if p.ParagraphStyle != nil {
		if prefix, ok := headingPrefixes[p.ParagraphStyle.NamedStyleType]; ok {
			// A heading is a single line by definition: a soft break inside one
			// would otherwise produce a second line that is not a heading.
			return prefix + strings.Join(strings.Split(text, "\n"), " ")
		}
	}
	return text
}

// bulletMarker picks "- " or "1. ".
//
// Every ordered item is numbered "1.": markdown renderers renumber an ordered
// list themselves, and the actual number a reader sees in Docs depends on where
// the list started, which the API reports per nesting level rather than per
// item. "1." is right in every renderer and wrong nowhere.
func (r renderer) bulletMarker(b *docs.Bullet) string {
	list, ok := r.lists[b.ListId]
	if !ok || list.ListProperties == nil {
		return "- "
	}
	levels := list.ListProperties.NestingLevels
	if int(b.NestingLevel) >= len(levels) {
		return "- "
	}
	if lvl := levels[b.NestingLevel]; lvl != nil && orderedGlyphs[lvl.GlyphType] {
		return "1. "
	}
	return "- "
}

func hasHorizontalRule(p *docs.Paragraph) bool {
	for _, el := range p.Elements {
		if el != nil && el.HorizontalRule != nil {
			return true
		}
	}
	return false
}

// element renders one inline element.
func (r renderer) element(el *docs.ParagraphElement) string {
	switch {
	case el == nil:
		return ""

	case el.TextRun != nil:
		if r.markdown {
			return styled(el.TextRun)
		}
		return el.TextRun.Content

	case el.Person != nil:
		// A person chip is real text in the document — the reviewer's name is
		// part of the sentence it sits in — so it is rendered, not skipped.
		if p := el.Person.PersonProperties; p != nil {
			if p.Name != "" {
				return p.Name
			}
			return p.Email
		}
		return ""

	case el.RichLink != nil:
		p := el.RichLink.RichLinkProperties
		if p == nil {
			return ""
		}
		if r.markdown && p.Uri != "" && p.Title != "" {
			return "[" + p.Title + "](" + p.Uri + ")"
		}
		return p.Title

	case el.HorizontalRule != nil, el.PageBreak != nil, el.ColumnBreak != nil,
		el.Equation != nil, el.FootnoteReference != nil, el.InlineObjectElement != nil,
		el.AutoText != nil:
		// None of these is prose. An image, an equation, a page number and a
		// footnote marker all measure as nothing, and inventing placeholder
		// text for them would put words in the document that nobody wrote.
		// The horizontal rule is handled at paragraph level, where it is the
		// whole block rather than a character inside a sentence.
		return ""
	}
	return ""
}

// styled wraps a run in its markdown emphasis and link.
func styled(run *docs.TextRun) string {
	content := run.Content
	if content == "" {
		return ""
	}
	style := run.TextStyle
	if style == nil {
		return content
	}

	// Trailing newlines belong to the paragraph, not to the emphasised phrase:
	// "**bold\n**" is not emphasis in any markdown renderer.
	core := strings.TrimRight(content, "\n\v")
	tail := content[len(core):]
	if strings.TrimSpace(core) == "" {
		return content
	}

	// Leading and trailing spaces have to stay outside the markers for the same
	// reason: "** bold **" renders literally.
	lead := core[:len(core)-len(strings.TrimLeft(core, " \t"))]
	trail := core[len(strings.TrimRight(core, " \t")):]
	word := core[len(lead) : len(core)-len(trail)]

	if style.Bold {
		word = "**" + word + "**"
	}
	if style.Italic {
		word = "*" + word + "*"
	}
	if style.Link != nil && style.Link.Url != "" {
		word = "[" + word + "](" + style.Link.Url + ")"
	}
	return lead + word + trail + tail
}

// table renders a table.
//
// In markdown it becomes a pipe table with the first row as the header, which
// is the only shape markdown has — Docs tables often have no header row, and a
// table whose first row is data reads as a table with an odd header rather than
// as broken markup.
func (r renderer) table(t *docs.Table) string {
	if len(t.TableRows) == 0 {
		return ""
	}
	if !r.markdown {
		var sb strings.Builder
		for _, row := range t.TableRows {
			for _, cell := range row.TableCells {
				if cell == nil {
					continue
				}
				sb.WriteString(renderPlain(cell.Content, r.lists))
			}
		}
		return sb.String()
	}

	rows := make([][]string, 0, len(t.TableRows))
	width := 0
	for _, row := range t.TableRows {
		if row == nil {
			continue
		}
		cells := make([]string, 0, len(row.TableCells))
		for _, cell := range row.TableCells {
			if cell == nil {
				cells = append(cells, "")
				continue
			}
			cells = append(cells, cellText(renderMarkdown(cell.Content, r.lists)))
		}
		if len(cells) > width {
			width = len(cells)
		}
		rows = append(rows, cells)
	}
	if width == 0 {
		return ""
	}

	var sb strings.Builder
	for i, cells := range rows {
		for len(cells) < width {
			cells = append(cells, "")
		}
		sb.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if i == 0 {
			sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// cellText flattens a cell to one line. A markdown table cell cannot contain a
// line break, so a multi-paragraph cell joins with a space rather than breaking
// the table.
func cellText(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	fields := strings.Fields(strings.ReplaceAll(s, "\n", " "))
	return strings.Join(fields, " ")
}
