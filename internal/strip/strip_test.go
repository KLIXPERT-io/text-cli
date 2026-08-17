package strip

import (
	"strings"
	"testing"
)

func TestModes(t *testing.T) {
	got := Modes()
	want := []string{"none", "markdown", "html", "auto"}
	if len(got) != len(want) {
		t.Fatalf("Modes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Modes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The caller must not be able to corrupt the package's own list.
	Modes()[0] = "mutated"
	if Modes()[0] != "none" {
		t.Error("Modes() returns a shared slice")
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"none", "none", true},
		{"markdown", "markdown", true},
		{"html", "html", true},
		{"auto", "auto", true},
		{"empty is not a mode", "", false},
		{"unknown", "rst", false},
		{"case sensitive", "Markdown", false},
		{"md abbreviation is not accepted", "md", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.in); got != tc.want {
				t.Errorf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestApplyModeNoneIsIdentity(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"plain prose", "Two sentences. The second one."},
		{"markdown", "# Heading\n\n```go\nfunc main() {}\n```\n\n- item\n"},
		{"html", "<p>Hallo <b>Welt</b></p>"},
		{"ragged whitespace", "  leading and trailing   \n\n\n\nblank lines\t\n"},
		{"windows newlines", "one\r\ntwo\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Apply(tc.in, ModeNone); got != tc.in {
				t.Errorf("Apply(%q, ModeNone) = %q, want it unchanged", tc.in, got)
			}
		})
	}
}

func TestApplyUnknownModePassesThrough(t *testing.T) {
	const in = "# Heading\n\ntext"
	if got := Apply(in, Mode("rst")); got != in {
		t.Errorf("Apply with unknown mode = %q, want %q", got, in)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Mode
	}{
		{"empty", "", ModeNone},
		{"blank", "   \n\n\t\n", ModeNone},
		{"plain prose", "Ein einfacher Satz. Und noch einer, ohne jede Auszeichnung.", ModeNone},
		{"plain prose with an ampersand", "Tom & Jerry sind ein Duo. Mehr nicht.", ModeNone},
		{"doctype", "<!DOCTYPE html>\n<html><body><p>Hi</p></body></html>", ModeHTML},
		{"html element anywhere", "some preamble\n<html>\n<p>Hi</p>", ModeHTML},
		{"body fragment", "<body><p>Hi</p></body>", ModeHTML},
		{"three distinct block tags", "<section><p>One</p><ul><li>Two</li></ul></section>", ModeHTML},
		{"repeated single block tag", "<p>One.</p><p>Two.</p>", ModeHTML},
		{"atx heading", "# Title\n\nBody text.", ModeMarkdown},
		{"front matter", "---\ntitle: x\n---\n\nBody.", ModeMarkdown},
		{"fenced code", "Intro\n\n```go\nx := 1\n```\n", ModeMarkdown},
		{"list markers", "Shopping:\n\n- eggs\n- milk\n", ModeMarkdown},
		{"ordered list", "Steps:\n\n1. First\n2. Second\n", ModeMarkdown},
		{"link syntax", "Read the [docs](https://example.com) first.", ModeMarkdown},
		{"reference definition", "Read the docs.\n\n[docs]: https://example.com\n", ModeMarkdown},
		{"table", "| a | b |\n| - | - |\n", ModeMarkdown},
		// Markdown may legally embed HTML, so a document that shows both sets
		// of signals is treated as Markdown — whose stripper also strips HTML.
		{"markdown with embedded html prefers markdown", "# Title\n\n<div><p>Embedded</p></div>\n", ModeMarkdown},
		{"html document with a dash list inside still html", "<!DOCTYPE html><html><body><p>- not a list</p></body></html>", ModeHTML},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.in); got != tc.want {
				t.Errorf("Detect() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyAuto(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "markdown is stripped",
			in:   "# Title\n\nSome `code` and a [link](https://example.com).\n",
			want: "Title.\n\nSome and a link.",
		},
		{
			name: "html is stripped",
			in:   "<html><body><p>Ein Satz.</p></body></html>",
			want: "Ein Satz.",
		},
		{
			name: "plain text is left alone",
			in:   "Ein einfacher Satz.   Mit  eigenwilligen Abständen.\n\n\n",
			want: "Ein einfacher Satz.   Mit  eigenwilligen Abständen.\n\n\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Apply(tc.in, ModeAuto); got != tc.want {
				t.Errorf("Apply(auto) = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTerminate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain heading", "Installation", "Installation."},
		{"already terminated", "Done.", "Done."},
		{"question", "Why?", "Why?"},
		{"ellipsis", "And so on…", "And so on…"},
		{"terminator behind a bracket", "(Done.)", "(Done.)"},
		{"terminator behind a quote", "\"Fertig.\"", "\"Fertig.\""},
		// The terminator goes inside the closing quote, where a segmenter that
		// knows about closers ("...") will still see it end the sentence.
		{"quoted heading", "\"Fertig\"", "\"Fertig.\""},
		{"trailing colon is promoted, not stacked", "Note:", "Note."},
		{"trailing comma is promoted", "Ada, Lead,", "Ada, Lead."},
		{"trailing spaces", "Heading   ", "Heading."},
		{"idempotent", terminate("Heading"), "Heading."},
		{"nothing to terminate", "", ""},
		{"punctuation only stays untouched", "---", "---"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminate(tc.in); got != tc.want {
				t.Errorf("terminate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Stripping must never leave artefacts that would fabricate sentences or words:
// no ". ." runs, no wordless lines, no leading or trailing blank lines.
func TestStrippedOutputHasNoPhantomSentences(t *testing.T) {
	docs := []struct {
		name string
		mode Mode
		in   string
	}{
		{"markdown kitchen sink", ModeMarkdown, kitchenSinkMarkdown},
		{"heading of only an image", ModeMarkdown, "# ![logo](logo.png)\n\nBody text.\n"},
		{"heading of only a code span", ModeMarkdown, "# `func main()`\n\nBody text.\n"},
		{"empty table cells", ModeMarkdown, "| a |  |\n| - | - |\n|   |  |\n"},
		{"html with empty elements", ModeHTML, "<p></p><div>  </div><p>Real text.</p><br><li></li>"},
		{"html of nothing but a script", ModeHTML, "<script>var a = 1;</script>"},
	}
	for _, tc := range docs {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in, tc.mode)
			if strings.Contains(got, ". .") || strings.Contains(got, ".\n.") {
				t.Errorf("phantom sentence in %q", got)
			}
			if strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
				t.Errorf("stray blank line at the edge of %q", got)
			}
			if strings.Contains(got, "\n\n\n") {
				t.Errorf("run of blank lines in %q", got)
			}
			for _, line := range strings.Split(got, "\n") {
				if line != "" && !hasProse(line) {
					t.Errorf("wordless line %q in %q", line, got)
				}
				if line != strings.TrimSpace(line) {
					t.Errorf("untrimmed line %q in %q", line, got)
				}
			}
		})
	}
}

const kitchenSinkMarkdown = `---
title: Kitchen sink
tags: [a, b]
---

# Heading one

A paragraph with a [link](https://example.com/docs), an ![image](/img.png),
` + "`inline code`" + `, **bold**, *italic* and ~~struck~~ text.

## Heading two {#two}

> A quotation.

- item one
- item two

| Name | Role |
| ---- | ---- |
| Ada  | Lead |

` + "```" + `go
func main() { fmt.Println("x") }
` + "```" + `

<div class="note"><p>Embedded HTML.</p></div>

Closing paragraph.
`
