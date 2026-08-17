package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/analyze/readability"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// dfRun builds a root command with only newDiffCmd wired in, runs it, and
// returns stdout. It mirrors rdRun in readability_test.go.
func dfRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	t.Setenv("TEXT_LANG", "")

	st := &State{Cfg: config.Default()}
	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", "json", "output format")
	pf.StringVar(&st.Lang, "lang", "", "analysis language")
	pf.StringVarP(&st.File, "file", "f", "", "read text from a file")
	pf.StringVar(&st.InputFormat, "input-format", "text", "input format")
	pf.StringVar(&st.TextField, "text-field", "text", "JSONL text field")
	pf.StringVar(&st.IDField, "id-field", "id", "JSONL id field")
	pf.Int64Var(&st.MaxBytes, "max-bytes", input.DefaultMaxBytes, "max input size")
	pf.BoolVarP(&st.Quiet, "quiet", "q", false, "suppress warnings")
	root.AddCommand(newDiffCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	err := root.Execute()
	return out.String(), err
}

// dfRunSplit is like dfRun but keeps stdout and stderr apart, so a warning on
// stderr can be asserted without scanning the JSON body for it.
func dfRunSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("TEXT_LANG", "")

	st := &State{Cfg: config.Default()}
	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", "json", "output format")
	pf.StringVar(&st.Lang, "lang", "", "analysis language")
	pf.StringVarP(&st.File, "file", "f", "", "read text from a file")
	pf.StringVar(&st.InputFormat, "input-format", "text", "input format")
	pf.StringVar(&st.TextField, "text-field", "text", "JSONL text field")
	pf.StringVar(&st.IDField, "id-field", "id", "JSONL id field")
	pf.Int64Var(&st.MaxBytes, "max-bytes", input.DefaultMaxBytes, "max input size")
	pf.BoolVarP(&st.Quiet, "quiet", "q", false, "suppress warnings")
	root.AddCommand(newDiffCmd())

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func dfWriteTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func dfRequireCode(t *testing.T, err error, want errs.Code) {
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
	if e.Hint == "" {
		t.Fatal("error carries no hint; the caller cannot recover")
	}
}

type dfEnvelope struct {
	Data diffData `json:"data"`
}

func dfDecode(t *testing.T, out string) diffData {
	t.Helper()
	var env dfEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, out)
	}
	return env.Data
}

func dfMetric(t *testing.T, data diffData, name string) diffMetricRow {
	t.Helper()
	for _, m := range data.Metrics {
		if m.Metric == name {
			return m
		}
	}
	t.Fatalf("no metric %q in %+v", name, data.Metrics)
	return diffMetricRow{}
}

// dfAmstadScore independently recomputes the Amstad formula from raw text, so
// the exact delta arithmetic asserted below is checked against a computation
// path that never touches diff.go or amstad.go's own rounding.
func dfAmstadScore(t *testing.T, text string) float64 {
	t.Helper()
	d, err := textproc.Analyze(text, textproc.LangGerman)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	raw := 180 - d.Stats.AvgSentenceLength - 58.5*d.Stats.AvgSyllablesPerWord
	return math.Round(raw*10) / 10
}

const (
	// dfGermanHard is dense, long-sentence German prose: long compound-ish
	// words, few sentence breaks.
	dfGermanHard = "Die Implementierung der Datenverarbeitungsinfrastruktur erfordert eine umfassende " +
		"Berücksichtigung sämtlicher Randbedingungen, welche sich aus den unterschiedlichen " +
		"Anforderungen der beteiligten Fachabteilungen ergeben und kontinuierlich angepasst werden müssen."
	// dfGermanEasy says the same kind of thing in short, plain sentences.
	dfGermanEasy = "Wir bauen ein neues System. Es soll Daten speichern. Jede Abteilung kann es nutzen. " +
		"Wir passen es bei Bedarf an."
)

