package doc

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The blocks a decoder emits are the contract with the strip pass: a heading,
// an item and a row each have to arrive as their own block, and consecutive
// items and rows have to arrive as one. These cases pin the rendering that
// every decoder in this package depends on.
func TestMDBuilderBlocks(t *testing.T) {
	tests := []struct {
		name  string
		build func(b *mdBuilder)
		want  string
	}{
		{
			name: "blocks are separated by a blank line",
			build: func(b *mdBuilder) {
				b.Heading(2, "Einführung")
				b.Para("Ein Absatz.")
			},
			want: "## Einführung\n\nEin Absatz.\n",
		},
		{
			name: "consecutive items are one list block",
			build: func(b *mdBuilder) {
				b.Item(0, false, "eins")
				b.Item(0, false, "zwei")
				b.Item(1, false, "zwei a")
			},
			want: "- eins\n- zwei\n  - zwei a\n",
		},
		{
			name: "consecutive rows are one table block",
			build: func(b *mdBuilder) {
				b.Row([]string{"A", "B"})
				b.Row([]string{"C", "D"})
			},
			want: "| A | B |\n| C | D |\n",
		},
		{
			name: "a paragraph between two items ends the list",
			build: func(b *mdBuilder) {
				b.Item(0, false, "eins")
				b.Para("Dazwischen.")
				b.Item(0, false, "zwei")
			},
			want: "- eins\n\nDazwischen.\n\n- zwei\n",
		},
		{
			name: "a table after a list is its own block",
			build: func(b *mdBuilder) {
				b.Item(0, true, "eins")
				b.Row([]string{"A", "B"})
				b.Item(0, false, "zwei")
			},
			want: "1. eins\n\n| A | B |\n\n- zwei\n",
		},
		{
			name: "an empty text adds no block and does not break the one open",
			build: func(b *mdBuilder) {
				b.Item(0, false, "eins")
				b.Para("   ")
				b.Heading(1, "")
				b.Row(nil)
				b.Item(0, false, "zwei")
			},
			want: "- eins\n- zwei\n",
		},
		{
			name: "a heading closes an open table",
			build: func(b *mdBuilder) {
				b.Row([]string{"A"})
				b.Heading(3, "Danach")
			},
			want: "| A |\n\n### Danach\n",
		},
		{
			name:  "nothing added renders nothing",
			build: func(b *mdBuilder) {},
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b mdBuilder
			tc.build(&b)
			if got := b.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Empty has to account for the block still open, or a decoder whose only output
// is a table reports itself as having produced nothing and the file comes back
// as empty_input.
func TestMDBuilderEmpty(t *testing.T) {
	tests := []struct {
		name  string
		build func(b *mdBuilder)
		want  bool
	}{
		{name: "nothing added", build: func(b *mdBuilder) {}, want: true},
		{name: "only whitespace added", build: func(b *mdBuilder) { b.Para("  \t ") }, want: true},
		{name: "one paragraph", build: func(b *mdBuilder) { b.Para("Text.") }},
		{name: "only a list", build: func(b *mdBuilder) { b.Item(0, false, "eins") }},
		{name: "only a table", build: func(b *mdBuilder) { b.Row([]string{"A"}) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b mdBuilder
			tc.build(&b)
			if got := b.Empty(); got != tc.want {
				t.Fatalf("Empty() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A list or a table has to cost time proportional to its size, not to its size
// squared.
//
// Appending each line to the block with += copies the whole block, and a
// spreadsheet is where that stops being theoretical: an .ods of a few hundred
// thousand rows is an ordinary export, it decodes into one table block, and
// squared it turned a one-megabyte file into three quarters of a minute inside a
// CLI whose stated input limit is ten megabytes. The deadline is deliberately
// far above the linear cost — this test is here to catch a return to quadratic,
// not to police a constant factor.
func TestMDBuilderIsLinearInBlockSize(t *testing.T) {
	const rows = 200_000

	tests := []struct {
		name string
		add  func(b *mdBuilder, i int)
		want func(i int) string
	}{
		{
			name: "one table of many rows",
			add:  func(b *mdBuilder, i int) { b.Row([]string{"Zelle " + strconv.Itoa(i), "Wert"}) },
			want: func(i int) string { return "| Zelle " + strconv.Itoa(i) + " | Wert |" },
		},
		{
			name: "one list of many items",
			add:  func(b *mdBuilder, i int) { b.Item(0, false, "Punkt "+strconv.Itoa(i)) },
			want: func(i int) string { return "- Punkt " + strconv.Itoa(i) },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			var b mdBuilder
			for i := 0; i < rows; i++ {
				tc.add(&b, i)
			}
			out := b.String()
			if elapsed := time.Since(start); elapsed > 20*time.Second {
				t.Fatalf("building %d rows took %s; the block builder is quadratic again", rows, elapsed)
			}

			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			if len(lines) != rows {
				t.Fatalf("got %d lines, want %d", len(lines), rows)
			}
			// The whole thing is one block: a blank line anywhere in it would mean
			// the rows were split into separate tables or lists.
			if strings.Contains(out, "\n\n") {
				t.Fatal("the rows were not assembled into a single block")
			}
			for _, i := range []int{0, rows / 2, rows - 1} {
				if lines[i] != tc.want(i) {
					t.Fatalf("line %d = %q, want %q", i, lines[i], tc.want(i))
				}
			}
		})
	}
}
