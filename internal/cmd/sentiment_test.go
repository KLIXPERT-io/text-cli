package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/spf13/cobra"
)

// fakeSentimentProvider implements the base provider plus the sentiment
// capability. It never touches the network and counts calls, so the cache tests
// can prove a second run cost nothing.
type fakeSentimentProvider struct {
	mu       sync.Mutex
	calls    int
	lastText string
	lastOpts entity.Options
	err      error
	// res overrides the canned answer when set.
	res *entity.SentimentResult
}

func (f *fakeSentimentProvider) Name() string { return "fakesent" }

func (f *fakeSentimentProvider) AnalyzeEntities(context.Context, string, entity.Options) (*entity.Result, error) {
	return &entity.Result{Provider: "fakesent", LanguageSupported: true, Entities: []entity.Entity{}}, nil
}

func (f *fakeSentimentProvider) AnalyzeSentiment(_ context.Context, text string, opts entity.Options) (*entity.SentimentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastText, f.lastOpts = text, opts
	if f.err != nil {
		return nil, f.err
	}
	if f.res != nil {
		clone := *f.res
		return &clone, nil
	}
	return &entity.SentimentResult{
		Provider:  "fakesent",
		Language:  "en",
		Score:     0.8,
		Magnitude: 1.6,
		Label:     entity.LabelPositive,
		Sentences: []entity.SentenceSentiment{
			{Text: "The room was lovely.", Score: 0.9, Magnitude: 0.9, BeginOffset: 0},
			{Text: "The food was awful.", Score: -0.6, Magnitude: 0.7, BeginOffset: 21},
		},
	}, nil
}

func (f *fakeSentimentProvider) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.err, f.res = 0, nil, nil
}

func (f *fakeSentimentProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeBaseProvider does entities and nothing else — the shape a knowledge-base
// backend has. It exists to prove the capability assertion refuses it.
type fakeBaseProvider struct{}

func (f *fakeBaseProvider) Name() string { return "fakebase" }

func (f *fakeBaseProvider) AnalyzeEntities(context.Context, string, entity.Options) (*entity.Result, error) {
	return &entity.Result{Provider: "fakebase", LanguageSupported: true, Entities: []entity.Entity{}}, nil
}

var (
	fakeSent = &fakeSentimentProvider{}
	fakeBase = &fakeBaseProvider{}
)

func init() {
	entity.Register("fakesent", func() (entity.Provider, error) { return fakeSent, nil })
	entity.Register("fakebase", func() (entity.Provider, error) { return fakeBase, nil })
}

// newNLTestRoot mirrors the persistent flags the real root owns, so the
// sentiment and classify commands see the same flag set they do in production
// while the test controls the cache directory and config.
func newNLTestRoot(t *testing.T, st *State) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	if st.Cfg == nil {
		st.Cfg = config.Default()
	}
	if st.Cache == nil {
		st.Cache = cache.New(t.TempDir(), time.Hour)
	}
	if st.OutputFormat == "" {
		st.OutputFormat = "json"
	}

	root := &cobra.Command{Use: "text", SilenceUsage: true, SilenceErrors: true}
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", st.OutputFormat, "")
	pf.StringVar(&st.Lang, "lang", "", "")
	pf.StringVarP(&st.File, "file", "f", "", "")
	pf.StringVar(&st.InputFormat, "input-format", "text", "")
	pf.StringVar(&st.TextField, "text-field", "text", "")
	pf.StringVar(&st.IDField, "id-field", "id", "")
	pf.BoolVar(&st.NoCache, "no-cache", false, "")
	pf.BoolVar(&st.Refresh, "refresh", false, "")
	pf.DurationVar(&st.CacheTTL, "cache-ttl", 0, "")

	root.AddCommand(newSentimentCmd(), newClassifyCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	return root, buf
}

// runNL executes one of the two commands and returns the decoded JSON envelope
// (when the format is json) plus the raw output.
func runNL(t *testing.T, st *State, name string, args ...string) (map[string]any, string) {
	t.Helper()
	root, buf := newNLTestRoot(t, st)
	root.SetArgs(append([]string{name}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %s %v: %v", name, args, err)
	}
	out := buf.String()
	var env map[string]any
	if st.OutputFormat == "json" {
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode output %q: %v", out, err)
		}
	}
	return env, out
}

func TestSentimentSingleDocumentJSONShape(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "review.txt", "The room was lovely. The food was awful.")

	env, _ := runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent")

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want an object", env["data"])
	}
	if data["id"] != "0" || data["provider"] != "fakesent" || data["language"] != "en" {
		t.Fatalf("data = %v", data)
	}
	if data["score"] != 0.8 || data["magnitude"] != 1.6 {
		t.Fatalf("score/magnitude = %v/%v", data["score"], data["magnitude"])
	}
	if data["label"] != entity.LabelPositive {
		t.Fatalf("label = %v", data["label"])
	}
	sents, ok := data["sentences"].([]any)
	if !ok || len(sents) != 2 {
		t.Fatalf("sentences = %v, want 2 by default", data["sentences"])
	}
	first := sents[0].(map[string]any)
	if first["text"] != "The room was lovely." || first["score"] != 0.9 || first["begin_offset"] != float64(0) {
		t.Fatalf("first sentence = %v", first)
	}
	if sents[1].(map[string]any)["begin_offset"] != float64(21) {
		t.Fatalf("second sentence offset = %v", sents[1])
	}

	meta := env["meta"].(map[string]any)
	if meta["provider"] != "fakesent" || meta["documents"] != float64(1) {
		t.Fatalf("meta = %v", meta)
	}
	if meta["cached"] != false || meta["api_calls"] != float64(1) {
		t.Fatalf("meta cache fields = %v", meta)
	}
	if meta["language"] != "en" || meta["language_detected"] != true {
		t.Fatalf("meta language = %v / %v", meta["language"], meta["language_detected"])
	}
}

