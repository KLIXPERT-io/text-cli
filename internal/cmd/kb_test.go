package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/knowledge"
	"github.com/spf13/cobra"
)

// fakeKBSource stands in for a knowledge database: it never touches the
// network, counts calls so the cache tests can prove a second run was free, and
// can be told to fail a specific title.
type fakeKBSource struct {
	mu    sync.Mutex
	calls int
	// lookups records every (title, lang) pair asked for, in call order.
	lookups []string
	// missing titles answer with not_found; failing titles answer with a
	// non-degradable error.
	missing map[string]bool
	failing map[string]bool
	hits    []knowledge.SearchHit
	// delay makes the concurrency of --enrich observable: with a serial
	// implementation the goroutine count never exceeds one.
	delay time.Duration
	// maxConcurrent is the high-water mark of simultaneous lookups.
	inFlight, maxConcurrent int
}

func (f *fakeKBSource) Name() string { return "fake-kb" }

func (f *fakeKBSource) Lookup(_ context.Context, title, lang string) (*knowledge.Article, error) {
	f.mu.Lock()
	f.calls++
	f.lookups = append(f.lookups, title+"/"+lang)
	f.inFlight++
	if f.inFlight > f.maxConcurrent {
		f.maxConcurrent = f.inFlight
	}
	missing, failing, delay := f.missing[title], f.failing[title], f.delay
	f.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	switch {
	case failing:
		return nil, errs.New(errs.CodeRateLimited, "slow down").WithRetry(5)
	case missing:
		return nil, errs.Newf(errs.CodeNotFound, "no article titled %q", title).
			WithHint("Try `text kb search`.")
	}
	return &knowledge.Article{
		Title:       title,
		Description: "description of " + title,
		Extract:     "Extract of " + title + ". " + strings.Repeat("Ein sehr langer Satz über Ada. ", 6),
		URL:         "https://" + lang + ".wikipedia.org/wiki/" + strings.ReplaceAll(title, " ", "_"),
		Lang:        lang,
	}, nil
}

func (f *fakeKBSource) Search(_ context.Context, _, _ string, limit int) ([]knowledge.SearchHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.hits) > limit {
		return f.hits[:limit], nil
	}
	return f.hits, nil
}

func (f *fakeKBSource) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.lookups, f.missing, f.failing = 0, nil, nil, nil
	f.delay, f.inFlight, f.maxConcurrent = 0, 0, 0
	f.hits = []knowledge.SearchHit{
		{Title: "Analytical Engine", Description: "A mechanical general-purpose computer", URL: "https://en.wikipedia.org/wiki/Analytical_Engine", Score: 2},
		{Title: "Difference engine", Description: "An automatic mechanical calculator", URL: "https://en.wikipedia.org/wiki/Difference_engine"},
	}
}

