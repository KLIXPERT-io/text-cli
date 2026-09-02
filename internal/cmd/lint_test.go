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
	"github.com/KLIXPERT-io/text-cli/internal/lint"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// A paragraph of German administrative prose: passive, Substantivstil and
// Behördendeutsch in one place, which is what the command exists for.
const ltGermanSample = "Im Rahmen der Durchführung des Projektes wird seitens der Abteilung eine " +
	"umfassende Überprüfung der bestehenden Prozesse vorgenommen, damit die Einhaltung der " +
	"gesetzlichen Anforderungen sichergestellt werden kann. Hinsichtlich der Inanspruchnahme " +
	"externer Dienstleister wurde eine Entscheidung noch nicht getroffen. Die Bearbeitung der " +
	"Anträge erfolgt unter Berücksichtigung der Vorgaben."

const ltEnglishSample = "The report was written by the committee in order to provide an evaluation " +
	"of the implementation of the requirements. The decision was taken quickly."

// ltClean is short, plain, and must produce nothing at all.
const ltClean = "Der Hund läuft. Die Katze schläft."

// ltRun builds a root command with only the persistent flags the lint command
// depends on, runs it, and returns stdout. It skips the real PersistentPreRunE
// so a test never reads the developer's config file.
func ltRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("TEXT_LANG", "")

	st := &State{Cfg: config.Default()}
	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", "json", "output format")
	pf.StringVar(&st.Lang, "lang", "", "analysis language")
	pf.StringArrayVarP(&st.Files, "file", "f", nil, "read text from a file")
	pf.StringVar(&st.InputFormat, "input-format", "text", "input format")
	pf.StringVar(&st.TextField, "text-field", "text", "JSONL text field")
	pf.StringVar(&st.IDField, "id-field", "id", "JSONL id field")
	pf.Int64Var(&st.MaxBytes, "max-bytes", input.DefaultMaxBytes, "max input size")
	root.AddCommand(newLintCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	err := root.Execute()
	return out.String(), err
}

func ltWriteTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func ltRequireCode(t *testing.T, err error, want errs.Code, wantExit int) *errs.E {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T (%v), want *errs.E", err, err)
	}
	if e.Code != want {
		t.Fatalf("code = %q, want %q (message: %s)", e.Code, want, e.Message)
	}
	if got := errs.ExitCode(err); got != wantExit {
		t.Fatalf("exit code = %d, want %d", got, wantExit)
	}
	if e.Hint == "" {
		t.Fatal("error carries no hint; the caller cannot recover")
	}
	return e
}

type ltEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta output.Meta     `json:"meta"`
}

type ltDocPayload struct {
	ID               string         `json:"id"`
	Language         string         `json:"language"`
	LanguageDetected bool           `json:"language_detected"`
	Findings         []lint.Finding `json:"findings"`
	Summary          map[string]int `json:"summary"`
	Total            int            `json:"total"`
}

func ltDecode(t *testing.T, s string) ltEnvelope {
	t.Helper()
	var env ltEnvelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, s)
	}
	return env
}

func ltSingle(t *testing.T, out string) (ltDocPayload, output.Meta) {
	t.Helper()
	env := ltDecode(t, out)
	var doc ltDocPayload
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode data: %v\n%s", err, env.Data)
	}
	return doc, env.Meta
}

// The single-document JSON shape is contractual: an agent consuming the work
// list is written against exactly these keys.
func TestLintSingleDocumentJSON(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, meta := ltSingle(t, out)

	if doc.ID != "0" || doc.Language != "de" {
		t.Fatalf("id/language = %q/%q", doc.ID, doc.Language)
	}
	if doc.LanguageDetected {
		t.Fatal("language_detected should be false when --lang was given")
	}
	if doc.Total != len(doc.Findings) || doc.Total == 0 {
		t.Fatalf("total = %d, findings = %d", doc.Total, len(doc.Findings))
	}
	for _, rule := range []string{"passive", "nominalization", "bureaucratic"} {
		if doc.Summary[rule] == 0 {
			t.Fatalf("summary is missing %q: %v", rule, doc.Summary)
		}
	}
	sum := 0
	for _, n := range doc.Summary {
		sum += n
	}
	if sum != doc.Total {
		t.Fatalf("summary sums to %d but total is %d", sum, doc.Total)
	}
	for i, f := range doc.Findings {
		if f.Rule == "" || f.Severity == "" || f.Message == "" {
			t.Fatalf("finding %d is incomplete: %+v", i, f)
		}
		if got := ltGermanSample[f.Start:f.End]; got != f.Excerpt {
			t.Fatalf("finding %d: text[%d:%d] = %q, excerpt = %q", i, f.Start, f.End, got, f.Excerpt)
		}
	}
	if meta.Documents != 1 || meta.Language != "de" {
		t.Fatalf("meta = %+v", meta)
	}
}