func TestSentimentSentencesFlagOff(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "review.txt", "The room was lovely. The food was awful.")

	env, _ := runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent", "--sentences=false")
	data := env["data"].(map[string]any)
	if _, has := data["sentences"]; has {
		t.Fatalf("--sentences=false still emitted %v", data["sentences"])
	}
	if data["score"] != 0.8 {
		t.Fatalf("the document score must survive: %v", data)
	}
}

// TestSentimentLabelIsDerivedWhenProviderOmitsIt pins where the label rule
// lives: one place, applied to every backend.
func TestSentimentLabelIsDerivedWhenProviderOmitsIt(t *testing.T) {
	cases := []struct {
		name      string
		score     float64
		magnitude float64
		want      string
	}{
		{"mixed", 0.0, 3.4, entity.LabelMixed},
		{"neutral", 0.05, 0.1, entity.LabelNeutral},
		{"negative", -0.7, 2.0, entity.LabelNegative},
		{"positive", 0.7, 2.0, entity.LabelPositive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeSent.reset()
			fakeSent.res = &entity.SentimentResult{Provider: "fakesent", Language: "en", Score: tc.score, Magnitude: tc.magnitude}
			defer fakeSent.reset()

			file := writeTemp(t, "doc.txt", "text "+tc.name)
			env, _ := runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent")
			if got := env["data"].(map[string]any)["label"]; got != tc.want {
				t.Fatalf("label = %v, want %q for score %v magnitude %v", got, tc.want, tc.score, tc.magnitude)
			}
		})
	}
}

