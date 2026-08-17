package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/fetch"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// fetchWorkers bounds the concurrency of a multi-URL fetch. Scrapes are the
// slowest calls this CLI makes, so serial is painful, but they are also the
// most expensive: four keeps a page of URLs quick without turning a typo'd loop
// into a bill.
const fetchWorkers = 4

func newFetchCmd() *cobra.Command {
	var (
		links   bool
		timeout time.Duration
	)

	c := &cobra.Command{
		Use:     "fetch <url...>",
		Aliases: []string{"scrape", "read"},
		Short:   "Read a web page as clean prose",
		Long: `fetch turns a URL into the text on the page: navigation, cookie banners,
and script tags removed, the body returned as markdown.

It is the input side of everything else. Every other command reads stdin, a
file, or an argument; fetch is what puts a web page into that pipeline:

  text fetch https://example.com/post --output text | text entities
  text fetch https://example.com/post --output text | text readability

The same thing is one flag away without the pipe — every analysis command
accepts --url and fetches through this exact path, cache included:

  text entities --url https://example.com/post
  text readability --url https://example.com/post --lang de
  text lint --url https://example.com/post

Pages are cached for 24h, so analysing the same URL three different ways costs
one scrape. --refresh re-reads it.

Fetching needs a Firecrawl API key:

  text config set firecrawl.api_key fc-...
  # or: export FIRECRAWL_API_KEY=fc-...`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			urls, err := s.loadURLs(args)
			if err != nil {
				return err
			}

			opts := s.fetchOptions()
			opts.IncludeLinks = links
			if timeout > 0 {
				opts.Timeout = timeout
			}

			pages, stats, err := s.fetchPages(cmd.Context(), urls, opts)
			if err != nil {
				return err
			}
			// A batch tolerates a dead link the way `kb lookup` tolerates a
			// missing article: the other pages were paid for. A single URL
			// propagates, so a scripted one-shot fetch still fails loudly.
			if len(pages) == 0 {
				return stats.firstErr
			}
			if stats.firstErr != nil && !s.Quiet {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %d of %d URLs failed: %v\n",
					len(urls)-len(pages), len(urls), stats.firstErr)
			}

			var data any = map[string]any{"pages": pages}
			if len(pages) == 1 && len(urls) == 1 {
				data = pages[0]
			}
			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    fetchMeta(s.fetcherName(), pages, stats),
				Columns: []string{"url", "title", "status_code", "chars", "language"},
				Rows:    pageRows(pages),
				Records: pageRecords(pages),
				Text:    func(w io.Writer) error { return writePagesText(w, pages) },
			})
		},
	}

	c.Flags().BoolVar(&links, "links", false, "include the page's outbound links")
	c.Flags().DurationVar(&timeout, "timeout", 0, "per-page timeout (default 60s)")
	return c
}

// loadURLs resolves the URLs to fetch: the positional arguments, the --url
// flag, or one per line on stdin.
//
// Each argument is its own URL — unlike the analysis commands, which join
// arguments into one document — for the same reason `kb lookup A B` looks up
// two titles: nothing sensible is named "https://a https://b".
func (s *State) loadURLs(args []string) ([]string, error) {
	raw := append(append([]string{}, s.URLs...), args...)

	if len(raw) == 0 {
		// Nothing named: fall back to a list on stdin, so
		// `... | text fetch` works at the end of a pipe.
		st := *s
		st.URLs = nil
		if st.InputFormat == "" || input.Format(st.InputFormat) == input.FormatText {
			st.InputFormat = string(input.FormatLines)
		}
		items, err := input.Load(input.Options{
			File:     st.File,
			Format:   input.Format(st.InputFormat),
			MaxBytes: st.MaxBytes,
		})
		if err != nil {
			if isCode(err, errs.CodeEmptyInput) {
				return nil, errs.New(errs.CodeEmptyInput, "no URL given").
					WithHint("Pass one: text fetch https://example.com.")
			}
			return nil, err
		}
		for _, it := range items {
			raw = append(raw, it.Text)
		}
	}

	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		v, err := firecrawl.ValidateURL(r)
		if err != nil {
			return nil, err
		}
		// The same URL twice in one invocation is a pipeline artefact, and
		// fetching it twice would bill twice for one answer.
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, errs.New(errs.CodeEmptyInput, "no URL given").
			WithHint("Pass one: text fetch https://example.com.")
	}
	return out, nil
}

