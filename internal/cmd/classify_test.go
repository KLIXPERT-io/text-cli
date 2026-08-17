package cmd

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// longText is comfortably over MinClassifyWords, so the length guard is not
// what any of these tests happen to be measuring.
const longText = "Cloud Natural Language sorts a document into a taxonomy of content " +
	"categories, which only works once the document carries enough context to be " +
	"about something in the first place. A single short sentence never does."

// fakeClassifyProvider implements the base provider plus the classification
// capability, counting calls so the cache tests can prove a second run was free.
type fakeClassifyProvider struct {
	mu       sync.Mutex
	calls    int
	lastText string
	lastOpts entity.Options
	err      error
}

func (f *fakeClassifyProvider) Name() string { return "fakecls" }

func (f *fakeClassifyProvider) AnalyzeEntities(context.Context, string, entity.Options) (*entity.Result, error) {
	return &entity.Result{Provider: "fakecls", LanguageSupported: true, Entities: []entity.Entity{}}, nil
}

func (f *fakeClassifyProvider) ClassifyText(_ context.Context, text string, opts entity.Options) (*entity.ClassificationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastText, f.lastOpts = text, opts
	if f.err != nil {
		return nil, f.err
	}
	return &entity.ClassificationResult{
		Provider: "fakecls",
		Language: opts.Language,
		Categories: []entity.Category{
			{Name: "/Computers & Electronics/Software", Confidence: 0.62},
			{Name: "/Science/Computer Science", Confidence: 0.91},
			{Name: "/Books & Literature", Confidence: 0.2},
		},
	}, nil
}

func (f *fakeClassifyProvider) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.err = 0, nil
}

func (f *fakeClassifyProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var fakeCls = &fakeClassifyProvider{}

func init() {
	entity.Register("fakecls", func() (entity.Provider, error) { return fakeCls, nil })
}

func TestClassifySingleDocumentJSONShape(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)

	env, _ := runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--lang", "en")

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want an object", env["data"])
	}
	if data["id"] != "0" || data["provider"] != "fakecls" || data["language"] != "en" {
		t.Fatalf("data = %v", data)
	}
	cats, ok := data["categories"].([]any)
	if !ok || len(cats) != 3 {
		t.Fatalf("categories = %v", data["categories"])
	}
	// Sorted by confidence desc.
	first := cats[0].(map[string]any)
	if first["name"] != "/Science/Computer Science" || first["confidence"] != 0.91 {
		t.Fatalf("first category = %v", first)
	}

	meta := env["meta"].(map[string]any)
	if meta["provider"] != "fakecls" || meta["documents"] != float64(1) {
		t.Fatalf("meta = %v", meta)
	}
	if meta["cached"] != false || meta["api_calls"] != float64(1) {
		t.Fatalf("meta cache fields = %v", meta)
	}
	if meta["language"] != "en" {
		t.Fatalf("meta.language = %v", meta["language"])
	}
}

