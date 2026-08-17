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
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

const (
	rdEnglishSample = "The dog runs fast across the street. The cat sleeps on the sofa. " +
		"We are going to the movies tonight because the weather is bad."
	rdGermanSample = "Der Hund läuft schnell über die Straße. Die Katze schläft auf dem Sofa. " +
		"Wir gehen heute Abend ins Kino, weil das Wetter schlecht ist."
)

// rdRun builds a root command with only the persistent flags the readability
// command depends on, runs it, and returns stdout. It deliberately skips the
// real PersistentPreRunE so a test never reads the developer's config file.
func rdRun(t *testing.T, args ...string) (string, error) {
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
	root.AddCommand(newReadabilityCmd(), newMetricsCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	err := root.Execute()
	return out.String(), err
}

func rdWriteTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

type rdEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta output.Meta     `json:"meta"`
}

func rdDecodeEnvelope(t *testing.T, s string) rdEnvelope {
	t.Helper()
	var env rdEnvelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		t.Fatalf("decode envelope: %v\n%s", err, s)
	}
	return env
}

func rdRequireCode(t *testing.T, err error, want errs.Code, wantExit int) {
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
}

// The single-document JSON shape is contractual: the README and the agent skill
// are written against exactly these keys.
func TestReadabilitySingleDocumentJSON(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--output", "json")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	env := rdDecodeEnvelope(t, out)

	var doc struct {
		ID               string `json:"id"`
		Language         string `json:"language"`
		LanguageDetected bool   `json:"language_detected"`
		Stats            *struct {
			Sentences           int     `json:"sentences"`
			Words               int     `json:"words"`
			Syllables           int     `json:"syllables"`
			AvgSentenceLength   float64 `json:"avg_sentence_length"`
			AvgSyllablesPerWord float64 `json:"avg_syllables_per_word"`
		} `json:"stats"`
		Metrics []struct {
			Metric   string         `json:"metric"`
			Title    string         `json:"title"`
			Score    float64        `json:"score"`
			Level    string         `json:"level"`
			Grade    string         `json:"grade"`
			Scale    string         `json:"scale"`
			Language string         `json:"language"`
			Extra    map[string]any `json:"extra"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(env.Data, &doc); err != nil {
		t.Fatalf("decode data: %v\n%s", err, env.Data)
	}
	if doc.ID != "0" {
		t.Fatalf("id = %q, want 0", doc.ID)
	}
	if doc.Language != "en" {
		t.Fatalf("language = %q, want en", doc.Language)
	}
	if doc.LanguageDetected {
		t.Fatal("language_detected should be false when --lang was given")
	}
	if doc.Stats == nil || doc.Stats.Words == 0 || doc.Stats.Sentences != 3 {
		t.Fatalf("stats = %+v, want 3 sentences and some words", doc.Stats)
	}
	if len(doc.Metrics) != 1 {
		t.Fatalf("metrics = %+v, want just flesch", doc.Metrics)
	}
	m := doc.Metrics[0]
	if m.Metric != "flesch" || m.Title != "Flesch Reading Ease" || m.Language != "en" {
		t.Fatalf("metric = %+v", m)
	}
	if m.Level == "" || m.Grade == "" || m.Scale != "0–100, higher is easier" {
		t.Fatalf("level/grade/scale = %q/%q/%q", m.Level, m.Grade, m.Scale)
	}
	for _, k := range []string{"asl", "asw", "words", "sentences", "syllables"} {
		if _, ok := m.Extra[k]; !ok {
			t.Fatalf("extra is missing %q: %v", k, m.Extra)
		}
	}
	if env.Meta.Documents != 1 || env.Meta.Language != "en" || env.Meta.APICalls != 0 || env.Meta.Cached {
		t.Fatalf("meta = %+v", env.Meta)
	}
	if !strings.Contains(out, `"ttl_remaining_sec": null`) {
		t.Fatalf("meta should carry an explicit null ttl:\n%s", out)
	}
}

// The headline behaviour: one batch, two languages, each row scored with the
// formula calibrated for it.
func TestReadabilityJSONLBatchResolvesLanguagePerDocument(t *testing.T) {
	lines := strings.Join([]string{
		`{"id":"de-1","text":` + rdMustJSON(rdGermanSample) + `,"slug":"hund"}`,
		`{"id":"en-1","text":` + rdMustJSON(rdEnglishSample) + `,"slug":"dog"}`,
	}, "\n") + "\n"
	f := rdWriteTemp(t, "batch.jsonl", lines)

	out, err := rdRun(t, "readability", "--file", f, "--input-format", "jsonl", "--output", "json")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	env := rdDecodeEnvelope(t, out)

	var data struct {
		Documents []readabilityDoc `json:"documents"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v\n%s", err, env.Data)
	}
	if len(data.Documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(data.Documents))
	}
	want := []struct{ id, lang, metric string }{
		{"de-1", "de", "amstad"},
		{"en-1", "en", "flesch"},
	}
	for i, w := range want {
		got := data.Documents[i]
		if got.ID != w.id {
			t.Fatalf("doc %d id = %q, want %q", i, got.ID, w.id)
		}
		if got.Language != w.lang {
			t.Fatalf("doc %d language = %q, want %q (detection picked the wrong formula)", i, got.Language, w.lang)
		}
		if !got.LanguageDetected {
			t.Fatalf("doc %d should be marked as detected", i)
		}
		if len(got.Metrics) != 1 || got.Metrics[0].Metric != w.metric {
			t.Fatalf("doc %d metrics = %+v, want %s", i, got.Metrics, w.metric)
		}
		if got.Metrics[0].Language != w.lang {
			t.Fatalf("doc %d metric language = %q, want %q", i, got.Metrics[0].Language, w.lang)
		}
	}
	if env.Meta.Documents != 2 {
		t.Fatalf("meta.documents = %d, want 2", env.Meta.Documents)
	}
	if env.Meta.Language != "" {
		t.Fatalf("meta.language = %q, want empty for a mixed batch", env.Meta.Language)
	}
}

