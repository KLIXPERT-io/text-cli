package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// fakeEntityProvider stands in for a real backend: it never touches the
// network, and it counts calls so the cache tests can prove a second run cost
// nothing.
type fakeEntityProvider struct {
	mu       sync.Mutex
	calls    int
	lastText string
	lastOpts entity.Options
	err      error
	// byText overrides the canned answer for one specific input document, so a
	// batch test can hand each document its own entities without a network.
	byText map[string][]entity.Entity
}

func (f *fakeEntityProvider) Name() string { return "fake" }

func (f *fakeEntityProvider) AnalyzeEntities(_ context.Context, text string, opts entity.Options) (*entity.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastText, f.lastOpts = text, opts
	if f.err != nil {
		return nil, f.err
	}
	res := &entity.Result{Provider: "fake", Language: "de", LanguageSupported: true}
	if ents, ok := f.byText[text]; ok {
		res.Entities = ents
		return res, nil
	}
	res.Entities = []entity.Entity{
		{
			Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, Probability: 0.98, MentionCount: 2,
			Metadata:     map[string]string{"wikipedia_url": "https://de.wikipedia.org/wiki/Ada_Lovelace", "mid": "/m/0ff4d"},
			WikipediaURL: "https://de.wikipedia.org/wiki/Ada_Lovelace",
			MID:          "/m/0ff4d",
			Mentions: []entity.Mention{
				{Text: "Ada Lovelace", Type: "PROPER", BeginOffset: 0, Probability: 0.98},
				{Text: "Lovelace", Type: "PROPER", BeginOffset: 40, Probability: 0.9},
			},
		},
		{Name: "London", Type: "LOCATION", Salience: 0.3, Probability: 0.7, MentionCount: 1},
		{Name: "Rechenmaschine", Type: "OTHER", Salience: 0.1, Probability: 0.3, MentionCount: 1},
	}
	return res, nil
}

func (f *fakeEntityProvider) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.err, f.byText = 0, nil, nil
}