// A clean document is a valid answer, not an empty one: findings must still be
// an array so a consumer never special-cases null.
func TestLintCleanDocument(t *testing.T) {
	f := ltWriteTemp(t, "clean.txt", ltClean)
	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, _ := ltSingle(t, out)
	if doc.Total != 0 || len(doc.Findings) != 0 {
		t.Fatalf("clean document produced %d findings: %+v", doc.Total, doc.Findings)
	}
	if !strings.Contains(out, `"findings": []`) {
		t.Fatalf("findings must serialize as an empty array:\n%s", out)
	}
}

func TestLintBatchResolvesRulesPerDocument(t *testing.T) {
	lines := strings.Join([]string{
		`{"id":"de-1","text":` + ltMustJSON(ltGermanSample) + `,"slug":"projekt"}`,
		`{"id":"en-1","text":` + ltMustJSON(ltEnglishSample) + `,"slug":"report"}`,
	}, "\n") + "\n"
	f := ltWriteTemp(t, "batch.jsonl", lines)

	out, err := ltRun(t, "lint", "--file", f, "--input-format", "jsonl", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	env := ltDecode(t, out)
	var data struct {
		Documents []ltDocPayload `json:"documents"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v\n%s", err, env.Data)
	}
	if len(data.Documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(data.Documents))
	}
	de, en := data.Documents[0], data.Documents[1]
	if de.ID != "de-1" || de.Language != "de" || en.ID != "en-1" || en.Language != "en" {
		t.Fatalf("documents = %+v / %+v", de, en)
	}
	if de.Summary["bureaucratic"] == 0 {
		t.Fatalf("the German rules did not run on the German row: %v", de.Summary)
	}
	if en.Summary["bureaucratic"] != 0 || en.Summary["modal-hedge"] != 0 {
		t.Fatalf("German-only rules ran on the English row: %v", en.Summary)
	}
	if en.Summary["passive"] == 0 {
		t.Fatalf("the English rules did not run on the English row: %v", en.Summary)
	}
	if env.Meta.Documents != 2 {
		t.Fatalf("meta.documents = %d, want 2", env.Meta.Documents)
	}
	if env.Meta.Language != "" {
		t.Fatalf("meta.language = %q, want empty for a mixed batch", env.Meta.Language)
	}
}

func TestLintCSVColumns(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "csv")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := "doc_id,rule,severity,sentence,start,end,value,excerpt,message"
	if lines[0] != want {
		t.Fatalf("csv header = %q, want %q", lines[0], want)
	}
	if len(lines) < 5 {
		t.Fatalf("csv has %d lines, want a row per finding:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[1], "0,") {
		t.Fatalf("csv row is not keyed by the document id: %q", lines[1])
	}
}

// NDJSON is the streaming form: one finding per line, carrying the document id
// and the source's sidecar fields so the output can be joined back.
func TestLintNDJSONOneFindingPerLine(t *testing.T) {
	lines := `{"id":"a","text":` + ltMustJSON(ltGermanSample) + `,"slug":"projekt"}` + "\n"
	f := ltWriteTemp(t, "one.jsonl", lines)

	out, err := ltRun(t, "lint", "--file", f, "--input-format", "jsonl", "--lang", "de", "--output", "ndjson")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		n++
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode ndjson line: %v\n%s", err, line)
		}
		if rec["doc_id"] != "a" || rec["slug"] != "projekt" {
			t.Fatalf("record lost its identity: %v", rec)
		}
		if rec["rule"] == "" || rec["message"] == "" {
			t.Fatalf("record is not a finding: %v", rec)
		}
	}
	if n < 5 {
		t.Fatalf("ndjson lines = %d, want one per finding", n)
	}
}

func TestLintTextOutputGroupsByRule(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "text")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	if !strings.Contains(out, "passive (") || !strings.Contains(out, "bureaucratic (") {
		t.Fatalf("text output is not grouped by rule:\n%s", out)
	}
	if !strings.Contains(out, " — ") {
		t.Fatalf("text output does not pair the excerpt with the message:\n%s", out)
	}
	if !strings.Contains(out, "finding(s)") {
		t.Fatalf("text output has no total:\n%s", out)
	}

	clean := ltWriteTemp(t, "clean.txt", ltClean)
	out, err = ltRun(t, "lint", "--file", clean, "--lang", "de", "--output", "text")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "no findings") {
		t.Fatalf("a clean document should say so:\n%s", out)
	}
}

func TestLintRulesSelection(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)

	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "passive,bureaucratic", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, _ := ltSingle(t, out)
	for rule := range doc.Summary {
		if rule != "passive" && rule != "bureaucratic" {
			t.Fatalf("--rules selected %q as well: %v", rule, doc.Summary)
		}
	}
	if doc.Summary["passive"] == 0 || doc.Summary["bureaucratic"] == 0 {
		t.Fatalf("summary = %v, want both selected rules", doc.Summary)
	}

	// An unknown rule fails with a hint that lists the real ones.
	_, err = ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "passiv")
	e := ltRequireCode(t, err, errs.CodeInvalidArgs, 5)
	if !strings.Contains(e.Hint, "passive") || !strings.Contains(e.Hint, "text lint rules") {
		t.Fatalf("hint should list the known rules and point at the listing, got %q", e.Hint)
	}

	// A rule that exists but not for this language explains itself.
	_, err = ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "adverb")
	e = ltRequireCode(t, err, errs.CodeUnsupportedLanguage, 5)
	if !strings.Contains(e.Hint, "modal-hedge") {
		t.Fatalf("hint should name the rules that do support de, got %q", e.Hint)
	}

	// --rules all behaves like auto: everything valid for the language.
	all, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "all", "--output", "json")
	if err != nil {
		t.Fatalf("lint --rules all: %v\n%s", err, all)
	}
	auto, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "auto", "--output", "json")
	if err != nil {
		t.Fatalf("lint --rules auto: %v\n%s", err, auto)
	}
	if all != auto {
		t.Fatal("--rules all and --rules auto disagree")
	}
}

func TestLintSeverityFilter(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	info, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, info)
	}
	warn, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--severity", "warn", "--output", "json")
	if err != nil {
		t.Fatalf("lint --severity warn: %v\n%s", err, warn)
	}
	all, _ := ltSingle(t, info)
	only, _ := ltSingle(t, warn)
	if only.Total >= all.Total {
		t.Fatalf("--severity warn kept %d of %d findings", only.Total, all.Total)
	}
	if only.Total == 0 {
		t.Fatal("--severity warn dropped everything; the German sample has warnings")
	}
	for _, f := range only.Findings {
		if f.Severity != lint.SeverityWarn {
			t.Fatalf("severity filter let %q through", f.Severity)
		}
	}

	_, err = ltRun(t, "lint", "--file", f, "--lang", "de", "--severity", "fatal")
	ltRequireCode(t, err, errs.CodeInvalidArgs, 5)
}

func TestLintThresholdOverrides(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)

	// A limit of five words makes every sentence long.
	out, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "long-sentence",
		"--max-sentence-words", "5", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, _ := ltSingle(t, out)
	if doc.Summary["long-sentence"] != 3 {
		t.Fatalf("long-sentence = %d, want one per sentence: %v", doc.Summary["long-sentence"], doc.Summary)
	}

	// --worst caps the hard-sentence list.
	out, err = ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "hard-sentence",
		"--worst", "1", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, _ = ltSingle(t, out)
	if doc.Total != 1 {
		t.Fatalf("--worst 1 produced %d findings", doc.Total)
	}

	// --max-word-chars 8 flags the compounds.
	out, err = ltRun(t, "lint", "--file", f, "--lang", "de", "--rules", "long-word",
		"--max-word-chars", "12", "--output", "json")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, out)
	}
	doc, _ = ltSingle(t, out)
	if doc.Total == 0 {
		t.Fatal("--max-word-chars 12 found no long words in a text full of compounds")
	}
	for _, f := range doc.Findings {
		if f.Value <= 12 {
			t.Fatalf("finding %q has %v characters", f.Excerpt, f.Value)
		}
	}
}

// The CI gate: the findings are still printed, and the exit code is non-zero.
func TestLintFailOnFindings(t *testing.T) {
	dirty := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	out, err := ltRun(t, "lint", "--file", dirty, "--lang", "de", "--fail-on-findings", "--output", "json")
	ltRequireCode(t, err, errs.CodeGeneric, 1)
	if !strings.Contains(out, `"findings"`) {
		t.Fatalf("the gate swallowed the report:\n%s", out)
	}

	clean := ltWriteTemp(t, "clean.txt", ltClean)
	if out, err := ltRun(t, "lint", "--file", clean, "--lang", "de", "--fail-on-findings", "--output", "json"); err != nil {
		t.Fatalf("a clean document must pass the gate: %v\n%s", err, out)
	}

	// Raising the severity is the documented way past a noisy gate.
	if _, err := ltRun(t, "lint", "--file", dirty, "--lang", "de", "--rules", "repeated-word",
		"--severity", "warn", "--fail-on-findings"); err != nil {
		t.Fatalf("no warnings should mean no failure: %v", err)
	}
}

// Output that reorders between runs is unusable in CI, so this is a contract.
func TestLintOutputIsDeterministic(t *testing.T) {
	f := ltWriteTemp(t, "doc.de.txt", ltGermanSample)
	first, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "csv")
	if err != nil {
		t.Fatalf("lint: %v\n%s", err, first)
	}
	for i := 0; i < 4; i++ {
		again, err := ltRun(t, "lint", "--file", f, "--lang", "de", "--output", "csv")
		if err != nil {
			t.Fatalf("lint: %v", err)
		}
		if again != first {
			t.Fatalf("run %d differs from run 1:\n%s\n---\n%s", i+2, first, again)
		}
	}
	// And the rows are sorted by offset.
	prev := -1
	for _, line := range strings.Split(strings.TrimSpace(first), "\n")[1:] {
		cols := strings.Split(line, ",")
		start := ltAtoi(t, cols[4])
		if start < prev {
			t.Fatalf("row out of order: start %d after %d", start, prev)
		}
		prev = start
	}
}

// The rule listing is discovery: an agent reads it to find out what this binary
// can check, so it is built from the registry.
func TestLintRulesListing(t *testing.T) {
	out, err := ltRun(t, "lint", "rules", "--output", "json")
	if err != nil {
		t.Fatalf("lint rules: %v\n%s", err, out)
	}
	var env struct {
		Data struct {
			Rules []struct {
				Name        string   `json:"name"`
				Title       string   `json:"title"`
				Languages   []string `json:"languages"`
				Severity    string   `json:"severity"`
				Description string   `json:"description"`
			} `json:"rules"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(env.Data.Rules) < 11 {
		t.Fatalf("rules list has %d entries", len(env.Data.Rules))
	}
	seen := map[string]bool{}
	for _, r := range env.Data.Rules {
		if r.Name == "" || r.Title == "" || r.Description == "" || len(r.Languages) == 0 || r.Severity == "" {
			t.Fatalf("incomplete discovery record: %+v", r)
		}
		seen[r.Name] = true
	}
	for _, want := range []string{"long-sentence", "hard-sentence", "passive", "nominalization", "bureaucratic"} {
		if !seen[want] {
			t.Fatalf("rule %q is missing from the listing", want)
		}
	}

	if out, err := ltRun(t, "lint", "rules", "--output", "text"); err != nil || !strings.Contains(out, "long-sentence") {
		t.Fatalf("text listing = %q (%v)", out, err)
	}
}

func ltMustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func ltAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}