// TestClassifyTooShortIsRejectedBeforeTheCall is the guard that keeps a raw
// InvalidArgument from the API off the user's screen — and, just as important,
// keeps the request from being sent at all.
func TestClassifyTooShortIsRejectedBeforeTheCall(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "short.txt", "Ada Lovelace worked with Charles Babbage in London.")

	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"classify", "--file", file, "--provider", "fakecls"})
	err := root.Execute()

	var e *errs.E
	if err == nil || !asErr(err, &e) {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if e.Code != errs.CodeInvalidArgs {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeInvalidArgs)
	}
	if !strings.Contains(e.Message, "too short") {
		t.Fatalf("message = %q", e.Message)
	}
	if !strings.Contains(e.Hint, "20+") {
		t.Fatalf("hint %q should say roughly how many words are needed", e.Hint)
	}
	if fakeCls.callCount() != 0 {
		t.Fatalf("provider was called %d times for text that cannot be classified", fakeCls.callCount())
	}

	// One short document in a batch fails the run before anything is billed.
	fakeCls.reset()
	batch := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"`+longText+`"}`+"\n"+
			`{"id":"b","text":"too short"}`+"\n")
	root, _ = newNLTestRoot(t, &State{})
	root.SetArgs([]string{"classify", "--file", batch, "--input-format", "jsonl", "--provider", "fakecls"})
	if err := root.Execute(); err == nil {
		t.Fatal("a batch containing a too-short document must fail")
	}
	if fakeCls.callCount() != 0 {
		t.Fatalf("provider calls = %d, want 0: the guard runs before the loop", fakeCls.callCount())
	}
}

func TestClassifyFilters(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)

	env, _ := runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--top", "2")
	cats := env["data"].(map[string]any)["categories"].([]any)
	if len(cats) != 2 {
		t.Fatalf("--top 2 kept %d", len(cats))
	}
	if cats[0].(map[string]any)["name"] != "/Science/Computer Science" {
		t.Fatalf("--top kept the wrong categories: %v", cats)
	}

	env, _ = runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--min-confidence", "0.5")
	cats = env["data"].(map[string]any)["categories"].([]any)
	if len(cats) != 2 {
		t.Fatalf("--min-confidence 0.5 kept %d, want 2", len(cats))
	}
	for _, c := range cats {
		if c.(map[string]any)["confidence"].(float64) < 0.5 {
			t.Fatalf("threshold leaked %v", c)
		}
	}

	// The threshold runs before the truncation.
	env, _ = runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--min-confidence", "0.5", "--top", "1")
	cats = env["data"].(map[string]any)["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["confidence"] != 0.91 {
		t.Fatalf("combined filters = %v", cats)
	}

	// Filtering everything out is an empty array, never null.
	env, _ = runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--min-confidence", "0.99")
	cats, ok := env["data"].(map[string]any)["categories"].([]any)
	if !ok || len(cats) != 0 {
		t.Fatalf("categories = %v, want an empty array", env["data"].(map[string]any)["categories"])
	}
}

func TestClassifyRejectsOutOfRangeConfidence(t *testing.T) {
	file := writeTemp(t, "post.txt", longText)
	for _, v := range []string{"-0.1", "1.5"} {
		root, _ := newNLTestRoot(t, &State{})
		root.SetArgs([]string{"classify", "--file", file, "--provider", "fakecls", "--min-confidence", v})
		err := root.Execute()
		var e *errs.E
		if err == nil || !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
			t.Fatalf("--min-confidence %s: err = %v, want invalid_args", v, err)
		}
	}
}

func TestClassifyBatchShape(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"`+longText+`","source":"blog"}`+"\n"+
			`{"id":"b","text":"`+longText+` And a second document.","source":"docs"}`+"\n")

	env, _ := runNL(t, &State{}, "classify", "--file", file, "--input-format", "jsonl", "--provider", "fakecls")
	data := env["data"].(map[string]any)
	docs, ok := data["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Fatalf("data.documents = %v", data["documents"])
	}
	if _, has := data["categories"]; has {
		t.Fatal("a batch must not also carry a top-level categories key")
	}
	d0 := docs[0].(map[string]any)
	if d0["id"] != "a" || d0["provider"] != "fakecls" || len(d0["categories"].([]any)) != 3 {
		t.Fatalf("document = %v", d0)
	}
	if env["meta"].(map[string]any)["documents"] != float64(2) {
		t.Fatalf("meta.documents = %v", env["meta"].(map[string]any)["documents"])
	}
}

func TestClassifyCSVColumns(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)

	_, out := runNL(t, &State{OutputFormat: "csv"}, "classify", "--file", file, "--provider", "fakecls")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "doc_id,category,confidence" {
		t.Fatalf("header = %q", lines[0])
	}
	if len(lines) != 4 {
		t.Fatalf("rows = %d, want a header plus one row per category:\n%s", len(lines), out)
	}
	if lines[1] != "0,/Science/Computer Science,0.91" {
		t.Fatalf("row = %q", lines[1])
	}
}