func (f *fakeEntityProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var fakeEnts = &fakeEntityProvider{}

func init() {
	entity.Register("fake", func() (entity.Provider, error) { return fakeEnts, nil })
}

// newTestRoot mirrors the persistent flags the real root owns, so the entities
// command sees the same flag set it does in production while the test controls
// the cache directory and config.
func newTestRoot(t *testing.T, st *State) (*cobra.Command, *bytes.Buffer) {
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

	root.AddCommand(newEntitiesCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	return root, buf
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// runEntities executes the command and returns the decoded JSON envelope.
func runEntities(t *testing.T, st *State, args ...string) (map[string]any, string) {
	t.Helper()
	root, buf := newTestRoot(t, st)
	root.SetArgs(append([]string{"entities"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
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

func TestEntitiesSingleDocumentJSONShape(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake")

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want an object", env["data"])
	}
	if data["id"] != "0" {
		t.Fatalf("data.id = %v, want \"0\"", data["id"])
	}
	if data["provider"] != "fake" {
		t.Fatalf("data.provider = %v", data["provider"])
	}
	if data["language"] != "de" {
		t.Fatalf("data.language = %v", data["language"])
	}
	if data["language_supported"] != true {
		t.Fatalf("data.language_supported = %v", data["language_supported"])
	}
	ents, ok := data["entities"].([]any)
	if !ok || len(ents) != 3 {
		t.Fatalf("data.entities = %v", data["entities"])
	}
	// Sorted by salience desc.
	first := ents[0].(map[string]any)
	if first["name"] != "Ada Lovelace" || first["type"] != "PERSON" {
		t.Fatalf("first entity = %v", first)
	}
	if first["salience"] != 0.6 {
		t.Fatalf("salience = %v", first["salience"])
	}
	if first["probability"] != 0.98 {
		t.Fatalf("probability = %v", first["probability"])
	}
	if first["mention_count"] != float64(2) {
		t.Fatalf("mention_count = %v", first["mention_count"])
	}
	if first["wikipedia_url"] != "https://de.wikipedia.org/wiki/Ada_Lovelace" {
		t.Fatalf("wikipedia_url = %v", first["wikipedia_url"])
	}
	if first["mid"] != "/m/0ff4d" {
		t.Fatalf("mid = %v", first["mid"])
	}

	meta, ok := env["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta is %T", env["meta"])
	}
	if meta["provider"] != "fake" {
		t.Fatalf("meta.provider = %v", meta["provider"])
	}
	if meta["documents"] != float64(1) {
		t.Fatalf("meta.documents = %v", meta["documents"])
	}
	if meta["cached"] != false {
		t.Fatalf("meta.cached = %v, want false on the first run", meta["cached"])
	}
	if meta["api_calls"] != float64(1) {
		t.Fatalf("meta.api_calls = %v", meta["api_calls"])
	}
	if meta["language"] != "de" {
		t.Fatalf("meta.language = %v, want the detected language", meta["language"])
	}
	if meta["language_detected"] != true {
		t.Fatalf("meta.language_detected = %v, want true when no --lang was given", meta["language_detected"])
	}
}

func TestEntitiesTypeFilter(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake", "--types", "person,LOCATION")
	ents := env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 2 {
		t.Fatalf("--types kept %d entities, want 2", len(ents))
	}
	for _, e := range ents {
		typ := e.(map[string]any)["type"]
		if typ != "PERSON" && typ != "LOCATION" {
			t.Fatalf("unexpected type %v", typ)
		}
	}
}

// TestEntitiesMinSalienceAndTop pins the threshold semantics: it is compared
// against the entity score, which is salience whenever the provider reports
// one. The fake's saliences are 0.6 / 0.3 / 0.1.
func TestEntitiesMinSalienceAndTop(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake", "--min-salience", "0.2")
	if got := len(env["data"].(map[string]any)["entities"].([]any)); got != 2 {
		t.Fatalf("--min-salience 0.2 kept %d, want 2", got)
	}

	env, _ = runEntities(t, &State{}, "--file", file, "--provider", "fake", "--top", "1")
	ents := env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 1 || ents[0].(map[string]any)["name"] != "Ada Lovelace" {
		t.Fatalf("--top 1 = %v", ents)
	}

	env, _ = runEntities(t, &State{}, "--file", file, "--provider", "fake", "--min-salience", "0.99")
	ents, ok := env["data"].(map[string]any)["entities"].([]any)
	if !ok || len(ents) != 0 {
		t.Fatalf("entities = %v, want an empty array rather than null", env["data"].(map[string]any)["entities"])
	}
}

// TestEntitiesMinProbabilityIsAnAliasForMinSalience documents the compatibility
// rule: the older flag sets the same threshold, and an explicit --min-salience
// wins when both are given.
func TestEntitiesMinProbabilityIsAnAliasForMinSalience(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake", "--min-probability", "0.2")
	if got := len(env["data"].(map[string]any)["entities"].([]any)); got != 2 {
		t.Fatalf("--min-probability 0.2 kept %d, want the same 2 as --min-salience", got)
	}

	env, _ = runEntities(t, &State{}, "--file", file, "--provider", "fake", "--min-probability", "0.2", "--min-salience", "0.5")
	if got := len(env["data"].(map[string]any)["entities"].([]any)); got != 1 {
		t.Fatalf("explicit --min-salience did not win: kept %d, want 1", got)
	}
}

func TestEntitiesRejectsOutOfRangeThresholds(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	for _, flag := range []string{"--min-probability", "--min-salience"} {
		root, _ := newTestRoot(t, &State{})
		root.SetArgs([]string{"entities", "--file", file, "--provider", "fake", flag, "2"})
		err := root.Execute()
		var e *errs.E
		if err == nil || !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
			t.Fatalf("%s 2: err = %v, want invalid_args", flag, err)
		}
	}
}

func TestEntitiesRejectsUnknownSort(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	root, _ := newTestRoot(t, &State{})
	root.SetArgs([]string{"entities", "--file", file, "--provider", "fake", "--sort", "probability"})
	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("err = %v, want invalid_args", err)
	}
}

func TestEntitiesSortFlag(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake", "--sort", "name")
	ents := env["data"].(map[string]any)["entities"].([]any)
	if ents[0].(map[string]any)["name"] != "Ada Lovelace" || ents[1].(map[string]any)["name"] != "London" {
		t.Fatalf("--sort name = %v", ents)
	}
}

func TestEntitiesUnknownProvider(t *testing.T) {
	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	root, _ := newTestRoot(t, &State{})
	root.SetArgs([]string{"entities", "--file", file, "--provider", "nope"})
	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("err = %v, want invalid_args", err)
	}
	if !strings.Contains(e.Hint, "fake") {
		t.Fatalf("hint %q should list the registered providers", e.Hint)
	}
}

// TestEntitiesCacheHit proves the money-saving property: an identical second
// run must not call the provider again.
func TestEntitiesCacheHit(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")
	dir := t.TempDir()

	st := &State{Cache: cache.New(dir, time.Hour)}
	env, _ := runEntities(t, st, "--file", file, "--provider", "fake")
	if env["meta"].(map[string]any)["cached"] != false {
		t.Fatal("first run reported cached")
	}
	if fakeEnts.callCount() != 1 {
		t.Fatalf("provider calls = %d after the first run", fakeEnts.callCount())
	}

	st2 := &State{Cache: cache.New(dir, time.Hour)}
	env, _ = runEntities(t, st2, "--file", file, "--provider", "fake")
	meta := env["meta"].(map[string]any)
	if meta["cached"] != true {
		t.Fatalf("second identical run was not served from cache: %v", meta)
	}
	if meta["api_calls"] != float64(0) {
		t.Fatalf("meta.api_calls = %v on a cache hit, want 0", meta["api_calls"])
	}
	if meta["cached_at"] == nil || meta["cached_at"] == "" {
		t.Fatalf("meta.cached_at = %v", meta["cached_at"])
	}
	if ttl, ok := meta["ttl_remaining_sec"].(float64); !ok || ttl <= 0 {
		t.Fatalf("meta.ttl_remaining_sec = %v", meta["ttl_remaining_sec"])
	}
	if fakeEnts.callCount() != 1 {
		t.Fatalf("provider was called %d times; the cache did not hold", fakeEnts.callCount())
	}
	// The cached payload still filters, without paying again.
	st3 := &State{Cache: cache.New(dir, time.Hour)}
	env, _ = runEntities(t, st3, "--file", file, "--provider", "fake", "--top", "1")
	if got := len(env["data"].(map[string]any)["entities"].([]any)); got != 1 {
		t.Fatalf("--top on a cache hit returned %d entities", got)
	}
	if fakeEnts.callCount() != 1 {
		t.Fatalf("filtering forced a new API call: %d", fakeEnts.callCount())
	}
}

func TestEntitiesNoCacheAndRefresh(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")
	dir := t.TempDir()

	runEntities(t, &State{Cache: cache.New(dir, time.Hour)}, "--file", file, "--provider", "fake", "--no-cache")
	// --no-cache must not write, so the next plain run is still a miss.
	env, _ := runEntities(t, &State{Cache: cache.New(dir, time.Hour)}, "--file", file, "--provider", "fake")
	if env["meta"].(map[string]any)["cached"] != false {
		t.Fatal("--no-cache wrote an entry")
	}
	if fakeEnts.callCount() != 2 {
		t.Fatalf("provider calls = %d, want 2", fakeEnts.callCount())
	}

	// --refresh skips the read but writes, so it always costs a call and the
	// run after it is a hit.
	env, _ = runEntities(t, &State{Cache: cache.New(dir, time.Hour)}, "--file", file, "--provider", "fake", "--refresh")
	if env["meta"].(map[string]any)["cached"] != false {
		t.Fatal("--refresh served from cache")
	}
	if fakeEnts.callCount() != 3 {
		t.Fatalf("provider calls = %d, want 3", fakeEnts.callCount())
	}
	env, _ = runEntities(t, &State{Cache: cache.New(dir, time.Hour)}, "--file", file, "--provider", "fake")
	if env["meta"].(map[string]any)["cached"] != true {
		t.Fatal("--refresh did not repopulate the cache")
	}
}

// TestEntitiesCacheKeyIsProviderLanguageText documents the key contract: the
// filters are deliberately absent from it.
func TestEntitiesCacheKeyIsProviderLanguageText(t *testing.T) {
	base := cache.Key("entities", []string{"fake", "de", "hello"}, "", "")
	if got := cache.Key("entities", []string{"fake", "de", "hello"}, "", ""); got != base {
		t.Fatal("the key is not stable for identical inputs")
	}
	for _, other := range [][]string{
		{"google", "de", "hello"},
		{"fake", "en", "hello"},
		{"fake", "de", "hallo"},
		{"fake", "", "hello"},
	} {
		if cache.Key("entities", other, "", "") == base {
			t.Fatalf("%v collides with the base key", other)
		}
	}
}

func TestEntitiesLanguageFlagIsPassedToProvider(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace worked in London.")

	runEntities(t, &State{}, "--file", file, "--provider", "fake")
	if fakeEnts.lastOpts.Language != "" {
		t.Fatalf("no --lang must send an empty language, got %q", fakeEnts.lastOpts.Language)
	}

	runEntities(t, &State{}, "--file", file, "--provider", "fake", "--lang", "en")
	if fakeEnts.lastOpts.Language != "en" {
		t.Fatalf("--lang en sent %q", fakeEnts.lastOpts.Language)
	}

	// An explicit --lang auto overrides a configured entities.language.
	cfg := config.Default()
	cfg.Entities.Language = "de"
	runEntities(t, &State{Cfg: cfg}, "--file", file, "--provider", "fake", "--lang", "auto")
	if fakeEnts.lastOpts.Language != "" {
		t.Fatalf("--lang auto sent %q, want empty", fakeEnts.lastOpts.Language)
	}

	// Without a flag, entities.language from config is used.
	cfg2 := config.Default()
	cfg2.Entities.Language = "de"
	runEntities(t, &State{Cfg: cfg2}, "--file", file, "--provider", "fake")
	if fakeEnts.lastOpts.Language != "de" {
		t.Fatalf("config entities.language sent %q, want de", fakeEnts.lastOpts.Language)
	}
}

func TestEntitiesTimeoutFlagReachesProvider(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	runEntities(t, &State{}, "--file", file, "--provider", "fake", "--timeout", "5s")
	if fakeEnts.lastOpts.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v", fakeEnts.lastOpts.Timeout)
	}
	runEntities(t, &State{}, "--file", file, "--provider", "fake")
	if fakeEnts.lastOpts.Timeout != entity.DefaultTimeout {
		t.Fatalf("default timeout = %v, want %v", fakeEnts.lastOpts.Timeout, entity.DefaultTimeout)
	}
}

func TestEntitiesMultipleDocuments(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"Ada Lovelace","source":"blog"}`+"\n"+
			`{"id":"b","text":"London"}`+"\n")

	env, _ := runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake")
	data := env["data"].(map[string]any)
	docs, ok := data["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Fatalf("data.documents = %v", data["documents"])
	}
	if _, has := data["entities"]; has {
		t.Fatal("a batch must not also carry a top-level entities key")
	}
	d0 := docs[0].(map[string]any)
	if d0["id"] != "a" || d0["provider"] != "fake" {
		t.Fatalf("document = %v", d0)
	}
	if len(d0["entities"].([]any)) != 3 {
		t.Fatalf("entities = %v", d0["entities"])
	}
	if env["meta"].(map[string]any)["documents"] != float64(2) {
		t.Fatalf("meta.documents = %v", env["meta"].(map[string]any)["documents"])
	}
}

func TestEntitiesNDJSONCarriesDocIDAndFields(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"Ada Lovelace","source":"blog","type":"input-field"}`+"\n")

	st := &State{OutputFormat: "ndjson"}
	_, out := runEntities(t, st, "--file", file, "--input-format", "jsonl", "--provider", "fake", "--top", "2")

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("ndjson lines = %d, want one per entity:\n%s", len(lines), out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	if rec["doc_id"] != "a" {
		t.Fatalf("doc_id = %v", rec["doc_id"])
	}
	if rec["name"] != "Ada Lovelace" {
		t.Fatalf("name = %v", rec["name"])
	}
	if rec["source"] != "blog" {
		t.Fatalf("passthrough field lost: %v", rec)
	}
	// A passthrough field must never shadow a computed key.
	if rec["type"] != "PERSON" {
		t.Fatalf("type = %v, want the computed entity type to win", rec["type"])
	}
	if rec["provider"] != "fake" {
		t.Fatalf("provider = %v", rec["provider"])
	}
}