func TestSentimentBatchShape(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"lovely","source":"blog"}`+"\n"+
			`{"id":"b","text":"awful"}`+"\n")

	env, _ := runNL(t, &State{}, "sentiment", "--file", file, "--input-format", "jsonl", "--provider", "fakesent")
	data := env["data"].(map[string]any)
	docs, ok := data["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Fatalf("data.documents = %v", data["documents"])
	}
	if _, has := data["score"]; has {
		t.Fatal("a batch must not also carry a top-level score")
	}
	d0 := docs[0].(map[string]any)
	if d0["id"] != "a" || d0["provider"] != "fakesent" || d0["label"] != entity.LabelPositive {
		t.Fatalf("document = %v", d0)
	}
	if env["meta"].(map[string]any)["documents"] != float64(2) {
		t.Fatalf("meta.documents = %v", env["meta"].(map[string]any)["documents"])
	}
}

// TestSentimentCSVShapes documents the rule: CSV and table are one row per
// document by default, and switch to one row per sentence only when the user
// passes --sentences explicitly (it defaults to true, so keying off the value
// alone would mean the document-level score never appeared in CSV at all).
func TestSentimentCSVShapes(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "review.txt", "The room was lovely. The food was awful.")

	_, out := runNL(t, &State{OutputFormat: "csv"}, "sentiment", "--file", file, "--provider", "fakesent")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "doc_id,score,magnitude,label,language" {
		t.Fatalf("default header = %q", lines[0])
	}
	if len(lines) != 2 {
		t.Fatalf("default rows = %d, want a header plus one row per document:\n%s", len(lines), out)
	}
	if lines[1] != "0,0.8,1.6,positive,en" {
		t.Fatalf("row = %q", lines[1])
	}

	_, out = runNL(t, &State{OutputFormat: "csv"}, "sentiment", "--file", file, "--provider", "fakesent", "--sentences")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "doc_id,sentence,score,magnitude,text" {
		t.Fatalf("--sentences header = %q", lines[0])
	}
	if len(lines) != 3 {
		t.Fatalf("--sentences rows = %d, want one per sentence:\n%s", len(lines), out)
	}
	if lines[1] != "0,0,0.9,0.9,The room was lovely." {
		t.Fatalf("sentence row = %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "0,1,-0.6,") {
		t.Fatalf("second sentence row = %q", lines[2])
	}
}

func TestSentimentNDJSONIsOneDocumentPerLine(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"lovely","source":"blog","label":"input-field"}`+"\n"+
			`{"id":"b","text":"awful"}`+"\n")

	_, out := runNL(t, &State{OutputFormat: "ndjson"}, "sentiment", "--file", file, "--input-format", "jsonl", "--provider", "fakesent")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d, want one per document:\n%s", len(lines), out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	if rec["doc_id"] != "a" || rec["score"] != 0.8 {
		t.Fatalf("record = %v", rec)
	}
	if rec["source"] != "blog" {
		t.Fatalf("passthrough field lost: %v", rec)
	}
	if rec["label"] != entity.LabelPositive {
		t.Fatalf("label = %v, want the computed label to win over the input field", rec["label"])
	}
}

func TestSentimentTextOutput(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "review.txt", "The room was lovely. The food was awful.")

	_, out := runNL(t, &State{OutputFormat: "text"}, "sentiment", "--file", file, "--provider", "fakesent")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("text lines = %d, want one per document:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "positive") || !strings.Contains(lines[0], "0.8000") || !strings.Contains(lines[0], "1.6000") {
		t.Fatalf("line = %q", lines[0])
	}

	_, out = runNL(t, &State{OutputFormat: "text"}, "sentiment", "--file", file, "--provider", "fakesent", "--sentences")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("--sentences text lines = %d, want the document plus its sentences:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "The room was lovely.") || !strings.Contains(lines[2], "negative") {
		t.Fatalf("sentence lines =\n%s", out)
	}
}

// TestSentimentCacheHit proves the money-saving property: an identical second
// run must not call the provider again.
func TestSentimentCacheHit(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "review.txt", "The room was lovely. The food was awful.")
	dir := t.TempDir()

	env, _ := runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", file, "--provider", "fakesent")
	if env["meta"].(map[string]any)["cached"] != false {
		t.Fatal("first run reported cached")
	}
	if fakeSent.callCount() != 1 {
		t.Fatalf("provider calls = %d after the first run", fakeSent.callCount())
	}

	env, _ = runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", file, "--provider", "fakesent")
	meta := env["meta"].(map[string]any)
	if meta["cached"] != true || meta["api_calls"] != float64(0) {
		t.Fatalf("second identical run was not served from cache: %v", meta)
	}
	if meta["cached_at"] == nil || meta["cached_at"] == "" {
		t.Fatalf("meta.cached_at = %v", meta["cached_at"])
	}
	if ttl, ok := meta["ttl_remaining_sec"].(float64); !ok || ttl <= 0 {
		t.Fatalf("meta.ttl_remaining_sec = %v", meta["ttl_remaining_sec"])
	}
	if fakeSent.callCount() != 1 {
		t.Fatalf("provider was called %d times; the cache did not hold", fakeSent.callCount())
	}

	// Toggling --sentences re-renders the cached payload instead of paying.
	env, _ = runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", file, "--provider", "fakesent", "--sentences=false")
	if _, has := env["data"].(map[string]any)["sentences"]; has {
		t.Fatal("--sentences=false leaked sentences from the cached payload")
	}
	if fakeSent.callCount() != 1 {
		t.Fatalf("re-rendering forced a new API call: %d", fakeSent.callCount())
	}

	// --refresh always pays, and repopulates.
	runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", file, "--provider", "fakesent", "--refresh")
	if fakeSent.callCount() != 2 {
		t.Fatalf("--refresh calls = %d, want 2", fakeSent.callCount())
	}
	// --no-cache neither reads nor writes.
	runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", file, "--provider", "fakesent", "--no-cache")
	if fakeSent.callCount() != 3 {
		t.Fatalf("--no-cache calls = %d, want 3", fakeSent.callCount())
	}
}