// fetcherName resolves which backend to use: --fetcher, then the config, then
// the registry's own default.
func (s *State) fetcherName() string {
	name := strings.TrimSpace(s.Fetcher)
	if name == "" && s.Cfg != nil {
		name = strings.TrimSpace(s.Cfg.Fetch.Provider)
	}
	if name == "" {
		name = fetch.Default()
	}
	return name
}

// fetchOptions builds the per-call options from the shared flags.
func (s *State) fetchOptions() fetch.Options {
	opts := fetch.Options{
		MainContentOnly: s.MainContent,
		MaxAge:          fetch.DefaultMaxAge,
	}
	// --refresh means "do not serve me anything stale", and the backend's own
	// cache is stale in exactly the same way this CLI's is. Honouring it only
	// locally would re-request the page and get the cached copy back.
	if s.Refresh || s.NoCache {
		opts.MaxAge = 0
	}
	return opts
}

// openFetcher resolves the backend and injects the credential and endpoint.
//
// The key is set here rather than resolved inside the factory because
// fetch.Register documents factories as cheap: the registry constructs every
// backend just to answer "which of you can do this", and a factory that read a
// config file would make that expensive.
func (s *State) openFetcher() (fetch.Fetcher, error) {
	name := s.fetcherName()
	if name == "" {
		return nil, errs.New(errs.CodeProviderUnavailable, "no fetcher is registered").
			WithHint("This is a build problem, not a configuration one.")
	}
	f, err := fetch.Open(name)
	if err != nil {
		return nil, err
	}
	if api, ok := f.(fetch.APIConfigurer); ok {
		var configured, base string
		if s.Cfg != nil {
			configured = s.Cfg.Firecrawl.APIKey
			base = s.Cfg.Firecrawl.BaseURL
		}
		api.SetAPIKey(firecrawl.ResolveKey(configured))
		api.SetBaseURL(base)
	}
	return f, nil
}

// fetchStats is the cache accounting behind the meta envelope, plus the first
// failure so a partial batch can report why.
type fetchStats struct {
	apiCalls  int
	cacheHits int
	oldest    *cache.Entry
	firstErr  error
}

// fetchPages resolves many URLs: cache first, then a bounded pool of concurrent
// scrapes for the misses.
//
// The phasing mirrors kbLookupMany, for the same reasons: cache reads and
// writes happen on the calling goroutine because the store is a shared struct
// with lazily-initialised state, and only the network calls run concurrently.
// Results are re-assembled in request order, so concurrency never reaches the
// output.
func (s *State) fetchPages(ctx context.Context, urls []string, opts fetch.Options) ([]*fetch.Page, fetchStats, error) {
	var stats fetchStats
	if len(urls) == 0 {
		return nil, stats, nil
	}

	f, err := s.openFetcher()
	if err != nil {
		return nil, stats, err
	}
	name := f.Name()

	type slot struct {
		url     string
		page    *fetch.Page
		err     error
		fetched bool
	}
	slots := make([]slot, len(urls))
	for i, u := range urls {
		slots[i].url = u
	}

	// Phase 1: the cache, serially.
	var misses []int
	for i := range slots {
		if page, entry := s.fetchCacheGet(name, slots[i].url, opts); page != nil {
			slots[i].page = page
			stats.cacheHits++
			if stats.oldest == nil || entry.CachedAt.Before(stats.oldest.CachedAt) {
				stats.oldest = entry
			}
			continue
		}
		misses = append(misses, i)
	}

	// Phase 2: the network, concurrently. Each goroutine writes its own slot.
	if n := len(misses); n > 0 {
		workers := fetchWorkers
		if workers > n {
			workers = n
		}
		jobs := make(chan int)
		done := make(chan struct{})
		for w := 0; w < workers; w++ {
			go func() {
				defer func() { done <- struct{}{} }()
				for i := range jobs {
					page, err := f.Fetch(ctx, slots[i].url, opts)
					if err != nil {
						slots[i].err = err
						continue
					}
					slots[i].page, slots[i].fetched = page, true
				}
			}()
		}
		for _, i := range misses {
			jobs <- i
		}
		close(jobs)
		for w := 0; w < workers; w++ {
			<-done
		}
		// Every miss cost a request whether or not it produced a page: a
		// failed scrape is still billed traffic, and meta.api_calls is what a
		// caller rate-limits itself by.
		stats.apiCalls = n
	}

	// Phase 3: cache writes, serially.
	for i := range slots {
		if slots[i].fetched {
			s.fetchCachePut(name, slots[i].url, opts, slots[i].page)
		}
	}

	pages := make([]*fetch.Page, 0, len(slots))
	for i := range slots {
		if slots[i].err != nil {
			if stats.firstErr == nil {
				stats.firstErr = slots[i].err
			}
			continue
		}
		if slots[i].page != nil {
			pages = append(pages, slots[i].page)
		}
	}
	// A whole batch that failed for a reason no retry fixes — a missing key, a
	// bad credential — is the command's error, not a warning: reporting "0 of 3
	// pages" for an unset API key would bury the one thing to fix.
	if len(pages) == 0 && stats.firstErr != nil && isFatalFetchErr(stats.firstErr) {
		return nil, stats, stats.firstErr
	}
	return pages, stats, nil
}

