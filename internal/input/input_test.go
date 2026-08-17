package input

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// pipe marks stdin as piped without touching the real process stdin.
func pipe(v bool) *bool { return &v }

func codeOf(t *testing.T, err error) errs.Code {
	t.Helper()
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is not *errs.E: %v", err)
	}
	return e.Code
}

func TestLoadFromArgs(t *testing.T) {
	items, err := Load(Options{Args: []string{"Hello", "world."}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	// Args join with a space so `text readability Hello world.` reads naturally.
	if items[0].Text != "Hello world." {
		t.Errorf("text = %q, want %q", items[0].Text, "Hello world.")
	}
	if items[0].ID != "0" {
		t.Errorf("id = %q, want %q", items[0].ID, "0")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(p, []byte("Der Text ist kurz."), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Load(Options{File: p, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "Der Text ist kurz." {
		t.Errorf("text = %q", items[0].Text)
	}
}

// A file wins over positional args, per the documented precedence.
func TestFileBeatsArgs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(p, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Load(Options{File: p, Args: []string{"from", "args"}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "from file" {
		t.Errorf("text = %q, want the file contents", items[0].Text)
	}
}

func TestLoadFromStdin(t *testing.T) {
	items, err := Load(Options{Stdin: strings.NewReader("piped text"), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "piped text" {
		t.Errorf("text = %q", items[0].Text)
	}
}

// "-" means stdin even when a file flag is the thing carrying it.
func TestDashMeansStdin(t *testing.T) {
	items, err := Load(Options{File: "-", Stdin: strings.NewReader("dash stdin"), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "dash stdin" {
		t.Errorf("text = %q", items[0].Text)
	}
}

// The CLI must never block waiting for a human to type.
func TestTerminalStdinWithNoArgsIsAnError(t *testing.T) {
	_, err := Load(Options{StdinIsPipe: pipe(false)})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if got := codeOf(t, err); got != errs.CodeEmptyInput {
		t.Errorf("code = %q, want %q", got, errs.CodeEmptyInput)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(Options{File: filepath.Join(t.TempDir(), "nope.txt"), StdinIsPipe: pipe(false)})
	if got := codeOf(t, err); got != errs.CodeNotFound {
		t.Errorf("code = %q, want %q", got, errs.CodeNotFound)
	}
}

func TestWhitespaceOnlyInputIsEmpty(t *testing.T) {
	_, err := Load(Options{Stdin: strings.NewReader("   \n\t  "), StdinIsPipe: pipe(true)})
	if got := codeOf(t, err); got != errs.CodeEmptyInput {
		t.Errorf("code = %q, want %q", got, errs.CodeEmptyInput)
	}
}

func TestLoadLines(t *testing.T) {
	in := "first line\n\nsecond line\n   \nthird line\n"
	items, err := Load(Options{Format: FormatLines, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (blank lines skipped)", len(items))
	}
	want := []string{"first line", "second line", "third line"}
	for i, w := range want {
		if items[i].Text != w {
			t.Errorf("item %d = %q, want %q", i, items[i].Text, w)
		}
		if items[i].ID != string(rune('0'+i)) {
			t.Errorf("item %d id = %q, want %q", i, items[i].ID, string(rune('0'+i)))
		}
	}
}

func TestLoadJSONL(t *testing.T) {
	in := `{"id":"a1","text":"Erster Absatz.","url":"/a"}
{"id":"b2","text":"Second paragraph.","url":"/b"}
`
	items, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "a1" || items[0].Text != "Erster Absatz." {
		t.Errorf("item 0 = %+v", items[0])
	}
	// Sidecar fields must survive so a batch can be joined back to its source.
	if got := items[0].Fields["url"]; got != "/a" {
		t.Errorf("fields[url] = %v, want /a", got)
	}
	// The text field itself must not be duplicated into Fields.
	if _, dup := items[0].Fields["text"]; dup {
		t.Error("text field leaked into Fields")
	}
}

func TestLoadJSONLCustomFields(t *testing.T) {
	in := `{"slug":"post-1","body":"Der Inhalt."}` + "\n"
	items, err := Load(Options{
		Format: FormatJSONL, TextField: "body", IDField: "slug",
		Stdin: strings.NewReader(in), StdinIsPipe: pipe(true),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "post-1" || items[0].Text != "Der Inhalt." {
		t.Errorf("item = %+v", items[0])
	}
}

// A non-string id (a JSON number) still yields a usable id.
func TestLoadJSONLNumericID(t *testing.T) {
	items, err := Load(Options{
		Format:      FormatJSONL,
		Stdin:       strings.NewReader(`{"id":42,"text":"hi"}` + "\n"),
		StdinIsPipe: pipe(true),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "42" {
		t.Errorf("id = %q, want %q", items[0].ID, "42")
	}
}

// Without an id field, ids fall back to the row index.
func TestLoadJSONLIndexFallback(t *testing.T) {
	in := `{"text":"one"}` + "\n" + `{"text":"two"}` + "\n"
	items, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "0" || items[1].ID != "1" {
		t.Errorf("ids = %q, %q; want 0, 1", items[0].ID, items[1].ID)
	}
}

func TestLoadJSONLErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want errs.Code
	}{
		{"not json", "this is not json\n", errs.CodeInvalidArgs},
		{"missing text field", `{"id":"a"}` + "\n", errs.CodeInvalidArgs},
		{"text not a string", `{"text":123}` + "\n", errs.CodeInvalidArgs},
		{"no rows", "\n\n", errs.CodeEmptyInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(tt.in), StdinIsPipe: pipe(true)})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if got := codeOf(t, err); got != tt.want {
				t.Errorf("code = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownFormat(t *testing.T) {
	_, err := Load(Options{Format: "yaml", Stdin: strings.NewReader("x"), StdinIsPipe: pipe(true)})
	if got := codeOf(t, err); got != errs.CodeInvalidArgs {
		t.Errorf("code = %q, want %q", got, errs.CodeInvalidArgs)
	}
}

func TestTruncation(t *testing.T) {
	items, err := Load(Options{
		Stdin: strings.NewReader("abcdefghij"), StdinIsPipe: pipe(true), MaxBytes: 4,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "abcd" {
		t.Errorf("text = %q, want %q", items[0].Text, "abcd")
	}
	if !items[0].Truncated {
		t.Error("Truncated = false, want true")
	}
}

// Input that exactly fills the cap is not truncated — an off-by-one here would
// mark every exactly-sized document as incomplete.
func TestExactSizeIsNotTruncated(t *testing.T) {
	items, err := Load(Options{
		Stdin: strings.NewReader("abcd"), StdinIsPipe: pipe(true), MaxBytes: 4,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestUTF8Preserved(t *testing.T) {
	const s = "Größe, Straße und Weiß — käme das durch?"
	items, err := Load(Options{Stdin: strings.NewReader(s), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != s {
		t.Errorf("text = %q, want %q", items[0].Text, s)
	}
}