func (f *fakeKBSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var fakeKB = &fakeKBSource{}

// fakeKBEntities is this file's own entity provider, deliberately separate from
// the one entities_test.go registers: the enrichment tests need entities whose
// wikipedia_url they control.
type fakeKBEntityProvider struct{ ents []entity.Entity }

func (f *fakeKBEntityProvider) Name() string { return "fake-kb-ents" }

func (f *fakeKBEntityProvider) AnalyzeEntities(_ context.Context, _ string, _ entity.Options) (*entity.Result, error) {
	return &entity.Result{Provider: "fake-kb-ents", Language: "de", LanguageSupported: true, Entities: f.ents}, nil
}

var fakeKBEnts = &fakeKBEntityProvider{}

func init() {
	knowledge.Register("fake-kb", func() (knowledge.Source, error) { return fakeKB, nil })
	entity.Register("fake-kb-ents", func() (entity.Provider, error) { return fakeKBEnts, nil })
}

// newKBRoot mirrors the persistent flags the real root owns so the kb command
// sees the flag set it does in production, with the cache pointed at a temp dir.
func newKBRoot(t *testing.T, st *State) (*cobra.Command, *bytes.Buffer) {
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
	pf.StringArrayVarP(&st.Files, "file", "f", nil, "")
	pf.StringVar(&st.InputFormat, "input-format", "text", "")
	pf.StringVar(&st.TextField, "text-field", "text", "")
	pf.StringVar(&st.IDField, "id-field", "id", "")
	pf.BoolVar(&st.NoCache, "no-cache", false, "")
	pf.BoolVar(&st.Refresh, "refresh", false, "")
	pf.BoolVar(&st.Verbose, "verbose", false, "")
	pf.BoolVar(&st.Quiet, "quiet", false, "")
	pf.DurationVar(&st.CacheTTL, "cache-ttl", 0, "")

	root.AddCommand(newKBCmd(), newEntitiesCmd())

	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetContext(context.WithValue(context.Background(), stateKey, st))
	return root, buf
}

// runKB executes the command and decodes the JSON envelope.
func runKB(t *testing.T, st *State, args ...string) (map[string]any, string) {
	t.Helper()
	root, buf := newKBRoot(t, st)
	root.SetArgs(args)
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

// writeTempKB writes a fixture file. It is this file's own helper so the kb
// tests do not depend on the entities test file's scaffolding.
func writeTempKB(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// decodeLastJSON pulls the envelope out of a buffer that also carries stderr
// warnings: the test root points both streams at one buffer, and the envelope
// is the pretty-printed object that starts at its own line.
func decodeLastJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "{" {
			var env map[string]any
			if err := json.Unmarshal([]byte(strings.Join(lines[i:], "\n")), &env); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}
			return env
		}
	}
	t.Fatalf("no JSON envelope in %q", out)
	return nil
}

func runKBErr(t *testing.T, st *State, args ...string) error {
	t.Helper()
	root, _ := newKBRoot(t, st)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		t.Fatalf("execute %v: expected an error", args)
	}
	return err
}

func TestKBLookupSingleJSONShape(t *testing.T) {
	fakeKB.reset()

	env, _ := runKB(t, &State{}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace")

	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is %T, want the article object", env["data"])
	}
	if data["title"] != "Ada Lovelace" {
		t.Fatalf("data.title = %v", data["title"])
	}
	if data["description"] != "description of Ada Lovelace" {
		t.Fatalf("data.description = %v", data["description"])
	}
	if data["lang"] != "en" {
		t.Fatalf("data.lang = %v, want the default edition", data["lang"])
	}
	meta := env["meta"].(map[string]any)
	if meta["provider"] != "fake-kb" {
		t.Fatalf("meta.provider = %v", meta["provider"])
	}
	if meta["api_calls"] != float64(1) {
		t.Fatalf("meta.api_calls = %v, want 1", meta["api_calls"])
	}
	if meta["cached"] != false {
		t.Fatalf("meta.cached = %v on a fresh call", meta["cached"])
	}
}

