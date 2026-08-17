package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/knowledge"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// kbTTL is how long an article stays cached. Encyclopedia articles do not
// change minute to minute, and a long TTL is what keeps a batch of fifty entity
// lookups from hammering a free public API. --cache-ttl still overrides it.
const kbTTL = 24 * time.Hour

// kbEnrichWorkers bounds the concurrency of `--enrich`. The lookups are
// independent network calls, so serial is needlessly slow, but a public API
// answering an unauthenticated client deserves a small number: four is enough
// to hide the latency and small enough not to look like an attack.
const kbEnrichWorkers = 4

// textWrapWidth is where --output text wraps an extract. 80 is the width a
// terminal and a diff both assume.
const textWrapWidth = 80

// enrichSourceName is the knowledge source `text entities --enrich` looks
// entities up in.
//
// It is not a flag: the join key is the entity's wikipedia_url, which only
// Wikipedia can answer, so offering a choice would offer a broken one. It is a
// var rather than a const purely so tests can point enrichment at a fake source
// instead of the real API.
var enrichSourceName = knowledge.SourceWikipedia

func newKBCmd() *cobra.Command {
	var (
		source  string
		timeout time.Duration
		limit   int
	)

	c := &cobra.Command{
		Use:     "kb",
		Aliases: []string{"knowledge"},
		Short:   "Look titles up in a knowledge database (Wikipedia)",
		Long: `kb reads a knowledge database — Wikipedia today — and returns the short
description and lead paragraph for a title.

It is the other half of ` + "`text entities`" + `: entities come back with a
wikipedia_url, and kb turns those into prose. The two compose directly:

  text entities --file post.md --output ndjson | jq -r .name | text kb lookup

Lookups are cached for 24h, so a batch that repeats a title pays once.`,
	}

	lookup := &cobra.Command{
		Use:   "lookup [title...]",
		Short: "Fetch articles by title",
		Long: `lookup fetches one article per title. Titles come from the arguments, or
one per line on stdin, so it sits at the end of a pipe.

A batch tolerates misses: a title with no article is reported on stderr and
left out of the results, because a list of entity names always contains a few
things no encyclopedia has. A single title that is missing is a not_found
error, so a scripted one-shot lookup still fails loudly.

Examples:
  text kb lookup "Ada Lovelace"
  text kb lookup "Ada Lovelace" "Charles Babbage" --output csv
  text kb lookup --lang de "Große Koalition"
  text entities --file post.md --output ndjson | jq -r .name | text kb lookup`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			titles, err := s.loadTitles(args)
			if err != nil {
				return err
			}
			src, err := openKBSource(source, timeout)
			if err != nil {
				return err
			}
			lang := resolveKBLanguage(s)

			reqs := make([]kbRequest, 0, len(titles))
			for _, t := range titles {
				reqs = append(reqs, kbRequest{Title: t, Lang: lang})
			}
			outcomes, stats := s.kbLookupMany(cmd.Context(), src, source, reqs, kbEnrichWorkers)

			articles := make([]*knowledge.Article, 0, len(outcomes))
			for _, o := range outcomes {
				if o.Err != nil {
					// A single lookup propagates every failure: `text kb lookup
					// X` that silently prints nothing would be worse than an
					// exit code. In a batch only a miss degrades — a rate limit
					// or a network failure would repeat for every remaining
					// title, so it still aborts.
					if len(reqs) == 1 || !isNotFound(o.Err) {
						return o.Err
					}
					if !s.Quiet {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: no article for %q (%s)\n", o.Req.Title, o.Req.Lang)
					}
					continue
				}
				articles = append(articles, o.Article)
			}

			meta := kbMeta(source, lang, len(articles), stats)

			var data any = map[string]any{"documents": articles}
			if len(articles) == 1 && len(reqs) == 1 {
				data = articles[0]
			}
			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    meta,
				Columns: []string{"title", "description", "url", "lang"},
				Rows:    articleRows(articles),
				Records: articleRecords(articles),
				Text:    func(w io.Writer) error { return writeArticlesText(w, articles) },
			})
		},
	}

	search := &cobra.Command{
		Use:   "search <query...>",
		Short: "Find candidate article titles for a query",
		Long: `search runs a full-text search and returns candidate titles, best first.

It is the recovery path for a failed lookup: article titles are exact, and
"Ada Lovelace" is a page while "ada lovelace biography" is not.

Examples:
  text kb search "analytical engine"
  text kb search --lang de "Erste Programmiererin" --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			items, err := s.LoadInput(args)
			if err != nil {
				return err
			}
			if len(items) > 1 {
				return errs.Newf(errs.CodeInvalidArgs, "search takes one query, got %d", len(items)).
					WithHint("Drop --input-format, or use `text kb lookup` to process a list.")
			}
			query := strings.TrimSpace(items[0].Text)

			src, err := openKBSource(source, timeout)
			if err != nil {
				return err
			}
			lang := resolveKBLanguage(s)

			hits, entry, err := s.kbSearch(cmd.Context(), src, source, query, lang, limit)
			if err != nil {
				return err
			}

			stats := kbStats{apiCalls: 1}
			if entry != nil {
				stats = kbStats{cacheHits: 1, oldest: entry}
			}
			meta := kbMeta(source, lang, len(hits), stats)

			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"query": query, "hits": hits},
				Meta:    meta,
				Columns: []string{"title", "description", "url", "score"},
				Rows:    hitRows(hits),
				Records: hitRecords(hits),
				Text:    func(w io.Writer) error { return writeHitsText(w, hits) },
			})
		},
	}
	search.Flags().IntVar(&limit, "limit", knowledge.DefaultSearchLimit, "maximum number of hits")

	pf := c.PersistentFlags()
	pf.StringVar(&source, "source", knowledge.SourceWikipedia, "knowledge source: "+strings.Join(knowledge.Names(), "|"))
	pf.DurationVar(&timeout, "timeout", knowledge.DefaultTimeout, "per-request timeout")

	c.AddCommand(lookup, search)
	return c
}

// openKBSource resolves --source and applies --timeout to it. A source with no
// notion of a deadline simply does not implement knowledge.TimeoutSetter, the
// same way a provider without a connection does not implement io.Closer.
func openKBSource(name string, timeout time.Duration) (knowledge.Source, error) {
	src, err := knowledge.Open(name)
	if err != nil {
		return nil, err
	}
	if ts, ok := src.(knowledge.TimeoutSetter); ok {
		ts.SetTimeout(timeout)
	}
	return src, nil
}

// resolveKBLanguage picks the language edition to read.
//
// Unlike the analysis commands there is nothing to detect from — a title is not
// a document — so "auto" resolves to a concrete default instead of meaning
// "figure it out". --lang de reads de.wikipedia.org.
func resolveKBLanguage(s *State) string {
	if l := s.Language(); l != textproc.LangAuto {
		return knowledge.NormalizeLang(string(l))
	}
	return knowledge.DefaultLang
}

// loadTitles resolves the list of titles: one per argument, or one per line
// from stdin or --file.
//
// Each argument is its own title — unlike the analysis commands, which join
// arguments into one document — because `text kb lookup A B` asking for one
// article called "A B" would be surprising.
//
// Piped input defaults to line-per-title for the same reason: the pipeline this
// exists for is `... | jq -r .name | text kb lookup`, and treating that as a
// single multi-line title would be useless. An explicit --input-format still
// wins, so --input-format jsonl --text-field name also works.
func (s *State) loadTitles(args []string) ([]string, error) {
	if len(args) > 0 {
		titles := make([]string, 0, len(args))
		for _, a := range args {
			if t := strings.TrimSpace(a); t != "" {
				titles = append(titles, t)
			}
		}
		if len(titles) == 0 {
			return nil, errs.New(errs.CodeEmptyInput, "no titles given").
				WithHint(`Pass a title: text kb lookup "Ada Lovelace".`)
		}
		return titles, nil
	}

	// A copy: the default upgrade is scoped to this call and must not leak into
	// the shared state a later command reads.
	st := *s
	if st.InputFormat == "" || input.Format(st.InputFormat) == input.FormatText {
		st.InputFormat = string(input.FormatLines)
	}
	items, err := st.LoadInput(nil)
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		t := strings.TrimSpace(it.Text)
		// A piped entity list repeats names across documents; looking the same
		// title up twice is free after the cache but still noise in the output.
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		titles = append(titles, t)
	}
	if len(titles) == 0 {
		return nil, errs.New(errs.CodeEmptyInput, "input contained no titles").
			WithHint("Check the upstream command in your pipeline.")
	}
	return titles, nil
}

// kbRequest is one article to resolve.
type kbRequest struct {
	Title string
	Lang  string
}

// kbOutcome is the answer for one request, in request order. Exactly one of
// Article and Err is set.
type kbOutcome struct {
	Req     kbRequest
	Article *knowledge.Article
	Err     error
}

// kbStats is the cache accounting behind the meta envelope.
type kbStats struct {
	apiCalls  int
	cacheHits int
	// oldest is the cache entry whose TTL expires first, i.e. the one that
	// bounds how stale the whole answer can be.
	oldest *cache.Entry
}

// kbLookupMany resolves many titles: cache first, then a bounded pool of
// concurrent fetches for the misses.
//
// The three phases are deliberate. Cache reads and writes happen on the calling
// goroutine — the store is a shared struct with lazily-initialised state, and
// hammering it from four workers would be a data race for no gain — while only
// the network calls, which are what actually cost time, run concurrently.
// Requests are de-duplicated first, so the same entity appearing in ten
// documents costs one call, and results are re-assembled in request order, so
// concurrency never reaches the output.
func (s *State) kbLookupMany(ctx context.Context, src knowledge.Source, srcName string, reqs []kbRequest, workers int) ([]kbOutcome, kbStats) {
	var stats kbStats
	if len(reqs) == 0 {
		return nil, stats
	}

	type slot struct {
		req     kbRequest
		article *knowledge.Article
		err     error
		fetched bool // needs a cache write
	}
	uniq := make([]slot, 0, len(reqs))
	index := make(map[string]int, len(reqs))
	order := make([]int, len(reqs))
	for i, r := range reqs {
		r.Title = knowledge.CanonicalTitle(r.Title)
		r.Lang = knowledge.NormalizeLang(r.Lang)
		key := r.Lang + "\x00" + r.Title
		j, ok := index[key]
		if !ok {
			j = len(uniq)
			index[key] = j
			uniq = append(uniq, slot{req: r})
		}
		order[i] = j
	}

	// Phase 1: the cache, serially.
	var misses []int
	for i := range uniq {
		if art, entry := s.kbCacheGet(srcName, uniq[i].req); art != nil {
			uniq[i].article = art
			stats.cacheHits++
			if stats.oldest == nil || entry.CachedAt.Before(stats.oldest.CachedAt) {
				stats.oldest = entry
			}
			continue
		}
		misses = append(misses, i)
	}

	// Phase 2: the network, concurrently. Each goroutine writes its own slot,
	// so no lock is needed on uniq.
	if n := len(misses); n > 0 {
		if workers < 1 {
			workers = 1
		}
		if workers > n {
			workers = n
		}
		jobs := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					art, err := src.Lookup(ctx, uniq[i].req.Title, uniq[i].req.Lang)
					if err != nil {
						uniq[i].err = err
						continue
					}
					if art == nil {
						uniq[i].err = errs.Newf(errs.CodeNotFound, "no article titled %q", uniq[i].req.Title)
						continue
					}
					uniq[i].article, uniq[i].fetched = art, true
				}
			}()
		}
		for _, i := range misses {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		// Every miss cost a request, whether or not it produced an article: a
		// 404 is still traffic, and meta.api_calls is what a caller rate-limits
		// itself by.
		stats.apiCalls = n
	}

	// Phase 3: cache writes, serially.
	for i := range uniq {
		if uniq[i].fetched {
			s.kbCachePut(srcName, uniq[i].req, uniq[i].article)
		}
	}

	out := make([]kbOutcome, len(reqs))
	for i := range reqs {
		u := uniq[order[i]]
		out[i] = kbOutcome{Req: u.req, Article: u.article, Err: u.err}
	}
	return out, stats
}

// kbCacheGet returns a cached article, or nil on a miss or a corrupt payload.
func (s *State) kbCacheGet(srcName string, req kbRequest) (*knowledge.Article, *cache.Entry) {
	if s.Cache == nil || s.NoCache || s.Refresh {
		return nil, nil
	}
	entry, err := s.Cache.Get(kbCacheKey(srcName, req))
	if err != nil || entry == nil {
		return nil, nil
	}
	var art knowledge.Article
	if err := json.Unmarshal(entry.Payload, &art); err != nil {
		// A corrupt payload is not worth failing over: treat it as a miss and
		// pay for a fresh call.
		return nil, nil
	}
	return &art, entry
}

func (s *State) kbCachePut(srcName string, req kbRequest, art *knowledge.Article) {
	if s.Cache == nil || s.NoCache || art == nil {
		return
	}
	if payload, err := json.Marshal(art); err == nil {
		_ = s.Cache.Put(kbCacheKey(srcName, req), payload, s.TTLFor(kbTTL))
	}
}

// kbCacheKey covers exactly what the source saw: which database, which language
// edition, and the canonical title. It deliberately does not include the output
// format or any filter, so changing --output re-renders a cached article
// instead of re-fetching it.
func kbCacheKey(srcName string, req kbRequest) string {
	return cache.Key("kb", []string{srcName, req.Lang, "lookup", req.Title}, "", "")
}

// kbSearch returns search hits, from cache when possible. Search results move
// more than articles do, but they share the TTL: a query that is re-run inside
// a day is a pipeline retry, not a new question.
func (s *State) kbSearch(ctx context.Context, src knowledge.Source, srcName, query, lang string, limit int) ([]knowledge.SearchHit, *cache.Entry, error) {
	if limit <= 0 {
		limit = knowledge.DefaultSearchLimit
	}
	key := cache.Key("kb", []string{srcName, lang, "search", query, strconv.Itoa(limit)}, "", "")

	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			var hits []knowledge.SearchHit
			if err := json.Unmarshal(entry.Payload, &hits); err == nil {
				return hits, entry, nil
			}
		}
	}

	hits, err := src.Search(ctx, query, lang, limit)
	if err != nil {
		return nil, nil, err
	}
	if hits == nil {
		hits = []knowledge.SearchHit{}
	}
	if s.Cache != nil && !s.NoCache {
		if payload, err := json.Marshal(hits); err == nil {
			_ = s.Cache.Put(key, payload, s.TTLFor(kbTTL))
		}
	}
	return hits, nil, nil
}

// kbMeta fills the envelope the same way `text entities` does: "cached" means
// the whole answer came from cache, because a partially served batch still cost
// a request and reporting it as cached would hide that.
func kbMeta(srcName, lang string, documents int, stats kbStats) output.Meta {
	meta := output.Meta{
		Provider:  srcName,
		Documents: documents,
		APICalls:  stats.apiCalls,
		Language:  lang,
	}
	if stats.cacheHits > 0 && stats.apiCalls == 0 && stats.oldest != nil {
		meta.Cached = true
		meta.CachedAt = stats.oldest.CachedAt.Format(time.RFC3339)
		sec := int(stats.oldest.Remaining().Seconds())
		meta.TTLRemainingSec = &sec
	}
	return meta
}

func isNotFound(err error) bool {
	var e *errs.E
	return errors.As(err, &e) && e.Code == errs.CodeNotFound
}

// articleRows is the CSV/table shape: the four fields that identify an article.
// The extract is deliberately absent — a paragraph in a CSV cell is unreadable,
// and --output json is one flag away.
func articleRows(articles []*knowledge.Article) []output.Row {
	rows := []output.Row{}
	for _, a := range articles {
		if a == nil {
			continue
		}
		rows = append(rows, output.Row{
			"title":       a.Title,
			"description": a.Description,
			"url":         a.URL,
			"lang":        a.Lang,
		})
	}
	return rows
}

// articleRecords is the NDJSON stream: one complete article per line.
func articleRecords(articles []*knowledge.Article) []any {
	records := make([]any, 0, len(articles))
	for _, a := range articles {
		records = append(records, a)
	}
	return records
}

// writeArticlesText renders the human form: title, one-line description, then
// the wrapped lead paragraph.
func writeArticlesText(w io.Writer, articles []*knowledge.Article) error {
	for i, a := range articles {
		if a == nil {
			continue
		}
		if i > 0 {
			fmt.Fprintln(w)
		}
		title := a.Title
		if a.Disambiguation {
			// Worth flagging: the extract of a disambiguation page is a list of
			// unrelated things, not a description of one.
			title += " (disambiguation page)"
		}
		fmt.Fprintln(w, title)
		if a.Description != "" {
			fmt.Fprintln(w, a.Description)
		}
		if a.Extract != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, wrapText(a.Extract, textWrapWidth))
		}
		if a.URL != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, a.URL)
		}
	}
	return nil
}

func hitRows(hits []knowledge.SearchHit) []output.Row {
	rows := []output.Row{}
	for _, h := range hits {
		rows = append(rows, output.Row{
			"title":       h.Title,
			"description": h.Description,
			"url":         h.URL,
			"score":       h.Score,
		})
	}
	return rows
}

func hitRecords(hits []knowledge.SearchHit) []any {
	records := make([]any, 0, len(hits))
	for _, h := range hits {
		records = append(records, h)
	}
	return records
}

func writeHitsText(w io.Writer, hits []knowledge.SearchHit) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, h := range hits {
		fmt.Fprintf(tw, "%s\t%s\n", h.Title, h.Description)
	}
	return tw.Flush()
}

// wrapText breaks a paragraph at word boundaries. It counts runes, not bytes,
// so a German extract full of umlauts wraps where it looks like it should.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var (
		b    strings.Builder
		line int
	)
	for i, word := range strings.Fields(s) {
		n := len([]rune(word))
		switch {
		case i == 0:
			b.WriteString(word)
			line = n
		case line+1+n > width:
			b.WriteByte('\n')
			b.WriteString(word)
			line = n
		default:
			b.WriteByte(' ')
			b.WriteString(word)
			line += 1 + n
		}
	}
	return b.String()
}

// --- `text entities --enrich` ------------------------------------------------

// entityKnowledge is the object --enrich attaches to an entity. It is a subset
// of knowledge.Article on purpose: the thumbnail and the alias list are noise
// next to an entity, while the description and the extract are the reason to
// ask at all.
type entityKnowledge struct {
	Source      string `json:"source"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Extract     string `json:"extract,omitempty"`
	URL         string `json:"url,omitempty"`
	Lang        string `json:"lang,omitempty"`
	// Disambiguation warns that the entity's URL points at a disambiguation
	// page, so the extract describes a list of things rather than this one.
	Disambiguation bool `json:"disambiguation,omitempty"`
}