func TestEntitiesCSVColumns(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	st := &State{OutputFormat: "csv"}
	_, out := runEntities(t, st, "--file", file, "--provider", "fake")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "doc_id,name,type,salience,probability,mentions,wikipedia_url" {
		t.Fatalf("header = %q", lines[0])
	}
	if len(lines) != 4 {
		t.Fatalf("rows = %d, want a header plus one row per entity:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[1], "0,Ada Lovelace,PERSON,0.6,0.98,2,https://") {
		t.Fatalf("row = %q", lines[1])
	}
}

func TestEntitiesTextOutput(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	st := &State{OutputFormat: "text"}
	_, out := runEntities(t, st, "--file", file, "--provider", "fake")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("text lines = %d, want one per entity:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "Ada Lovelace") || !strings.Contains(lines[0], "PERSON") || !strings.Contains(lines[0], "0.6000") {
		t.Fatalf("line = %q, want the salience score rendered", lines[0])
	}
	if !strings.Contains(lines[0], "2 mentions") || !strings.Contains(lines[1], "1 mention") {
		t.Fatalf("mention counts not rendered:\n%s", out)
	}
}

func TestEntitiesProviderErrorPropagates(t *testing.T) {
	fakeEnts.reset()
	fakeEnts.err = errs.New(errs.CodeAuthDenied, "nope").WithHint("enable the API")
	defer fakeEnts.reset()

	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	root, _ := newTestRoot(t, &State{})
	root.SetArgs([]string{"entities", "--file", file, "--provider", "fake"})
	err := root.Execute()
	var e *errs.E
	if err == nil || !asErr(err, &e) || e.Code != errs.CodeAuthDenied {
		t.Fatalf("err = %v, want auth_denied surfaced unchanged", err)
	}
	if errs.ExitCode(err) != 2 {
		t.Fatalf("exit code = %d, want 2", errs.ExitCode(err))
	}
}

func TestEntitiesAlias(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace.")
	root, buf := newTestRoot(t, &State{})
	root.SetArgs([]string{"ents", "--file", file, "--provider", "fake"})
	if err := root.Execute(); err != nil {
		t.Fatalf("ents alias: %v", err)
	}
	if !strings.Contains(buf.String(), "Ada Lovelace") {
		t.Fatalf("alias produced %q", buf.String())
	}
}

// aggregateFixture wires the fake provider to hand three documents their own
// entities, and returns the JSONL file that produces them.
func aggregateFixture(t *testing.T) string {
	t.Helper()
	fakeEnts.reset()
	fakeEnts.byText = map[string][]entity.Entity{
		"one": {
			{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.5, MentionCount: 3,
				WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace", MID: "/m/0ff4d"},
			{Name: "London", Type: "LOCATION", Salience: 0.3, MentionCount: 1},
			{Name: "Apple", Type: "ORGANIZATION", Salience: 0.2, MentionCount: 1},
		},
		"two": {
			{Name: "ada lovelace", Type: "PERSON", Salience: 0.2, MentionCount: 1},
			{Name: "Charles Babbage", Type: "PERSON", Salience: 0.6, MentionCount: 2},
			{Name: "apple", Type: "CONSUMER_GOOD", Salience: 0.2, MentionCount: 1},
		},
		"three": {
			{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.1, MentionCount: 1},
		},
	}
	return writeTemp(t, "docs.jsonl",
		`{"id":"a","text":"one"}`+"\n"+
			`{"id":"b","text":"two"}`+"\n"+
			`{"id":"c","text":"three"}`+"\n")
}

func TestEntitiesAggregateJSONShape(t *testing.T) {
	file := aggregateFixture(t)

	env, _ := runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake", "--aggregate")
	data := env["data"].(map[string]any)
	if data["documents"] != float64(3) {
		t.Fatalf("data.documents = %v, want 3", data["documents"])
	}
	if _, has := data["entities"]; !has {
		t.Fatalf("data = %v, want an entities list", data)
	}
	ents := data["entities"].([]any)
	// Ada: 0.5+0.2+0.1 = 0.8 over three documents; Babbage: 0.6 over one.
	if len(ents) != 5 {
		t.Fatalf("merged entities = %d, want 5:\n%v", len(ents), ents)
	}
	ada := ents[0].(map[string]any)
	if ada["name"] != "Ada Lovelace" || ada["type"] != "PERSON" {
		t.Fatalf("first merged entity = %v", ada)
	}
	if ada["combined_salience"] != 0.8 {
		t.Fatalf("combined_salience = %v, want 0.8", ada["combined_salience"])
	}
	if ada["avg_salience"] != 0.2667 {
		t.Fatalf("avg_salience = %v, want 0.2667 (rounded to 4 decimals)", ada["avg_salience"])
	}
	if ada["mentions"] != float64(5) || ada["documents"] != float64(3) {
		t.Fatalf("mentions/documents = %v/%v, want 5/3", ada["mentions"], ada["documents"])
	}
	if ada["wikipedia_url"] != "https://en.wikipedia.org/wiki/Ada_Lovelace" {
		t.Fatalf("wikipedia_url = %v", ada["wikipedia_url"])
	}
	// A batch under --aggregate must not also carry the per-document list.
	if _, has := data["documents"].([]any); has {
		t.Fatal("data.documents must be a count, not the per-document list")
	}
	// Apple the ORGANIZATION and apple the CONSUMER_GOOD stay apart.
	var apples int
	for _, e := range ents {
		if strings.EqualFold(e.(map[string]any)["name"].(string), "apple") {
			apples++
		}
	}
	if apples != 2 {
		t.Fatalf("apple entities = %d, want 2 (different types must not merge)", apples)
	}
	if env["meta"].(map[string]any)["documents"] != float64(3) {
		t.Fatalf("meta.documents = %v", env["meta"].(map[string]any)["documents"])
	}
}

// TestEntitiesAggregateFilterComposition pins where each filter runs: --types
// before the merge, --top after it.
func TestEntitiesAggregateFilterComposition(t *testing.T) {
	file := aggregateFixture(t)

	env, _ := runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake",
		"--aggregate", "--types", "PERSON")
	ents := env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 2 {
		t.Fatalf("--types PERSON merged %d entities, want 2", len(ents))
	}
	for _, e := range ents {
		if e.(map[string]any)["type"] != "PERSON" {
			t.Fatalf("--types leaked %v", e)
		}
	}

	// --top applies to the merged list, so the winner is the entity with the
	// highest combined salience (Ada, 0.8) and not the one with the highest
	// single-document salience (Babbage, 0.6).
	env, _ = runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake",
		"--aggregate", "--top", "1")
	ents = env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 1 || ents[0].(map[string]any)["name"] != "Ada Lovelace" {
		t.Fatalf("--top 1 = %v", ents)
	}

	// The salience threshold runs per document, before the merge: Ada's 0.2 and
	// 0.1 contributions drop out and only her 0.5 document survives.
	env, _ = runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake",
		"--aggregate", "--min-salience", "0.25")
	ents = env["data"].(map[string]any)["entities"].([]any)
	found := false
	for _, e := range ents {
		m := e.(map[string]any)
		if m["name"] == "Ada Lovelace" {
			found = true
			if m["combined_salience"] != 0.5 || m["documents"] != float64(1) {
				t.Fatalf("per-document threshold ran after the merge: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("Ada was filtered out entirely: %v", ents)
	}

	// --sort reorders the merged list without changing its contents.
	env, _ = runEntities(t, &State{}, "--file", file, "--input-format", "jsonl", "--provider", "fake",
		"--aggregate", "--sort", "name")
	ents = env["data"].(map[string]any)["entities"].([]any)
	if ents[0].(map[string]any)["name"] != "Ada Lovelace" || ents[1].(map[string]any)["name"] != "Apple" {
		t.Fatalf("--sort name = %v", ents)
	}
}

func TestEntitiesAggregateSingleDocument(t *testing.T) {
	fakeEnts.reset()
	file := writeTemp(t, "doc.txt", "Ada Lovelace arbeitete in London.")

	env, _ := runEntities(t, &State{}, "--file", file, "--provider", "fake", "--aggregate")
	data := env["data"].(map[string]any)
	if data["documents"] != float64(1) {
		t.Fatalf("data.documents = %v", data["documents"])
	}
	ents := data["entities"].([]any)
	if len(ents) != 3 {
		t.Fatalf("entities = %v", ents)
	}
	first := ents[0].(map[string]any)
	// With one document combined and average are the same number.
	if first["combined_salience"] != 0.6 || first["avg_salience"] != 0.6 {
		t.Fatalf("single-document aggregate = %v", first)
	}
	if first["mentions"] != float64(2) || first["documents"] != float64(1) {
		t.Fatalf("mentions/documents = %v/%v", first["mentions"], first["documents"])
	}
}

func TestEntitiesAggregateOutputFormats(t *testing.T) {
	file := aggregateFixture(t)

	_, csvOut := runEntities(t, &State{OutputFormat: "csv"}, "--file", file, "--input-format", "jsonl",
		"--provider", "fake", "--aggregate")
	lines := strings.Split(strings.TrimSpace(csvOut), "\n")
	if lines[0] != "name,type,combined_salience,avg_salience,mentions,documents,wikipedia_url" {
		t.Fatalf("csv header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Ada Lovelace,PERSON,0.8,0.2667,5,3,https://") {
		t.Fatalf("csv row = %q", lines[1])
	}

	file = aggregateFixture(t)
	_, ndOut := runEntities(t, &State{OutputFormat: "ndjson"}, "--file", file, "--input-format", "jsonl",
		"--provider", "fake", "--aggregate")
	ndLines := strings.Split(strings.TrimSpace(ndOut), "\n")
	if len(ndLines) != 5 {
		t.Fatalf("ndjson lines = %d, want one per merged entity:\n%s", len(ndLines), ndOut)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(ndLines[0]), &rec); err != nil {
		t.Fatalf("decode %q: %v", ndLines[0], err)
	}
	if rec["name"] != "Ada Lovelace" || rec["combined_salience"] != 0.8 || rec["documents"] != float64(3) {
		t.Fatalf("ndjson record = %v", rec)
	}
	if _, has := rec["doc_id"]; has {
		t.Fatalf("a merged entity must not claim a single doc_id: %v", rec)
	}

	file = aggregateFixture(t)
	_, textOut := runEntities(t, &State{OutputFormat: "text"}, "--file", file, "--input-format", "jsonl",
		"--provider", "fake", "--aggregate")
	textLines := strings.Split(strings.TrimSpace(textOut), "\n")
	if len(textLines) != 5 {
		t.Fatalf("text lines = %d:\n%s", len(textLines), textOut)
	}
	if !strings.Contains(textLines[0], "Ada Lovelace") || !strings.Contains(textLines[0], "0.8000") ||
		!strings.Contains(textLines[0], "avg 0.2667") || !strings.Contains(textLines[0], "3 documents") {
		t.Fatalf("text line = %q", textLines[0])
	}
}

// asErr is errors.As with a shorter name, used in table assertions.
func asErr(err error, target **errs.E) bool {
	e, ok := err.(*errs.E)
	if ok {
		*target = e
	}
	return ok
}