// TestKBLookupBatchUsesDocuments pins the repo's batch convention: many results
// live under data.documents, one result is the object itself.
func TestKBLookupBatchUsesDocuments(t *testing.T) {
	fakeKB.reset()

	env, _ := runKB(t, &State{}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace", "Charles Babbage")

	data := env["data"].(map[string]any)
	docs, ok := data["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Fatalf("data.documents = %v", data["documents"])
	}
	// Request order, not completion order.
	if docs[0].(map[string]any)["title"] != "Ada Lovelace" || docs[1].(map[string]any)["title"] != "Charles Babbage" {
		t.Fatalf("documents out of order: %v", docs)
	}
	if env["meta"].(map[string]any)["documents"] != float64(2) {
		t.Fatalf("meta.documents = %v", env["meta"].(map[string]any)["documents"])
	}
}

func TestKBLookupLangSelectsEdition(t *testing.T) {
	fakeKB.reset()

	env, _ := runKB(t, &State{}, "kb", "lookup", "--source", "fake-kb", "--lang", "de", "Große Koalition")

	if got := env["data"].(map[string]any)["lang"]; got != "de" {
		t.Fatalf("lang = %v, want de", got)
	}
	if got := fakeKB.lookups[0]; got != "Große Koalition/de" {
		t.Fatalf("source was asked for %q", got)
	}
	// --lang auto must resolve to a real edition: there is no auto.wikipedia.org.
	fakeKB.reset()
	runKB(t, &State{}, "kb", "lookup", "--source", "fake-kb", "--lang", "auto", "Ada Lovelace")
	if got := fakeKB.lookups[0]; got != "Ada Lovelace/en" {
		t.Fatalf("--lang auto asked for %q, want the en edition", got)
	}
}

func TestKBLookupCachesAcrossRuns(t *testing.T) {
	fakeKB.reset()
	dir := t.TempDir()
	store := func() *cache.Store { return cache.New(dir, time.Hour) }

	runKB(t, &State{Cache: store()}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace")
	if fakeKB.callCount() != 1 {
		t.Fatalf("first run made %d calls, want 1", fakeKB.callCount())
	}

	env, _ := runKB(t, &State{Cache: store()}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace")
	if fakeKB.callCount() != 1 {
		t.Fatalf("second run made %d calls total, want the cache to serve it", fakeKB.callCount())
	}
	meta := env["meta"].(map[string]any)
	if meta["cached"] != true {
		t.Fatalf("meta.cached = %v, want true", meta["cached"])
	}
	if meta["api_calls"] != float64(0) {
		t.Fatalf("meta.api_calls = %v, want 0", meta["api_calls"])
	}
	if meta["cached_at"] == nil || meta["cached_at"] == "" {
		t.Fatal("meta.cached_at is empty on a cached answer")
	}
	if ttl, ok := meta["ttl_remaining_sec"].(float64); !ok || ttl <= 0 {
		t.Fatalf("meta.ttl_remaining_sec = %v", meta["ttl_remaining_sec"])
	}

	// --refresh pays again; --no-cache never reads or writes. Both go through
	// the flag, not the struct: the persistent flag's default would overwrite a
	// field set by hand.
	runKB(t, &State{Cache: store()}, "kb", "lookup", "--source", "fake-kb", "--refresh", "Ada Lovelace")
	if fakeKB.callCount() != 2 {
		t.Fatalf("--refresh made %d calls total, want 2", fakeKB.callCount())
	}
	runKB(t, &State{Cache: store()}, "kb", "lookup", "--source", "fake-kb", "--no-cache", "Ada Lovelace")
	if fakeKB.callCount() != 3 {
		t.Fatalf("--no-cache made %d calls total, want 3", fakeKB.callCount())
	}
}

// TestKBLookupDeduplicatesTitles: the pipeline this exists for pipes an entity
// list in, and entity lists repeat.
func TestKBLookupDeduplicatesTitles(t *testing.T) {
	fakeKB.reset()

	env, _ := runKB(t, &State{}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace", "Ada Lovelace")

	if fakeKB.callCount() != 1 {
		t.Fatalf("made %d calls for one repeated title", fakeKB.callCount())
	}
	if got := env["meta"].(map[string]any)["api_calls"]; got != float64(1) {
		t.Fatalf("meta.api_calls = %v, want 1", got)
	}
}

func TestKBLookupTitlesFromStdinLines(t *testing.T) {
	fakeKB.reset()
	st := &State{}
	root, buf := newKBRoot(t, st)
	// The titles arrive through --file "-" so the test does not depend on the
	// TTY detection os.Stdin would trigger.
	file := writeTempKB(t, "titles.txt", "Ada Lovelace\n\nCharles Babbage\n")
	root.SetArgs([]string{"kb", "lookup", "--source", "fake-kb", "--file", file})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	docs := env["data"].(map[string]any)["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("documents = %v, want one per non-empty line", docs)
	}
	// The *output* is in request order — kbLookupMany reassembles it that way,
	// and that is the guarantee a consumer relies on.
	if got := docs[0].(map[string]any)["title"]; got != "Ada Lovelace" {
		t.Errorf("documents[0].title = %v, want the first line", got)
	}
	if got := docs[1].(map[string]any)["title"]; got != "Charles Babbage" {
		t.Errorf("documents[1].title = %v, want the second line", got)
	}
	// The order the *lookups* happened in is deliberately not asserted: they
	// run on a pool of concurrent workers, so which one reaches the source
	// first is scheduling, not behaviour. Assert the set instead — that both
	// lines became a lookup, and the blank line did not.
	if got := sortedCopy(fakeKB.lookups); !reflect.DeepEqual(got, []string{"Ada Lovelace/en", "Charles Babbage/en"}) {
		t.Fatalf("lookups = %v, want exactly one per non-empty line", got)
	}
}

// sortedCopy sorts a copy, so an assertion on a set does not reorder the slice
// a later assertion in the same test might read.
func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestKBLookupSingleMissIsNotFound(t *testing.T) {
	fakeKB.reset()
	fakeKB.missing = map[string]bool{"Nichtvorhanden": true}

	err := runKBErr(t, &State{}, "kb", "lookup", "--source", "fake-kb", "Nichtvorhanden")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeNotFound {
		t.Fatalf("error = %v, want not_found", err)
	}
	if errs.ExitCode(err) != 4 {
		t.Fatalf("exit code = %d, want 4", errs.ExitCode(err))
	}
}

// TestKBLookupBatchSkipsMisses: a list of entity names always contains things
// no encyclopedia has, and failing the whole run over one of them would make
// the pipeline unusable.
func TestKBLookupBatchSkipsMisses(t *testing.T) {
	fakeKB.reset()
	fakeKB.missing = map[string]bool{"Nichtvorhanden": true}

	st := &State{}
	root, buf := newKBRoot(t, st)
	root.SetArgs([]string{"kb", "lookup", "--source", "fake-kb", "Ada Lovelace", "Nichtvorhanden", "Charles Babbage"})
	if err := root.Execute(); err != nil {
		t.Fatalf("a batch must survive a miss, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "warning: no article for \"Nichtvorhanden\"") {
		t.Fatalf("the miss must be reported on stderr, got %q", out)
	}
	// The envelope is the last JSON document in the buffer; decode from the
	// first "{" that follows the warning line.
	env := decodeLastJSON(t, out)
	docs := env["data"].(map[string]any)["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("documents = %v, want the two that resolved", docs)
	}
}

// TestKBLookupBatchAbortsOnNonMiss: a rate limit would repeat for every
// remaining title, so degrading past it would just burn the quota.
func TestKBLookupBatchAbortsOnNonMiss(t *testing.T) {
	fakeKB.reset()
	fakeKB.failing = map[string]bool{"Charles Babbage": true}

	err := runKBErr(t, &State{}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace", "Charles Babbage")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeRateLimited {
		t.Fatalf("error = %v, want rate_limited", err)
	}
}

func TestKBLookupUnknownSource(t *testing.T) {
	err := runKBErr(t, &State{}, "kb", "lookup", "--source", "encarta", "Ada Lovelace")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args", err)
	}
	if !strings.Contains(e.Hint, knowledge.SourceWikipedia) {
		t.Fatalf("hint %q must list the known sources", e.Hint)
	}
}

func TestKBLookupCSVAndTextOutput(t *testing.T) {
	fakeKB.reset()

	_, csvOut := runKB(t, &State{OutputFormat: "csv"}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace")
	lines := strings.Split(strings.TrimSpace(csvOut), "\n")
	if lines[0] != "title,description,url,lang" {
		t.Fatalf("csv header = %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Ada Lovelace,description of Ada Lovelace,") {
		t.Fatalf("csv row = %q", lines[1])
	}

	fakeKB.reset()
	_, textOut := runKB(t, &State{OutputFormat: "text"}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace")
	textLines := strings.Split(strings.TrimSpace(textOut), "\n")
	if textLines[0] != "Ada Lovelace" || textLines[1] != "description of Ada Lovelace" {
		t.Fatalf("text output = %q", textOut)
	}
	for _, l := range textLines {
		if len([]rune(l)) > textWrapWidth {
			t.Fatalf("line %q is %d runes, want the extract wrapped at %d", l, len([]rune(l)), textWrapWidth)
		}
	}

	fakeKB.reset()
	_, ndOut := runKB(t, &State{OutputFormat: "ndjson"}, "kb", "lookup", "--source", "fake-kb", "Ada Lovelace", "Charles Babbage")
	if got := strings.Count(strings.TrimSpace(ndOut), "\n") + 1; got != 2 {
		t.Fatalf("ndjson emitted %d lines, want one article per line:\n%s", got, ndOut)
	}
}

func TestKBSearch(t *testing.T) {
	fakeKB.reset()

	env, _ := runKB(t, &State{}, "kb", "search", "--source", "fake-kb", "analytical engine")

	data := env["data"].(map[string]any)
	if data["query"] != "analytical engine" {
		t.Fatalf("data.query = %v", data["query"])
	}
	hits := data["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits = %v", hits)
	}
	first := hits[0].(map[string]any)
	if first["title"] != "Analytical Engine" || first["score"] != float64(2) {
		t.Fatalf("first hit = %v", first)
	}
	if env["meta"].(map[string]any)["api_calls"] != float64(1) {
		t.Fatalf("meta.api_calls = %v", env["meta"].(map[string]any)["api_calls"])
	}
}

func TestKBSearchLimitAndCache(t *testing.T) {
	fakeKB.reset()
	dir := t.TempDir()

	env, _ := runKB(t, &State{Cache: cache.New(dir, time.Hour)}, "kb", "search", "--source", "fake-kb", "--limit", "1", "engine")
	if hits := env["data"].(map[string]any)["hits"].([]any); len(hits) != 1 {
		t.Fatalf("--limit 1 returned %d hits", len(hits))
	}

	env, _ = runKB(t, &State{Cache: cache.New(dir, time.Hour)}, "kb", "search", "--source", "fake-kb", "--limit", "1", "engine")
	if fakeKB.callCount() != 1 {
		t.Fatalf("the second search made a call; searches must be cached")
	}
	if env["meta"].(map[string]any)["cached"] != true {
		t.Fatalf("meta.cached = %v, want true", env["meta"].(map[string]any)["cached"])
	}
	// A different --limit is a different question and must not reuse the cache.
	runKB(t, &State{Cache: cache.New(dir, time.Hour)}, "kb", "search", "--source", "fake-kb", "--limit", "2", "engine")
	if fakeKB.callCount() != 2 {
		t.Fatalf("--limit 2 reused the --limit 1 cache entry")
	}
}

// --- --enrich ----------------------------------------------------------------

// withFakeEnrichSource points `text entities --enrich` at the fake source. The
// real one is Wikipedia, and no test may reach it.
func withFakeEnrichSource(t *testing.T) {
	t.Helper()
	prev := enrichSourceName
	enrichSourceName = "fake-kb"
	t.Cleanup(func() { enrichSourceName = prev })
}

func TestEntitiesEnrichAttachesKnowledge(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, MentionCount: 2, WikipediaURL: "https://de.wikipedia.org/wiki/Ada_Lovelace"},
		{Name: "London", Type: "LOCATION", Salience: 0.3, MentionCount: 1, WikipediaURL: "https://en.wikipedia.org/wiki/London"},
		{Name: "Rechenmaschine", Type: "OTHER", Salience: 0.1, MentionCount: 1},
	}

	env, _ := runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich", "Ada Lovelace in London")

	ents := env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 3 {
		t.Fatalf("entities = %v", ents)
	}
	ada := ents[0].(map[string]any)
	kb, ok := ada["knowledge"].(map[string]any)
	if !ok {
		t.Fatalf("first entity has no knowledge object: %v", ada)
	}
	if kb["description"] != "description of Ada Lovelace" {
		t.Fatalf("knowledge.description = %v", kb["description"])
	}
	if !strings.HasPrefix(kb["extract"].(string), "Extract of Ada Lovelace") {
		t.Fatalf("knowledge.extract = %v", kb["extract"])
	}
	// The language comes from the entity's own URL, not from --lang: the
	// provider already picked an edition for this entity.
	if kb["lang"] != "de" {
		t.Fatalf("knowledge.lang = %v, want de from the wikipedia_url", kb["lang"])
	}
	if kb["source"] != "fake-kb" {
		t.Fatalf("knowledge.source = %v", kb["source"])
	}
	// The entity's own fields survive alongside it.
	if ada["name"] != "Ada Lovelace" || ada["salience"] != 0.6 {
		t.Fatalf("enrichment reshaped the entity: %v", ada)
	}
	// An entity with no wikipedia_url gets nothing, and no lookup is wasted.
	if _, has := ents[2].(map[string]any)["knowledge"]; has {
		t.Fatalf("an entity without a wikipedia_url must carry no knowledge: %v", ents[2])
	}
	if fakeKB.callCount() != 2 {
		t.Fatalf("made %d lookups, want one per entity with a URL", fakeKB.callCount())
	}
	if got := env["meta"].(map[string]any)["api_calls"]; got != float64(3) {
		t.Fatalf("meta.api_calls = %v, want the entity call plus two lookups", got)
	}
}

// TestEntitiesEnrichRunsAfterFiltering is the money test: a lookup must never
// be paid for an entity --top is about to discard.
func TestEntitiesEnrichRunsAfterFiltering(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
		{Name: "London", Type: "LOCATION", Salience: 0.3, WikipediaURL: "https://en.wikipedia.org/wiki/London"},
		{Name: "Charles Babbage", Type: "PERSON", Salience: 0.2, WikipediaURL: "https://en.wikipedia.org/wiki/Charles_Babbage"},
	}

	runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich", "--top", "1", "text")

	if fakeKB.callCount() != 1 {
		t.Fatalf("made %d lookups for --top 1, want 1", fakeKB.callCount())
	}
	if fakeKB.lookups[0] != "Ada Lovelace/en" {
		t.Fatalf("looked up %q", fakeKB.lookups[0])
	}

	fakeKB.reset()
	runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich", "--types", "LOCATION", "text")
	if fakeKB.callCount() != 1 || fakeKB.lookups[0] != "London/en" {
		t.Fatalf("--types paid for %v", fakeKB.lookups)
	}
}

// TestEntitiesEnrichDegradesOnFailure: an encyclopedia miss must never cost the
// caller the entities it already paid for.
func TestEntitiesEnrichDegradesOnFailure(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKB.missing = map[string]bool{"Ada Lovelace": true}
	fakeKB.failing = map[string]bool{"London": true}
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
		{Name: "London", Type: "LOCATION", Salience: 0.3, WikipediaURL: "https://en.wikipedia.org/wiki/London"},
	}

	st := &State{}
	root, buf := newKBRoot(t, st)
	root.SetArgs([]string{"entities", "--provider", "fake-kb-ents", "--enrich", "--verbose", "text"})
	if err := root.Execute(); err != nil {
		t.Fatalf("an enrichment failure must not fail the command: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "warning: enrich \"Ada Lovelace\"") {
		t.Fatalf("--verbose must report the miss on stderr, got %q", out)
	}
	env := decodeLastJSON(t, out)
	ents := env["data"].(map[string]any)["entities"].([]any)
	if len(ents) != 2 {
		t.Fatalf("entities = %v, want both kept", ents)
	}
	for _, e := range ents {
		if _, has := e.(map[string]any)["knowledge"]; has {
			t.Fatalf("a failed lookup must attach nothing: %v", e)
		}
	}
}

// TestEntitiesEnrichIsQuietWithoutVerbose keeps a routine miss out of stderr.
func TestEntitiesEnrichIsQuietWithoutVerbose(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKB.missing = map[string]bool{"Ada Lovelace": true}
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
	}

	_, out := runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich", "text")
	if strings.Contains(out, "warning") {
		t.Fatalf("a miss must be silent without --verbose, got %q", out)
	}
}

// TestEntitiesEnrichIsConcurrentAndOrdered: the lookups run in parallel, and
// the output is still in salience order.
func TestEntitiesEnrichIsConcurrentAndOrdered(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKB.delay = 20 * time.Millisecond
	var ents []entity.Entity
	names := []string{"A", "B", "C", "D", "E", "F"}
	for i, n := range names {
		ents = append(ents, entity.Entity{
			Name: n, Type: "PERSON", Salience: float64(len(names)-i) / 10,
			WikipediaURL: "https://en.wikipedia.org/wiki/" + n,
		})
	}
	fakeKBEnts.ents = ents

	env, _ := runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich", "text")

	fakeKB.mu.Lock()
	peak := fakeKB.maxConcurrent
	fakeKB.mu.Unlock()
	if peak < 2 {
		t.Fatalf("peak concurrency was %d; the lookups must not be serial", peak)
	}
	if peak > kbEnrichWorkers {
		t.Fatalf("peak concurrency was %d, want at most %d", peak, kbEnrichWorkers)
	}
	got := env["data"].(map[string]any)["entities"].([]any)
	for i, want := range names {
		if got[i].(map[string]any)["name"] != want {
			t.Fatalf("entity %d is %v, want %q — enrichment must not reorder output", i, got[i], want)
		}
	}
}

// TestEntitiesEnrichDeduplicatesAcrossDocuments: the same entity in ten
// documents is one article.
func TestEntitiesEnrichDeduplicatesAcrossDocuments(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
	}
	file := writeTempKB(t, "docs.txt", "erster Satz\nzweiter Satz\ndritter Satz\n")

	env, _ := runKB(t, &State{}, "entities", "--provider", "fake-kb-ents", "--enrich",
		"--file", file, "--input-format", "lines")

	if fakeKB.callCount() != 1 {
		t.Fatalf("made %d lookups for one entity across three documents", fakeKB.callCount())
	}
	docs := env["data"].(map[string]any)["documents"].([]any)
	if len(docs) != 3 {
		t.Fatalf("documents = %v", docs)
	}
	for _, d := range docs {
		e := d.(map[string]any)["entities"].([]any)[0].(map[string]any)
		if _, ok := e["knowledge"]; !ok {
			t.Fatalf("every document's copy must be enriched: %v", e)
		}
	}
}

func TestEntitiesEnrichCSVAddsDescription(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, MentionCount: 2, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
	}

	_, out := runKB(t, &State{OutputFormat: "csv"}, "entities", "--provider", "fake-kb-ents", "--enrich", "text")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasSuffix(lines[0], ",description") {
		t.Fatalf("csv header = %q, want the description column appended", lines[0])
	}
	if !strings.HasSuffix(lines[1], ",description of Ada Lovelace") {
		t.Fatalf("csv row = %q", lines[1])
	}
}