// enrichedEntity is an entity plus its article. The entity is embedded, so the
// JSON shape is the documented one with a single new "knowledge" key — adding a
// field is compatible, re-nesting the entity would not be.
type enrichedEntity struct {
	entity.Entity
	Knowledge *entityKnowledge `json:"knowledge,omitempty"`
}

// enrichedDoc mirrors entityDoc with enriched entities.
type enrichedDoc struct {
	ID                string           `json:"id"`
	Provider          string           `json:"provider"`
	Language          string           `json:"language,omitempty"`
	LanguageSupported bool             `json:"language_supported"`
	Entities          []enrichedEntity `json:"entities"`
}

// emitEnrichedEntities is the --enrich tail of `text entities`.
//
// It runs after filtering and sorting, so a lookup is never paid for an entity
// that --top or --types is about to discard, and it looks up every document's
// entities in one pass, so an entity that appears in ten documents costs one
// call rather than ten.
//
// Enrichment never fails the command. An entity whose article is missing, whose
// URL is not a Wikipedia URL, or whose lookup errored simply carries no
// knowledge object: the entities themselves were already paid for, and throwing
// them away because an encyclopedia was down would be the wrong trade. Failures
// are reported on stderr only under --verbose, because a batch of entity names
// legitimately contains things no encyclopedia has.
func (s *State) emitEnrichedEntities(cmd *cobra.Command, docs []entityDoc, items []input.Item, meta output.Meta) error {
	enriched := make([]enrichedDoc, 0, len(docs))
	for _, d := range docs {
		ents := make([]enrichedEntity, 0, len(d.Entities))
		for _, e := range d.Entities {
			ents = append(ents, enrichedEntity{Entity: e})
		}
		enriched = append(enriched, enrichedDoc{
			ID:                d.ID,
			Provider:          d.Provider,
			Language:          d.Language,
			LanguageSupported: d.LanguageSupported,
			Entities:          ents,
		})
	}

	stats := s.attachKnowledge(cmd, enriched)
	// The knowledge lookups are API calls this invocation made, so they belong
	// in the count — and if any of them went to the network, the answer as a
	// whole did not come from cache.
	meta.APICalls += stats.apiCalls
	if stats.apiCalls > 0 {
		meta.Cached = false
		meta.CachedAt = ""
		meta.TTLRemainingSec = nil
	}

	var data any = map[string]any{"documents": enriched}
	if len(enriched) == 1 {
		data = enriched[0]
	}
	return emitResult(cmd, emitOpts{
		Data:    data,
		Meta:    meta,
		Columns: []string{"doc_id", "name", "type", "salience", "probability", "mentions", "wikipedia_url", "description"},
		Rows:    enrichedRows(enriched),
		Records: enrichedRecords(enriched, items),
		Text:    func(w io.Writer) error { return writeEnrichedEntitiesText(w, enriched) },
	})
}