// NDJSON is the streaming form: flat rows, one per document, carrying the
// source's sidecar fields so the output can be joined back.
func TestReadabilityNDJSONPassesFieldsThrough(t *testing.T) {
	lines := `{"id":"a","text":` + rdMustJSON(rdEnglishSample) + `,"slug":"dog","words":"do-not-clobber"}` + "\n"
	f := rdWriteTemp(t, "one.jsonl", lines)

	out, err := rdRun(t, "readability", "--file", f, "--input-format", "jsonl", "--lang", "en", "--output", "ndjson")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	lineCount := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		lineCount++
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode ndjson line: %v\n%s", err, line)
		}
		if rec["id"] != "a" || rec["slug"] != "dog" {
			t.Fatalf("record lost its identity: %v", rec)
		}
		if _, ok := rec["flesch"]; !ok {
			t.Fatalf("record has no flesch score: %v", rec)
		}
		if _, ok := rec["flesch_level"].(string); !ok {
			t.Fatalf("record has no flesch_level: %v", rec)
		}
		// The computed word count must win over the incoming field.
		if _, ok := rec["words"].(float64); !ok {
			t.Fatalf("passthrough field overwrote the computed word count: %v", rec["words"])
		}
	}
	if lineCount != 1 {
		t.Fatalf("ndjson lines = %d, want 1", lineCount)
	}
}

func TestReadabilityCSVColumns(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--output", "csv")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	header := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	want := "id,language,words,sentences,syllables,avg_sentence_length,avg_syllables_per_word,flesch,flesch_level"
	if header != want {
		t.Fatalf("csv header = %q, want %q", header, want)
	}
}

func TestReadabilityTextOutput(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--output", "text")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "language: en") {
		t.Fatalf("text output should start with the language line:\n%s", out)
	}
	if !strings.Contains(out, "words ") || !strings.Contains(out, "flesch ") {
		t.Fatalf("text output is missing the stats or the metric line:\n%s", out)
	}
}

// --stats=false drops the statistics from both the JSON and the tabular forms.
func TestReadabilityWithoutStats(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--stats=false", "--output", "json")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	if strings.Contains(out, `"stats"`) {
		t.Fatalf("stats should be omitted:\n%s", out)
	}
}

func TestReadabilityUnknownMetric(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	_, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "gunning-fog")
	rdRequireCode(t, err, errs.CodeUnknownMetric, 5)
	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Hint, "flesch") {
		t.Fatalf("hint should list the known metrics, got %q", e.Hint)
	}
}

func TestReadabilityMetricDoesNotSupportLanguage(t *testing.T) {
	f := rdWriteTemp(t, "doc.de.txt", rdGermanSample)
	_, err := rdRun(t, "readability", "--file", f, "--lang", "de", "--metrics", "flesch")
	rdRequireCode(t, err, errs.CodeUnsupportedLanguage, 5)
	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Hint, "amstad") {
		t.Fatalf("hint should name the metric that does support de, got %q", e.Hint)
	}
}

func TestReadabilityUnsupportedLanguage(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	_, err := rdRun(t, "readability", "--file", f, "--lang", "fr")
	rdRequireCode(t, err, errs.CodeUnsupportedLanguage, 5)
}

// `text flesch` and `text amstad` are aliases that also pin the metric.
func TestReadabilityAliasSelectsItsMetric(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "flesch", "--file", f, "--lang", "en", "--output", "json")
	if err != nil {
		t.Fatalf("flesch: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"metric": "flesch"`) {
		t.Fatalf("alias did not select flesch:\n%s", out)
	}
	if _, err := rdRun(t, "amstad", "--file", f, "--lang", "en", "--output", "json"); err == nil {
		t.Fatal("`text amstad` on English should fail as unsupported_language")
	}
}

func TestMetricsListAndShow(t *testing.T) {
	out, err := rdRun(t, "metrics", "--output", "json")
	if err != nil {
		t.Fatalf("metrics: %v\n%s", err, out)
	}
	var env struct {
		Data struct {
			Metrics []metricInfo `json:"metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(env.Data.Metrics) < 2 {
		t.Fatalf("metrics list = %+v, want at least flesch and amstad", env.Data.Metrics)
	}
	for _, mi := range env.Data.Metrics {
		if mi.Name == "" || mi.Title == "" || mi.Description == "" || len(mi.Languages) == 0 {
			t.Fatalf("incomplete discovery record: %+v", mi)
		}
	}

	// show resolves aliases.
	out, err = rdRun(t, "metrics", "show", "fre-de", "--output", "json")
	if err != nil {
		t.Fatalf("metrics show: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"name": "amstad"`) {
		t.Fatalf("alias fre-de did not resolve to amstad:\n%s", out)
	}

	_, err = rdRun(t, "metrics", "show", "nope")
	rdRequireCode(t, err, errs.CodeUnknownMetric, 5)
}

func rdMustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