// A rewrite that simplifies German prose must show up as improved:true for
// Amstad (higher-is-easier), with the delta matching an independently
// computed before/after score exactly.
func TestDiffAmstadImprovement(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanHard)
	after := dfWriteTemp(t, "after.md", dfGermanEasy)

	out, err := dfRun(t, "diff", before, after, "--lang", "de", "--metrics", "amstad", "--output", "json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	data := dfDecode(t, out)
	m := dfMetric(t, data, "amstad")

	wantBefore := dfAmstadScore(t, dfGermanHard)
	wantAfter := dfAmstadScore(t, dfGermanEasy)
	wantDelta := math.Round((wantAfter-wantBefore)*10) / 10

	if m.Before != wantBefore {
		t.Fatalf("before = %v, want %v", m.Before, wantBefore)
	}
	if m.After != wantAfter {
		t.Fatalf("after = %v, want %v", m.After, wantAfter)
	}
	if m.Delta != wantDelta {
		t.Fatalf("delta = %v, want %v (exact arithmetic)", m.Delta, wantDelta)
	}
	if wantAfter <= wantBefore {
		t.Fatalf("test fixture is not actually an improvement: before=%v after=%v", wantBefore, wantAfter)
	}
	if !m.Improved {
		t.Fatalf("improved = false, want true for a simplifying rewrite (before=%v after=%v delta=%v)",
			m.Before, m.After, m.Delta)
	}
	if m.Direction != "higher-is-easier" {
		t.Fatalf("direction = %q, want higher-is-easier", m.Direction)
	}
}

// The inverse: a rewrite that makes German prose harder must report
// improved:false for Amstad.
func TestDiffAmstadRegression(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanEasy)
	after := dfWriteTemp(t, "after.md", dfGermanHard)

	out, err := dfRun(t, "diff", before, after, "--lang", "de", "--metrics", "amstad", "--output", "json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	m := dfMetric(t, dfDecode(t, out), "amstad")

	if m.Delta >= 0 {
		t.Fatalf("test fixture did not make the score fall: delta=%v", m.Delta)
	}
	if m.Improved {
		t.Fatalf("improved = true, want false for a text that got harder (before=%v after=%v delta=%v)",
			m.Before, m.After, m.Delta)
	}
}

const (
	// dfLIXHard is one long sentence stuffed with long (>6 character) words,
	// so LIX (words/sentence + 100×longWords/words) reads high.
	dfLIXHard = "The extraordinarily comprehensive documentation encompasses numerous " +
		"interdisciplinary methodological considerations regarding organizational " +
		"infrastructure implementations throughout multinational collaborative " +
		"environments requiring substantial administrative coordination."
	// dfLIXEasy is short sentences of short words, so LIX reads low.
	dfLIXEasy = "We build simple tools. They help teams. Work gets done fast."
)