// attachKnowledge fills in the knowledge object for every entity that carries a
// usable wikipedia_url, in place.
func (s *State) attachKnowledge(cmd *cobra.Command, docs []enrichedDoc) kbStats {
	src, err := knowledge.Open(enrichSourceName)
	if err != nil {
		s.warnEnrich(cmd, "knowledge source unavailable: "+err.Error())
		return kbStats{}
	}

	// A position per lookup, so the answers can be written back without
	// re-deriving anything.
	type target struct{ doc, ent int }
	var (
		reqs    []kbRequest
		targets []target
	)
	for di := range docs {
		for ei := range docs[di].Entities {
			// The language comes from the URL, not from --lang: the provider
			// already picked an edition for this entity, and a German document
			// yields de.wikipedia.org URLs whose titles do not exist in the
			// English edition.
			title, lang, ok := knowledge.ParseWikipediaURL(docs[di].Entities[ei].WikipediaURL)
			if !ok {
				continue
			}
			reqs = append(reqs, kbRequest{Title: title, Lang: lang})
			targets = append(targets, target{doc: di, ent: ei})
		}
	}
	if len(reqs) == 0 {
		return kbStats{}
	}

	outcomes, stats := s.kbLookupMany(cmd.Context(), src, enrichSourceName, reqs, kbEnrichWorkers)
	for i, o := range outcomes {
		if o.Err != nil || o.Article == nil {
			if o.Err != nil {
				s.warnEnrich(cmd, fmt.Sprintf("enrich %q: %v", o.Req.Title, o.Err))
			}
			continue
		}
		t := targets[i]
		a := o.Article
		docs[t.doc].Entities[t.ent].Knowledge = &entityKnowledge{
			Source:         enrichSourceName,
			Title:          a.Title,
			Description:    a.Description,
			Extract:        a.Extract,
			URL:            a.URL,
			Lang:           a.Lang,
			Disambiguation: a.Disambiguation,
		}
	}
	return stats
}