// TestEntitiesEnrichWithAggregateWarns: the two flags only look compatible.
func TestEntitiesEnrichWithAggregateWarns(t *testing.T) {
	withFakeEnrichSource(t)
	fakeKB.reset()
	fakeKBEnts.ents = []entity.Entity{
		{Name: "Ada Lovelace", Type: "PERSON", Salience: 0.6, WikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace"},
	}

	st := &State{}
	root, buf := newKBRoot(t, st)
	root.SetArgs([]string{"entities", "--provider", "fake-kb-ents", "--enrich", "--aggregate", "text"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(buf.String(), "--enrich is ignored with --aggregate") {
		t.Fatalf("expected a warning, got %q", buf.String())
	}
	if fakeKB.callCount() != 0 {
		t.Fatal("--aggregate must not pay for lookups it cannot show")
	}
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{"short stays on one line", "one two", 20, "one two"},
		{"breaks at a word boundary", "aaa bbb ccc ddd", 7, "aaa bbb\nccc ddd"},
		{"collapses whitespace", "  aaa   bbb  ", 20, "aaa bbb"},
		{"a long word is never split", "aaaaaaaaaa bb", 5, "aaaaaaaaaa\nbb"},
		{"zero width is a no-op", "aaa bbb", 0, "aaa bbb"},
		// Umlauts are two bytes and one column; wrapping must count columns.
		{"counts runes not bytes", "üüüü öööö", 9, "üüüü öööö"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrapText(tc.in, tc.width); got != tc.want {
				t.Fatalf("wrapText(%q, %d) = %q, want %q", tc.in, tc.width, got, tc.want)
			}
		})
	}
}