// The regression test for the direction logic: a lower-is-easier metric
// (LIX, real and already registered — see internal/analyze/readability/lix.go)
// whose score goes DOWN must report improved:true. Getting this backwards
// (treating a falling score as a regression) would make the whole command
// misleading, so this must never be skipped.
func TestDiffLowerIsEasierDirection(t *testing.T) {
	before := dfWriteTemp(t, "before.txt", dfLIXHard)
	after := dfWriteTemp(t, "after.txt", dfLIXEasy)

	out, err := dfRun(t, "diff", before, after, "--lang", "en", "--metrics", "lix", "--output", "json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	m := dfMetric(t, dfDecode(t, out), "lix")

	// Independently compute the expected scores by calling the real LIX
	// formula directly, the same one diff.go reaches through the registry.
	beforeDoc, err := textproc.Analyze(dfLIXHard, textproc.LangEnglish)
	if err != nil {
		t.Fatalf("Analyze before: %v", err)
	}
	afterDoc, err := textproc.Analyze(dfLIXEasy, textproc.LangEnglish)
	if err != nil {
		t.Fatalf("Analyze after: %v", err)
	}
	wantBefore, err := readability.LIX(beforeDoc)
	if err != nil {
		t.Fatalf("LIX before: %v", err)
	}
	wantAfter, err := readability.LIX(afterDoc)
	if err != nil {
		t.Fatalf("LIX after: %v", err)
	}
	if wantAfter.Score >= wantBefore.Score {
		t.Fatalf("test fixture does not actually lower the LIX score: before=%v after=%v", wantBefore.Score, wantAfter.Score)
	}

	if m.Before != wantBefore.Score {
		t.Fatalf("before = %v, want %v", m.Before, wantBefore.Score)
	}
	if m.After != wantAfter.Score {
		t.Fatalf("after = %v, want %v", m.After, wantAfter.Score)
	}
	if m.Direction != "lower-is-easier" {
		t.Fatalf("direction = %q, want lower-is-easier (Scale said so)", m.Direction)
	}
	if m.Delta >= 0 {
		t.Fatalf("delta = %v, want negative (score fell)", m.Delta)
	}
	if !m.Improved {
		t.Fatalf("improved = false, want true: a falling score on a lower-is-easier metric IS the improvement")
	}
}

func TestDiffWrongArgCount(t *testing.T) {
	f := dfWriteTemp(t, "a.md", "Some text here. More text follows.")

	for _, args := range [][]string{
		{"diff"},
		{"diff", f},
		{"diff", f, f, f},
	} {
		_, err := dfRun(t, args...)
		dfRequireCode(t, err, errs.CodeInvalidArgs)
	}
}

func TestDiffBothStdinIsInvalid(t *testing.T) {
	_, err := dfRun(t, "diff", "-", "-")
	dfRequireCode(t, err, errs.CodeInvalidArgs)
}

// Mixed languages must be surfaced, not hidden: both languages appear in the
// output, and a warning goes to stderr unless --quiet.
func TestDiffMixedLanguages(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanEasy)
	after := dfWriteTemp(t, "after.md",
		"The dog runs fast across the street. The cat sleeps on the sofa. "+
			"We are going to the movies tonight because the weather is bad.")

	out, stderr, err := dfRunSplit(t, "diff", before, after, "--output", "json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	data := dfDecode(t, out)
	if data.Before.Language != "de" {
		t.Fatalf("before.language = %q, want de", data.Before.Language)
	}
	if data.After.Language != "en" {
		t.Fatalf("after.language = %q, want en", data.After.Language)
	}
	if !strings.Contains(stderr, "de") || !strings.Contains(stderr, "en") {
		t.Fatalf("stderr should warn about both languages, got: %q", stderr)
	}

	// --quiet suppresses the warning but not the mixed-language data.
	_, stderrQuiet, err := dfRunSplit(t, "diff", before, after, "--output", "json", "--quiet")
	if err != nil {
		t.Fatalf("diff --quiet: %v", err)
	}
	if strings.TrimSpace(stderrQuiet) != "" {
		t.Fatalf("--quiet should suppress the warning, got: %q", stderrQuiet)
	}
}

// An empty document must fail on the same empty-input path every other
// command uses, not a diff-specific one.
func TestDiffEmptyDocument(t *testing.T) {
	before := dfWriteTemp(t, "before.md", "   \n  \n")
	after := dfWriteTemp(t, "after.md", "Some real prose goes here for the after side.")

	_, err := dfRun(t, "diff", before, after)
	dfRequireCode(t, err, errs.CodeEmptyInput)
}

// stats_delta reports after-minus-before, with the sign meaning growth, not
// improvement (that's the metrics' job).
func TestDiffStatsDelta(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanHard)
	after := dfWriteTemp(t, "after.md", dfGermanEasy)

	out, err := dfRun(t, "diff", before, after, "--lang", "de", "--metrics", "amstad", "--output", "json")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	data := dfDecode(t, out)

	beforeWords := data.Before.Stats.Words
	afterWords := data.After.Stats.Words
	wantWordsDelta, ok := data.StatsDelta["words"].(float64)
	if !ok {
		t.Fatalf("stats_delta.words missing or wrong type: %v", data.StatsDelta["words"])
	}
	if int(wantWordsDelta) != afterWords-beforeWords {
		t.Fatalf("stats_delta.words = %v, want %d", wantWordsDelta, afterWords-beforeWords)
	}
}

// --output text renders a compact block with an improvement arrow per metric.
func TestDiffTextOutput(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanHard)
	after := dfWriteTemp(t, "after.md", dfGermanEasy)

	out, err := dfRun(t, "diff", before, after, "--lang", "de", "--metrics", "amstad", "--output", "text")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	if !strings.Contains(out, "before:") || !strings.Contains(out, "after:") {
		t.Fatalf("text output missing before/after lines:\n%s", out)
	}
	if !strings.Contains(out, "amstad") {
		t.Fatalf("text output missing the metric line:\n%s", out)
	}
	if !strings.Contains(out, "↑") {
		t.Fatalf("text output should show an improvement arrow:\n%s", out)
	}
}

// CSV/table share the same fixed column set.
func TestDiffCSVColumns(t *testing.T) {
	before := dfWriteTemp(t, "before.md", dfGermanHard)
	after := dfWriteTemp(t, "after.md", dfGermanEasy)

	out, err := dfRun(t, "diff", before, after, "--lang", "de", "--metrics", "amstad", "--output", "csv")
	if err != nil {
		t.Fatalf("diff: %v\n%s", err, out)
	}
	header := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	want := "metric,before,after,delta,improved,level_before,level_after"
	if header != want {
		t.Fatalf("csv header = %q, want %q", header, want)
	}
}
