package strip

import (
	"strings"
	"testing"
)

func TestStripMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// -- front matter ------------------------------------------------
		{
			name: "yaml front matter",
			in:   "---\ntitle: Post\ndraft: false\n---\n\nThe body starts here.\n",
			want: "The body starts here.",
		},
		{
			name: "toml front matter",
			in:   "+++\ntitle = \"Post\"\n+++\n\nThe body starts here.\n",
			want: "The body starts here.",
		},
		{
			name: "yaml front matter closed with dots",
			in:   "---\ntitle: Post\n...\nThe body starts here.\n",
			want: "The body starts here.",
		},
		{
			// The second "---" is a setext underline, so what follows a real
			// front-matter block elsewhere in the document reads as a heading.
			name: "front matter only counts at the very start",
			in:   "A paragraph.\n\n---\ntitle: not front matter\n---\n",
			want: "A paragraph.\n\ntitle: not front matter.",
		},
		{
			name: "unterminated front matter is a rule, not metadata",
			in:   "---\nJust a paragraph after a rule.\n",
			want: "Just a paragraph after a rule.",
		},

		// -- code --------------------------------------------------------
		{
			name: "fenced code block",
			in:   "Before.\n\n```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},
		{
			name: "fenced code block with tildes and an info string",
			in:   "Before.\n\n~~~python title=\"x.py\"\ndef f():\n    return 1\n~~~\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},
		{
			name: "unclosed fence runs to the end of the input",
			in:   "Before.\n\n```js\nconst x = 1;\nconsole.log(x);\n",
			want: "Before.",
		},
		{
			name: "longer closing fence still closes",
			in:   "Before.\n\n```\ncode\n`````\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},
		{
			name: "indented code block",
			in:   "Before.\n\n    func main() {}\n    var x int\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},
		{
			name: "four space indent inside a list is a continuation, not code",
			in:   "- item one\n\n    still the same item\n\n- item two\n",
			want: "item one.\n\nstill the same item\n\nitem two.",
		},
		{
			name: "inline code content is dropped",
			in:   "Call `client.Post(ctx, &req)` to send the payload.\n",
			want: "Call to send the payload.",
		},
		{
			name: "double backtick span containing a backtick",
			in:   "Write ``a `b` c`` in the docs.\n",
			want: "Write in the docs.",
		},
		{
			name: "unbalanced backtick keeps the prose",
			in:   "A stray ` backtick in prose.\n",
			want: "A stray backtick in prose.",
		},

		// -- links, images, urls ------------------------------------------
		{
			name: "inline link keeps its text and drops the url",
			in:   "Read the [Anthropic docs](https://docs.anthropic.com/en/api) first.\n",
			want: "Read the Anthropic docs first.",
		},
		{
			name: "link url with nested parentheses",
			in:   "See [Go (language)](https://en.wikipedia.org/wiki/Go_(programming_language)) here.\n",
			want: "See Go (language) here.",
		},
		{
			name: "image is dropped entirely",
			in:   "Before ![a screenshot of the tool](/img/shot.png) after.\n",
			want: "Before after.",
		},
		{
			name: "reference link and its definition",
			in:   "Read the [docs][d] and the [spec][].\n\n[d]: https://example.com/docs \"Docs\"\n[spec]: https://example.com/spec\n",
			want: "Read the docs and the spec.",
		},
		{
			name: "shortcut reference link",
			in:   "Made by [Anthropic].\n\n[Anthropic]: https://anthropic.com\n",
			want: "Made by Anthropic.",
		},
		{
			name: "autolink",
			in:   "The spec lives at <https://example.com/spec> today.\n",
			want: "The spec lives at today.",
		},
		{
			name: "bare url does not swallow the sentence terminator",
			in:   "Fetch https://example.com/a_b-c?q=1. Then continue.\n",
			want: "Fetch. Then continue.",
		},
		{
			name: "email autolink",
			in:   "Write to <mail@example.com> for access.\n",
			want: "Write to for access.",
		},

		// -- headings ------------------------------------------------------
		{
			name: "atx heading gets a terminator",
			in:   "# Getting Started\n\nThe tool ships as one binary.\n",
			want: "Getting Started.\n\nThe tool ships as one binary.",
		},
		{
			name: "atx heading that already ends in punctuation",
			in:   "## Why bother?\n\nBecause markup lies.\n",
			want: "Why bother?\n\nBecause markup lies.",
		},
		{
			name: "closed atx heading",
			in:   "### Setup ###\n\nBody.\n",
			want: "Setup.\n\nBody.",
		},
		{
			name: "heading with a custom id attribute",
			in:   "## Why it matters {#why-it-matters}\n\nBody.\n",
			want: "Why it matters.\n\nBody.",
		},
		{
			name: "setext heading with equals",
			in:   "Getting Started\n===============\n\nBody.\n",
			want: "Getting Started.\n\nBody.",
		},
		{
			name: "setext heading with dashes",
			in:   "Getting Started\n---------------\n\nBody.\n",
			want: "Getting Started.\n\nBody.",
		},
		{
			name: "empty heading disappears without leaving a sentence",
			in:   "#\n\nBody.\n",
			want: "Body.",
		},

		// -- lists, quotes, rules ------------------------------------------
		{
			name: "nested unordered list",
			in:   "- First item\n- Second item\n  - Nested item\n- Third item\n",
			want: "First item.\nSecond item.\nNested item.\nThird item.",
		},
		{
			name: "ordered list",
			in:   "1. First\n2) Second\n",
			want: "First.\nSecond.",
		},
		{
			name: "task list",
			in:   "- [x] Done thing\n- [ ] Open thing\n",
			want: "Done thing.\nOpen thing.",
		},
		{
			name: "wrapped list item is terminated once, at its end",
			in:   "- an item that wraps\n  onto a second line\n- a short one\n",
			want: "an item that wraps\nonto a second line.\na short one.",
		},
		{
			name: "blockquote",
			in:   "> A quoted remark.\n> Still quoted.\n",
			want: "A quoted remark.\nStill quoted.",
		},
		{
			name: "nested blockquote with a list",
			in:   "> > - quoted item\n",
			want: "quoted item.",
		},
		{
			name: "horizontal rules disappear",
			in:   "Above.\n\n***\n\nBelow.\n\n___\n\nEnd.\n",
			want: "Above.\n\nBelow.\n\nEnd.",
		},

		// -- tables ---------------------------------------------------------
		{
			name: "table keeps cell text and ends each row",
			in:   "| Name | Role |\n|------|------|\n| Ada  | Lead |\n| Alan | Cryptanalyst |\n",
			want: "Name, Role.\n\nAda, Lead.\nAlan, Cryptanalyst.",
		},
		{
			name: "table with aligned separator row",
			in:   "| a | b |\n| :--- | ---: |\n| 1 | 2 |\n",
			want: "a, b.\n\n1, 2.",
		},
		{
			name: "table cells keep their own punctuation",
			in:   "| Erklärt das. | Und das? |\n| --- | --- |\n",
			want: "Erklärt das. Und das?",
		},

		// -- inline markers --------------------------------------------------
		{
			name: "emphasis markers are removed, text kept",
			in:   "This is **bold**, *italic*, _underlined_, ~~struck~~ and ***both***.\n",
			want: "This is bold, italic, underlined, struck and both.",
		},
		{
			name: "snake case is not emphasis",
			in:   "The flag is called max_retry_count today.\n",
			want: "The flag is called max_retry_count today.",
		},
		{
			name: "escaped markers stay literal",
			in:   "A literal \\*asterisk\\* and a \\# hash.\n",
			want: "A literal *asterisk* and a # hash.",
		},
		{
			name: "footnote marker and definition",
			in:   "A claim[^1] worth checking.\n\n[^1]: The supporting note.\n",
			want: "A claim worth checking.\n\nThe supporting note.",
		},

		// -- embedded html ----------------------------------------------------
		{
			name: "html block inside markdown",
			in:   "Paragraph one.\n\n<div class=\"note\">\n  <p>Embedded HTML prose</p>\n</div>\n\nParagraph two.\n",
			want: "Paragraph one.\n\nEmbedded HTML prose.\n\nParagraph two.",
		},
		{
			name: "inline html tags do not split words",
			in:   "Der <em>Sprach</em><em>raum</em> ist groß.\n",
			want: "Der Sprachraum ist groß.",
		},
		{
			name: "html comment inside markdown",
			in:   "Before.\n\n<!-- a TODO note\nspanning lines -->\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},
		{
			name: "entities are decoded",
			in:   "Gr&ouml;&szlig;e &amp; Ma&szlig;e.\n",
			want: "Größe & Maße.",
		},
		{
			name: "html inside a code fence is not treated as html",
			in:   "Before.\n\n```html\n<script>alert(1)</script>\n```\n\nAfter.\n",
			want: "Before.\n\nAfter.",
		},

		// -- whitespace ---------------------------------------------------------
		{
			name: "blank line runs collapse but paragraphs stay separate",
			in:   "One.\n\n\n\n\nTwo.\n",
			want: "One.\n\nTwo.",
		},
		{
			name: "trailing spaces and windows newlines",
			in:   "One.   \r\n\r\nTwo.  \r\n",
			want: "One.\n\nTwo.",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "markup only input",
			in:   "```\ncode\n```\n",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Apply(tc.in, ModeMarkdown); got != tc.want {
				t.Errorf("Apply(markdown)\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// A heading with no terminal punctuation fuses into the paragraph below it once
// the '#' markers are gone, and one fused sentence per heading is enough to
// wreck the sentence count for a whole document. This pins the fix.
func TestMarkdownHeadingsTerminateSentences(t *testing.T) {
	const doc = `# Installation

The tool ships as a single static binary.

## Usage

Pipe text into it.

### Notes

It reads stdin.
`
	got := Apply(doc, ModeMarkdown)
	for _, heading := range []string{"Installation.", "Usage.", "Notes."} {
		if !strings.Contains(got, heading) {
			t.Errorf("heading %q lost its terminator in %q", heading, got)
		}
	}
	for _, fused := range []string{"Installation The", "Usage Pipe", "Notes It"} {
		if strings.Contains(got, fused) {
			t.Errorf("heading fused with the paragraph below it: %q in %q", fused, got)
		}
	}
	if want := 6; countSentences(got) != want {
		t.Errorf("sentence count = %d, want %d, in %q", countSentences(got), want, got)
	}
}

// TestMarkdownCodeBlockDoesNotPolluteProse is the regression test for the bug
// this package exists to fix: a document whose Markdown is scored as prose gets
// the code block counted as words and sentences, which moves a Flesch score by
// tens of points.
func TestMarkdownCodeBlockDoesNotPolluteProse(t *testing.T) {
	const doc = "---\ntitle: Shipping the client\n---\n\n# Shipping the client\n\n" +
		"The client speaks HTTP and nothing else. It keeps no state between calls, " +
		"so you can share one instance across goroutines.\n\n" +
		"## Configuration\n\n" +
		"Set the token before the first request. The defaults are safe.\n\n" +
		"```go\n" +
		"package client\n\n" +
		"import (\n\t\"context\"\n\t\"net/http\"\n)\n\n" +
		"type Config struct {\n\tToken      string\n\tMaxRetries int\n\tTimeout    time.Duration\n}\n\n" +
		"func NewClient(ctx context.Context, cfg *Config) (*Client, error) {\n" +
		"\tif cfg.Token == \"\" {\n\t\treturn nil, errors.New(\"missing token\")\n\t}\n" +
		"\treturn &Client{httpClient: &http.Client{Timeout: cfg.Timeout}}, nil\n}\n" +
		"```\n\n" +
		"Read the [reference](https://example.com/reference) once you are set up.\n"

	got := Apply(doc, ModeMarkdown)

	codeIdentifiers := []string{
		"package client", "import", "context.Context", "http.Client", "MaxRetries",
		"NewClient", "errors.New", "httpClient", "struct", "nil", "cfg", "func",
		"time.Duration", "\t",
	}
	for _, id := range codeIdentifiers {
		if strings.Contains(got, id) {
			t.Errorf("code identifier %q survived stripping:\n%s", id, got)
		}
	}

	proseSentences := []string{
		"Shipping the client.",
		"The client speaks HTTP and nothing else.",
		"It keeps no state between calls, so you can share one instance across goroutines.",
		"Configuration.",
		"Set the token before the first request.",
		"The defaults are safe.",
		"Read the reference once you are set up.",
	}
	for _, s := range proseSentences {
		if !strings.Contains(got, s) {
			t.Errorf("prose sentence %q was lost:\n%s", s, got)
		}
	}

	// The point of the exercise: the counts the readability formulas consume.
	rawWords, rawSentences := countWords(doc), countSentences(doc)
	gotWords, gotSentences := countWords(got), countSentences(got)
	if gotWords >= rawWords {
		t.Errorf("word count did not drop: raw %d, stripped %d", rawWords, gotWords)
	}
	if gotSentences <= 2 {
		t.Errorf("sentence count %d is too low to score with", gotSentences)
	}
	t.Logf("raw: %d words / %d sentences — stripped: %d words / %d sentences",
		rawWords, rawSentences, gotWords, gotSentences)
}

// countWords and countSentences are deliberately naive stand-ins for the real
// tokenizer: this package must not depend on it to prove a point about it.
func countWords(s string) int {
	n := 0
	for _, f := range strings.Fields(s) {
		if hasProse(f) {
			n++
		}
	}
	return n
}

func countSentences(s string) int {
	n := 0
	for _, para := range strings.Split(s, "\n") {
		if !hasProse(para) {
			continue
		}
		terminators := strings.Count(para, ".") + strings.Count(para, "!") + strings.Count(para, "?")
		if terminators == 0 {
			terminators = 1 // an unterminated line is still one sentence
		}
		n += terminators
	}
	return n
}
