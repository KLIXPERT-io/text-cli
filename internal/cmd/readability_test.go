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

	"github.com/KLIXPERT-io/text-cli/internal/analyze"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
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
	// Pinned to one metric: the shape of a result is what this test is about,
	// and it must not change every time a formula joins the registry.
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch", "--output", "json")
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
	want := []struct{ id, lang, flagship string }{
		{"de-1", "de", "amstad"},
		{"en-1", "en", "flesch"},
	}
	// The assertion is about the *rule*, not about today's metric list: every
	// metric a row was scored with must declare that row's language, and the
	// language-specific metrics of the two rows must not overlap at all. A new
	// formula joining the registry cannot break this without breaking the
	// per-document resolution it is testing.
	specific := make([]map[string]bool, len(want))
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
		if len(got.Metrics) == 0 {
			t.Fatalf("doc %d was scored with nothing", i)
		}
		specific[i] = map[string]bool{}
		names := map[string]bool{}
		for _, r := range got.Metrics {
			names[r.Metric] = true
			m, ok := analyze.Get(r.Metric)
			if !ok {
				t.Fatalf("doc %d was scored with unregistered metric %q", i, r.Metric)
			}
			if !m.Supports(textproc.Language(w.lang)) {
				t.Fatalf("doc %d (%s) was scored with %q, which supports %v",
					i, w.lang, r.Metric, m.Languages)
			}
			if !m.Supports(textproc.Language(analyze.AnyLanguage)) {
				specific[i][r.Metric] = true
			}
		}
		if !names[w.flagship] {
			t.Fatalf("doc %d metrics %v are missing the %s formula for %s", i, names, w.flagship, w.lang)
		}
	}
	for name := range specific[0] {
		if specific[1][name] {
			t.Fatalf("metric %q was applied to both the German and the English row; "+
				"a language-specific formula must not cross rows", name)
		}
	}
	if len(specific[0]) == 0 || len(specific[1]) == 0 {
		t.Fatalf("each row should carry at least one language-specific metric, got %v and %v", specific[0], specific[1])
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
	// Metric columns follow the order given to --metrics, so an explicit
	// selection pins the header exactly.
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch,lix", "--output", "csv")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	header := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	want := "id,language,words,sentences,syllables,avg_sentence_length,avg_syllables_per_word,flesch,flesch_level,lix,lix_level"
	if header != want {
		t.Fatalf("csv header = %q, want %q", header, want)
	}

	// Under auto every metric valid for the language gets a pair of columns.
	out, err = rdRun(t, "readability", "--file", f, "--lang", "en", "--output", "csv")
	if err != nil {
		t.Fatalf("readability: %v\n%s", err, out)
	}
	header = strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	for _, m := range analyze.ForLanguage("en") {
		if !strings.Contains(header, ","+m.Name+","+m.Name+"_level") &&
			!strings.HasSuffix(header, ","+m.Name+","+m.Name+"_level") {
			t.Fatalf("csv header %q is missing the %s columns", header, m.Name)
		}
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
	_, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "dale-chall")
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

// ---------------------------------------------------------------------------
// --fail-under / --fail-over
// ---------------------------------------------------------------------------

// rdHardSample scores far below any sensible reading-ease threshold and far
// above any sensible grade-level one: one long sentence of long words.
const rdHardSample = "The multidimensional interoperability considerations subsequently necessitate " +
	"comprehensive organizational restructuring initiatives throughout the international " +
	"telecommunications infrastructure administration."

// A passing gate returns no error at all, and the output is unaffected by the
// flag being present.
func TestReadabilityFailUnderPasses(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch", "--fail-under", "10")
	if err != nil {
		t.Fatalf("a document above the threshold must pass: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"metric": "flesch"`) {
		t.Fatalf("output should be unchanged by a passing gate:\n%s", out)
	}
}

// A failing gate still prints every number — a CI job needs the scores that
// failed it — and reports the failure through the error, which carries the
// distinct generic exit code 1 rather than an argument-error 5.
func TestReadabilityFailUnderFails(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdHardSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch", "--fail-under", "60")
	rdRequireCode(t, err, errs.CodeGeneric, 1)

	if !strings.Contains(out, `"metric": "flesch"`) {
		t.Fatalf("the scores must still reach stdout on a failed gate:\n%s", out)
	}
	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Message, "flesch") || !strings.Contains(e.Message, "fail-under 60") {
		t.Fatalf("message should name the metric and the threshold, got %q", e.Message)
	}
}

// The mirror flag for grade levels: --fail-over trips when the score is too
// HIGH, because a higher grade level is a harder text.
func TestReadabilityFailOverOnGradeLevel(t *testing.T) {
	f := rdWriteTemp(t, "doc.de.txt", rdGermanSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "de", "--metrics", "wstf", "--fail-over", "20")
	if err != nil {
		t.Fatalf("a simple German sentence is well under grade 20: %v\n%s", err, out)
	}

	f = rdWriteTemp(t, "hard.txt", rdHardSample)
	_, err = rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "ari", "--fail-over", "8")
	rdRequireCode(t, err, errs.CodeGeneric, 1)
	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Message, "ari") || !strings.Contains(e.Message, "fail-over 8") {
		t.Fatalf("message should name the metric and the threshold, got %q", e.Message)
	}
}