// isFatalFetchErr reports whether a failure will repeat for every remaining URL.
func isFatalFetchErr(err error) bool {
	return isCode(err, errs.CodeAuthMissing) ||
		isCode(err, errs.CodeAuthDenied) ||
		isCode(err, errs.CodeAuthExpired) ||
		isCode(err, errs.CodeQuotaExceeded) ||
		isCode(err, errs.CodeRateLimited) ||
		isCode(err, errs.CodeProviderUnavailable)
}

// fetchCacheKey covers exactly what the backend saw: which fetcher, which URL,
// and the two options that change the returned text.
//
// MaxAge is deliberately absent. It bounds staleness in the *backend's* cache,
// not in this one, and including it would make `--refresh` and a normal call
// write two entries for one page. This store's own TTL is what governs here.
func fetchCacheKey(name, url string, opts fetch.Options) string {
	args := []string{name, url}
	if opts.MainContentOnly {
		args = append(args, "main")
	}
	if opts.IncludeLinks {
		args = append(args, "links")
	}
	return cache.Key("fetch", args, "", "")
}

func (s *State) fetchCacheGet(name, url string, opts fetch.Options) (*fetch.Page, *cache.Entry) {
	if s.Cache == nil || s.NoCache || s.Refresh {
		return nil, nil
	}
	entry, err := s.Cache.Get(fetchCacheKey(name, url, opts))
	if err != nil || entry == nil {
		return nil, nil
	}
	var page fetch.Page
	if err := json.Unmarshal(entry.Payload, &page); err != nil {
		return nil, nil
	}
	return &page, entry
}

func (s *State) fetchCachePut(name, url string, opts fetch.Options, page *fetch.Page) {
	if s.Cache == nil || s.NoCache || page == nil {
		return
	}
	ttl := 24 * time.Hour
	if s.Cfg != nil {
		ttl = s.Cfg.FetchTTL()
	}
	if payload, err := json.Marshal(page); err == nil {
		_ = s.Cache.Put(fetchCacheKey(name, url, opts), payload, s.TTLFor(ttl))
	}
}

func fetchMeta(name string, pages []*fetch.Page, stats fetchStats) output.Meta {
	meta := output.Meta{
		Provider:  name,
		Documents: len(pages),
		APICalls:  stats.apiCalls,
	}
	if stats.cacheHits > 0 && stats.apiCalls == 0 && stats.oldest != nil {
		meta.Cached = true
		meta.CachedAt = stats.oldest.CachedAt.Format(time.RFC3339)
		sec := int(stats.oldest.Remaining().Seconds())
		meta.TTLRemainingSec = &sec
	}
	return meta
}

// pageRows is the CSV/table shape. The content is deliberately absent — a whole
// article in a CSV cell is unreadable — but its length is there, because "did
// this page actually yield text?" is the question a table is being scanned for.
func pageRows(pages []*fetch.Page) []output.Row {
	rows := []output.Row{}
	for _, p := range pages {
		if p == nil {
			continue
		}
		rows = append(rows, output.Row{
			"url":         p.URL,
			"title":       p.Title,
			"status_code": p.StatusCode,
			"chars":       len([]rune(p.Content)),
			"language":    p.Language,
		})
	}
	return rows
}

func pageRecords(pages []*fetch.Page) []any {
	records := make([]any, 0, len(pages))
	for _, p := range pages {
		records = append(records, p)
	}
	return records
}

// writePagesText prints the page content and nothing else when there is one
// page.
//
// That is the whole point of `--output text`: it is the pipe form, and
// `text fetch URL --output text | text entities` must not feed a header line
// into the entity extractor. A multi-page fetch gets a separator, because
// otherwise two articles silently become one document.
func writePagesText(w io.Writer, pages []*fetch.Page) error {
	for i, p := range pages {
		if p == nil {
			continue
		}
		if len(pages) > 1 {
			if i > 0 {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, "# "+p.URL)
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, p.Content)
		if len(p.Links) > 0 {
			fmt.Fprintln(w)
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			for _, l := range p.Links {
				fmt.Fprintf(tw, "%s\n", l)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}