// warnEnrich reports an enrichment miss. Only under --verbose: a miss is
// expected often enough that warning by default would train users to ignore
// stderr.
func (s *State) warnEnrich(cmd *cobra.Command, msg string) {
	if !s.Verbose || s.Quiet {
		return
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+msg)
}

// enrichedRows is entityRows plus the article's one-line description — the only
// part of an article that fits in a table cell.
func enrichedRows(docs []enrichedDoc) []output.Row {
	rows := []output.Row{}
	for _, d := range docs {
		for _, e := range d.Entities {
			description := ""
			if e.Knowledge != nil {
				description = e.Knowledge.Description
			}
			rows = append(rows, output.Row{
				"doc_id":        d.ID,
				"name":          e.Name,
				"type":          e.Type,
				"salience":      e.Salience,
				"probability":   e.Probability,
				"mentions":      e.MentionCount,
				"wikipedia_url": e.WikipediaURL,
				"description":   description,
			})
		}
	}
	return rows
}

// enrichedRecords mirrors entityRecords: one entity per line, carrying the
// input's passthrough fields so a JSONL pipeline can be joined back to its
// source, with computed keys winning over passthrough ones.
func enrichedRecords(docs []enrichedDoc, items []input.Item) []any {
	fields := make(map[string]map[string]any, len(items))
	for _, it := range items {
		if len(it.Fields) > 0 {
			fields[it.ID] = it.Fields
		}
	}

	records := []any{}
	for _, d := range docs {
		for _, e := range d.Entities {
			rec := map[string]any{}
			for k, v := range fields[d.ID] {
				rec[k] = v
			}
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			var computed map[string]any
			if err := json.Unmarshal(b, &computed); err != nil {
				continue
			}
			for k, v := range computed {
				rec[k] = v
			}
			rec["doc_id"] = d.ID
			rec["provider"] = d.Provider
			if d.Language != "" {
				rec["language"] = d.Language
			}
			records = append(records, rec)
		}
	}
	return records
}

// writeEnrichedEntitiesText prints the entity line, then the description
// indented under it — the article's extract is a paragraph and belongs in JSON,
// not in a column layout.
func writeEnrichedEntitiesText(w io.Writer, docs []enrichedDoc) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	multi := len(docs) > 1
	for _, d := range docs {
		for _, e := range d.Entities {
			prefix := ""
			if multi {
				prefix = d.ID + "\t"
			}
			extra := e.WikipediaURL
			if extra == "" {
				extra = e.MID
			}
			description := ""
			if e.Knowledge != nil {
				description = e.Knowledge.Description
			}
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\n",
				prefix,
				e.Name,
				e.Type,
				strconv.FormatFloat(e.Score(), 'f', 4, 64),
				pluralMentions(e.MentionCount),
				description,
				extra,
			)
		}
	}
	return tw.Flush()
}
