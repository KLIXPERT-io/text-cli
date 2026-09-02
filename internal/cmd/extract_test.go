package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/strip"
	"github.com/spf13/cobra"
)

// exRun builds a root command with only newExtractCmd wired in and runs it. It
// mirrors dfRun in diff_test.go, except that --output is left empty: this
// command's default format is the thing under test in half these cases.
func exRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("TEXT_LANG", "")

	st := &State{Cfg: config.Default()}
	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", "", "output format")
	pf.StringVar(&st.Lang, "lang", "", "analysis language")
	pf.StringArrayVarP(&st.Files, "file", "f", nil, "read text from a file")
	pf.StringVar(&st.InputFormat, "input-format", "text", "input format")
	pf.StringVar(&st.TextField, "text-field", "text", "JSONL text field")
	pf.StringVar(&st.IDField, "id-field", "id", "JSONL id field")
	pf.Int64Var(&st.MaxBytes, "max-bytes", input.DefaultMaxBytes, "max input size")
	pf.StringVar(&st.Strip, "strip", string(strip.ModeAuto), "strip mode")
	pf.StringVar(&st.From, "from", "", "document format")
	pf.BoolVarP(&st.Quiet, "quiet", "q", false, "suppress warnings")
	root.AddCommand(newExtractCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"extract"}, args...))
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	err := root.Execute()
	return out.String(), err
}

// exFile writes a document to a temp file and returns its path.
func exFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// The markdown a decoder produced is what goes to stdout, unflattened. The
// whole reason this command exists is to show what the analysis commands were
// handed, so a heading it prints must still be a heading.
func TestExtractPrintsTheDocument(t *testing.T) {
	tests := []struct {
		name string
		body string
		args []string
		want string
	}{
		{
			name: "markdown is printed as it stands",
			body: "# Titel\n\nEin Satz.\n",
			want: "# Titel\n\nEin Satz.\n",
		},
		{
			// No --strip on the command line means no strip pass: the default
			// value of the flag is auto, and honouring it here would flatten the
			// document this command was asked to show.
			name: "an unchanged --strip flag does not flatten",
			body: "## Überschrift\n\n- Ein Punkt\n",
			want: "## Überschrift\n\n- Ein Punkt\n",
		},
		{
			// ...and asking for it explicitly is how you get the prose form.
			name: "an explicit --strip flattens to prose",
			body: "## Überschrift\n\nEin Satz.\n",
			args: []string{"--strip", "auto"},
			want: "Überschrift.\n\nEin Satz.\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := exFile(t, "doc.md", tc.body)
			out, err := exRun(t, append([]string{p}, tc.args...)...)
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			if out != tc.want {
				t.Errorf("stdout = %q, want %q", out, tc.want)
			}
		})
	}
}

// The command's payload is text, so it defaults to text — a JSON envelope on
// stdout would defeat the pipe it exists to feed. Naming a format still wins.
func TestExtractOutputFormat(t *testing.T) {
	p := exFile(t, "doc.md", "# Titel\n\nEin Satz.\n")

	out, err := exRun(t, p)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("default output is JSON, want the document itself: %q", out)
	}

	out, err = exRun(t, p, "--output", "json")
	if err != nil {
		t.Fatalf("extract --output json: %v", err)
	}
	var env struct {
		Data struct {
			File    string `json:"file"`
			Content string `json:"content"`
			Chars   int    `json:"chars"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if env.Data.File != p {
		// "0" is right for a measurement and names nothing here.
		t.Errorf("file = %q, want %q", env.Data.File, p)
	}
	if env.Data.Content != "# Titel\n\nEin Satz.\n" {
		t.Errorf("content = %q", env.Data.Content)
	}
	if env.Data.Chars != len([]rune(env.Data.Content)) {
		t.Errorf("chars = %d, want %d", env.Data.Chars, len([]rune(env.Data.Content)))
	}
}

func TestExtractOut(t *testing.T) {
	dir := t.TempDir()
	src := exFile(t, "doc.md", "# Titel\n\nEin Satz.\n")
	second := exFile(t, "zweit.md", "# Zwei\n\nNoch ein Satz.\n")

	t.Run("writes the document to the named path", func(t *testing.T) {
		dst := filepath.Join(dir, "out.md")
		if _, err := exRun(t, src, "--out", dst); err != nil {
			t.Fatalf("extract --out: %v", err)
		}
		b, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if string(b) != "# Titel\n\nEin Satz.\n" {
			t.Errorf("file = %q", b)
		}
	})

	t.Run("refuses to overwrite without force", func(t *testing.T) {
		dst := filepath.Join(dir, "taken.md")
		if err := os.WriteFile(dst, []byte("meine Notizen\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := exRun(t, src, "--out", dst)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		var e *errs.E
		if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
			t.Errorf("err = %v, want invalid_args", err)
		}
		b, _ := os.ReadFile(dst)
		if string(b) != "meine Notizen\n" {
			t.Errorf("the existing file was overwritten: %q", b)
		}
	})

	t.Run("force overwrites", func(t *testing.T) {
		dst := filepath.Join(dir, "forced.md")
		if err := os.WriteFile(dst, []byte("alt\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := exRun(t, src, "--out", dst, "--force"); err != nil {
			t.Fatalf("extract --force: %v", err)
		}
		b, _ := os.ReadFile(dst)
		if string(b) != "# Titel\n\nEin Satz.\n" {
			t.Errorf("file = %q", b)
		}
	})

	t.Run("a directory takes one file per document", func(t *testing.T) {
		outDir := filepath.Join(dir, "many")
		if err := os.Mkdir(outDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := exRun(t, src, second, "--out", outDir); err != nil {
			t.Fatalf("extract --out dir: %v", err)
		}
		for _, name := range []string{"doc.md", "zweit.md"} {
			if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("several documents into one file is refused", func(t *testing.T) {
		dst := filepath.Join(dir, "collision.md")
		_, err := exRun(t, src, second, "--out", dst)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if _, statErr := os.Stat(dst); statErr == nil {
			t.Error("a file was written for an ambiguous --out")
		}
	})
}

func TestExtractName(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"a path keeps its stem", "/tmp/report.pdf", "report.md"},
		{"a bare name works too", "notes.docx", "notes.md"},
		{"an extensionless file gains one", "/tmp/README", "README.md"},
		// A URL has no filename: the last path segment is the best name it
		// offers, and "https:.md" is not a filename.
		{"a url uses its last segment", "https://example.com/a/b", "b.md"},
		{"a bare url falls back to the host", "https://example.com/", "example.com.md"},
		{"stdin falls back", "stdin", "stdin.md"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractName(tc.id); got != tc.want {
				t.Errorf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractLabels(t *testing.T) {
	tests := []struct {
		name  string
		state State
		n     int
		want  []string
	}{
		{"one file", State{Files: []string{"a.pdf"}}, 1, []string{"a.pdf"}},
		{"two files", State{Files: []string{"a.pdf", "b.docx"}}, 2, []string{"a.pdf", "b.docx"}},
		{"a dash is stdin", State{Files: []string{"-"}}, 1, []string{"stdin"}},
		{"no source at all is stdin", State{}, 1, []string{"stdin"}},
		// One file, many documents: --input-format lines. Labelling every line
		// with the filename would claim they came from different places.
		{"more documents than files", State{Files: []string{"a.txt"}}, 3, nil},
		// A fetched page is already keyed by its URL.
		{"a url keeps the item id", State{URLs: []string{"https://example.com"}}, 1, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLabels(&tc.state, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("= %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