func TestClassifyNDJSONIsOneCategoryPerLine(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"`+longText+`","source":"blog","confidence":"input-field"}`+"\n")

	_, out := runNL(t, &State{OutputFormat: "ndjson"}, "classify", "--file", file, "--input-format", "jsonl",
		"--provider", "fakecls", "--top", "2")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d, want one per category:\n%s", len(lines), out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	if rec["doc_id"] != "a" || rec["name"] != "/Science/Computer Science" {
		t.Fatalf("record = %v", rec)
	}
	if rec["source"] != "blog" {
		t.Fatalf("passthrough field lost: %v", rec)
	}
	if rec["confidence"] != 0.91 {
		t.Fatalf("confidence = %v, want the computed value to win over the input field", rec["confidence"])
	}
	if rec["provider"] != "fakecls" {
		t.Fatalf("provider = %v", rec["provider"])
	}
}

func TestClassifyTextOutput(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)

	_, out := runNL(t, &State{OutputFormat: "text"}, "classify", "--file", file, "--provider", "fakecls")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("text lines = %d, want one per category:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "/Science/Computer Science") || !strings.Contains(lines[0], "0.9100") {
		t.Fatalf("line = %q", lines[0])
	}
}

// TestClassifyCacheHit proves the money-saving property, and that the filters
// live outside the cache key.
func TestClassifyCacheHit(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)
	dir := t.TempDir()

	env, _ := runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "classify", "--file", file, "--provider", "fakecls")
	if env["meta"].(map[string]any)["cached"] != false {
		t.Fatal("first run reported cached")
	}
	if fakeCls.callCount() != 1 {
		t.Fatalf("provider calls = %d after the first run", fakeCls.callCount())
	}

	env, _ = runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "classify", "--file", file, "--provider", "fakecls")
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
	if fakeCls.callCount() != 1 {
		t.Fatalf("provider was called %d times; the cache did not hold", fakeCls.callCount())
	}

	// Filtering a cached answer costs nothing.
	env, _ = runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "classify", "--file", file, "--provider", "fakecls", "--top", "1")
	if got := len(env["data"].(map[string]any)["categories"].([]any)); got != 1 {
		t.Fatalf("--top on a cache hit returned %d categories", got)
	}
	if fakeCls.callCount() != 1 {
		t.Fatalf("filtering forced a new API call: %d", fakeCls.callCount())
	}

	runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "classify", "--file", file, "--provider", "fakecls", "--refresh")
	if fakeCls.callCount() != 2 {
		t.Fatalf("--refresh calls = %d, want 2", fakeCls.callCount())
	}
	runNL(t, &State{Cache: cache.New(dir, time.Hour)}, "classify", "--file", file, "--provider", "fakecls", "--no-cache")
	if fakeCls.callCount() != 3 {
		t.Fatalf("--no-cache calls = %d, want 3", fakeCls.callCount())
	}
}

// TestClassifyCacheKeyIsSeparateFromEntities documents that the two commands do
// not share cached payloads even for identical (provider, language, text).
func TestClassifyCacheKeyIsSeparateFromEntities(t *testing.T) {
	args := []string{"fakecls", "en", "hello"}
	classify := cache.Key("classify", args, "", "")
	if classify == cache.Key("entities", args, "", "") {
		t.Fatal("classify collides with entities")
	}
	if classify == cache.Key("sentiment", args, "", "") {
		t.Fatal("classify collides with sentiment")
	}
	if classify != cache.Key("classify", args, "", "") {
		t.Fatal("the key is not stable for identical inputs")
	}
}

func TestClassifyRejectsProviderWithoutTheCapability(t *testing.T) {
	file := writeTemp(t, "post.txt", longText)
	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"classify", "--file", file, "--provider", "fakebase"})

	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) {
		t.Fatalf("err = %v, want a structured error", err)
	}
	if e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeProviderUnavailable)
	}
	if !strings.Contains(e.Hint, "fakecls") {
		t.Fatalf("hint %q should list the providers that do classify", e.Hint)
	}
}

func TestClassifyProviderErrorPropagates(t *testing.T) {
	fakeCls.reset()
	fakeCls.err = errs.New(errs.CodeQuotaExceeded, "quota").WithRetry(60)
	defer fakeCls.reset()

	file := writeTemp(t, "post.txt", longText)
	root, _ := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"classify", "--file", file, "--provider", "fakecls"})
	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeQuotaExceeded {
		t.Fatalf("err = %v, want quota_exceeded surfaced unchanged", err)
	}
	if errs.ExitCode(err) != 3 {
		t.Fatalf("exit code = %d, want 3", errs.ExitCode(err))
	}
}

func TestClassifyFlagsReachTheProvider(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)

	runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls", "--lang", "de", "--timeout", "7s")
	if fakeCls.lastOpts.Language != "de" || fakeCls.lastOpts.Timeout != 7*time.Second {
		t.Fatalf("opts = %#v", fakeCls.lastOpts)
	}

	runNL(t, &State{}, "classify", "--file", file, "--provider", "fakecls")
	if fakeCls.lastOpts.Language != "" {
		t.Fatalf("language = %q, want empty for auto-detection", fakeCls.lastOpts.Language)
	}
	if fakeCls.lastOpts.Timeout != entity.DefaultTimeout {
		t.Fatalf("default timeout = %v", fakeCls.lastOpts.Timeout)
	}
}

func TestClassifyAlias(t *testing.T) {
	fakeCls.reset()
	file := writeTemp(t, "post.txt", longText)
	root, buf := newNLTestRoot(t, &State{})
	root.SetArgs([]string{"cls", "--file", file, "--provider", "fakecls"})
	if err := root.Execute(); err != nil {
		t.Fatalf("cls alias: %v", err)
	}
	if !strings.Contains(buf.String(), "/Science/Computer Science") {
		t.Fatalf("alias produced %q", buf.String())
	}
}