// TestSentimentPartialBatchIsNotCached pins the established rule: meta.cached is
// only true when the whole batch came from cache.
func TestSentimentPartialBatchIsNotCached(t *testing.T) {
	fakeSent.reset()
	dir := t.TempDir()
	one := writeTemp(t, "one.txt", "lovely")
	runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", one, "--provider", "fakesent")

	both := writeTemp(t, "docs.jsonl", `{"id":"a","text":"lovely"}`+"\n"+`{"id":"b","text":"awful"}`+"\n")
	env, _ := runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "sentiment", "--file", both, "--input-format", "jsonl", "--provider", "fakesent")
	meta := env["meta"].(map[string]any)
	if meta["cached"] != false {
		t.Fatalf("a half-cached batch reported cached=true: %v", meta)
	}
	if meta["api_calls"] != float64(1) {
		t.Fatalf("meta.api_calls = %v, want 1 (only the uncached document)", meta["api_calls"])
	}
}

// TestSentimentRejectsProviderWithoutTheCapability is the capability assertion:
// a backend that only extracts entities is refused by name, with a hint listing
// the backends that would have worked.
func TestSentimentRejectsProviderWithoutTheCapability(t *testing.T) {
	file := writeTemp(t, "doc.txt", "lovely")
	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"sentiment", "--file", file, "--provider", "fakebase"})

	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeProviderUnavailable)
	}
	if !strings.Contains(e.Message, "fakebase") {
		t.Fatalf("message %q should name the provider", e.Message)
	}
	if !strings.Contains(e.Hint, "fakesent") {
		t.Fatalf("hint %q should list the providers that do support sentiment", e.Hint)
	}
}

func TestSentimentUnknownProviderIsStillInvalidArgs(t *testing.T) {
	file := writeTemp(t, "doc.txt", "lovely")
	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"sentiment", "--file", file, "--provider", "nope"})

	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("err = %v, want invalid_args for a name that does not exist at all", err)
	}
}

func TestSentimentProviderErrorPropagates(t *testing.T) {
	fakeSent.reset()
	fakeSent.err = errs.New(errs.CodeAuthDenied, "nope").WithHint("enable the API")
	defer fakeSent.reset()

	file := writeTemp(t, "doc.txt", "lovely")
	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"sentiment", "--file", file, "--provider", "fakesent"})
	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeAuthDenied {
		t.Fatalf("err = %v, want auth_denied surfaced unchanged", err)
	}
	if errs.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2", errs.ExitCode(err))
	}
}

func TestSentimentFlagsReachTheProvider(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "doc.txt", "lovely")

	runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent", "--lang", "en", "--timeout", "5s")
	if fakeSent.lastOpts.Language != "en" {
		t.Fatalf("language = %q", fakeSent.lastOpts.Language)
	}
	if fakeSent.lastOpts.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", fakeSent.lastOpts.Timeout)
	}
	if fakeSent.lastOpts.ServiceAccountPath != "" {
		t.Fatalf("service account = %q, want empty without a flag, env, or config", fakeSent.lastOpts.ServiceAccountPath)
	}

	key := writeTemp(t, "key.json", "{}")
	runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent", "--service-account", key)
	if fakeSent.lastOpts.ServiceAccountPath != key {
		t.Fatalf("service account = %q, want %q", fakeSent.lastOpts.ServiceAccountPath, key)
	}

	// No --lang means auto-detection: the provider is asked with an empty
	// language, exactly as `text entities` does it.
	runNL(t, &State{}, "sentiment", "--file", file, "--provider", "fakesent")
	if fakeSent.lastOpts.Language != "" {
		t.Fatalf("language = %q, want empty", fakeSent.lastOpts.Language)
	}
	if fakeSent.lastOpts.Timeout != entity.DefaultTimeout {
		t.Fatalf("default timeout = %v", fakeSent.lastOpts.Timeout)
	}
}

func TestSentimentAlias(t *testing.T) {
	fakeSent.reset()
	file := writeTemp(t, "doc.txt", "lovely")
	root, buf := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"sent", "--file", file, "--provider", "fakesent"})
	if err := root.Execute(); err != nil {
		t.Fatalf("sent alias: %v", err)
	}
	if !strings.Contains(buf.String(), `"label": "positive"`) {
		t.Fatalf("alias produced %q", buf.String())
	}
}