// The trap the two flags exist to prevent: --fail-under on a grade-level metric
// would be satisfied by every text and pass CI for the wrong reason. It is an
// argument error naming the flag that would have worked, in both directions.
func TestReadabilityGateWrongDirection(t *testing.T) {
	de := rdWriteTemp(t, "doc.de.txt", rdGermanSample)
	en := rdWriteTemp(t, "doc.txt", rdEnglishSample)

	tests := []struct {
		name       string
		args       []string
		wantFlag   string // the flag the hint must recommend
		wantMetric string
	}{
		{"fail-under on wstf", []string{"--file", de, "--lang", "de", "--metrics", "wstf", "--fail-under", "8"}, "fail-over", "wstf1"},
		{"fail-under on lix", []string{"--file", en, "--lang", "en", "--metrics", "lix", "--fail-under", "40"}, "fail-over", "lix"},
		{"fail-under on smog", []string{"--file", en, "--lang", "en", "--metrics", "smog", "--fail-under", "9"}, "fail-over", "smog"},
		{"fail-over on flesch", []string{"--file", en, "--lang", "en", "--metrics", "flesch", "--fail-over", "60"}, "fail-under", "flesch"},
		{"fail-over on amstad", []string{"--file", de, "--lang", "de", "--metrics", "amstad", "--fail-over", "60"}, "fail-under", "amstad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rdRun(t, append([]string{"readability"}, tt.args...)...)
			rdRequireCode(t, err, errs.CodeInvalidArgs, 5)
			var e *errs.E
			errors.As(err, &e)
			if !strings.Contains(e.Message, tt.wantMetric) {
				t.Fatalf("message should name the metric %q, got %q", tt.wantMetric, e.Message)
			}
			if !strings.Contains(e.Hint, "--"+tt.wantFlag) {
				t.Fatalf("hint should point at --%s, got %q", tt.wantFlag, e.Hint)
			}
		})
	}
}

// Under --metrics auto the selection is the registry's, not the user's, so a
// gate applies to the metrics it fits and leaves the others alone instead of
// rejecting the run.
func TestReadabilityGateUnderAutoIgnoresTheOtherDirection(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	// English auto covers both directions: flesch (higher is easier) plus the
	// grade-level pack and lix (lower is easier).
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--fail-under", "10")
	if err != nil {
		t.Fatalf("auto selection must not turn a one-directional gate into an error: %v\n%s", err, out)
	}
	// And the gate is still live: a threshold nothing can meet fails.
	_, err = rdRun(t, "readability", "--file", f, "--lang", "en", "--fail-under", "200")
	rdRequireCode(t, err, errs.CodeGeneric, 1)
}

// Both flags at once gate both families in a single run.
func TestReadabilityBothGatesTogether(t *testing.T) {
	f := rdWriteTemp(t, "doc.txt", rdEnglishSample)
	out, err := rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch,ari",
		"--fail-under", "10", "--fail-over", "20")
	if err != nil {
		t.Fatalf("both thresholds are met: %v\n%s", err, out)
	}

	_, err = rdRun(t, "readability", "--file", f, "--lang", "en", "--metrics", "flesch,ari",
		"--fail-under", "200", "--fail-over", "0")
	rdRequireCode(t, err, errs.CodeGeneric, 1)
	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Message, "flesch") || !strings.Contains(e.Message, "ari") {
		t.Fatalf("both failures should be reported, got %q", e.Message)
	}
}

// In a batch, one bad document fails the run, and the message says how many of
// how many failed — that count is what a CI log is read for.
func TestReadabilityGateBatchReportsFailureCount(t *testing.T) {
	lines := strings.Join([]string{
		`{"id":"ok-1","text":` + rdMustJSON(rdEnglishSample) + `}`,
		`{"id":"bad","text":` + rdMustJSON(rdHardSample) + `}`,
		`{"id":"ok-2","text":"The cat sat on the mat. The dog ran fast."}`,
	}, "\n") + "\n"
	f := rdWriteTemp(t, "batch.jsonl", lines)

	out, err := rdRun(t, "readability", "--file", f, "--input-format", "jsonl", "--lang", "en",
		"--metrics", "flesch", "--fail-under", "50", "--output", "json")
	rdRequireCode(t, err, errs.CodeGeneric, 1)

	var e *errs.E
	errors.As(err, &e)
	if !strings.Contains(e.Message, "1 of 3") {
		t.Fatalf("message should count the failing documents, got %q", e.Message)
	}
	// Every document's numbers are still on stdout, including the passing ones.
	for _, id := range []string{"ok-1", "bad", "ok-2"} {
		if !strings.Contains(out, `"id": "`+id+`"`) {
			t.Fatalf("document %q is missing from the output:\n%s", id, out)
		}
	}

	// A threshold every document meets passes the whole batch.
	if _, err := rdRun(t, "readability", "--file", f, "--input-format", "jsonl", "--lang", "en",
		"--metrics", "flesch", "--fail-under", "-500", "--output", "json"); err != nil {
		t.Fatalf("no document is below −500: %v", err)
	}
}

// Without the flags nothing is gated, however bad the text is.
func TestReadabilityNoGateWithoutFlags(t *testing.T) {
	f := rdWriteTemp(t, "hard.txt", rdHardSample)
	if _, err := rdRun(t, "readability", "--file", f, "--lang", "en"); err != nil {
		t.Fatalf("scoring is not gating: %v", err)
	}
}

func rdMustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
