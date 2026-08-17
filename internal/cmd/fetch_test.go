package cmd

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/fetch"
)

func TestCapText(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		max       int64
		want      string
		truncated bool
	}{
		{name: "under the cap is untouched", in: "hello", max: 100, want: "hello"},
		{name: "exactly at the cap is not truncation", in: "hello", max: 5, want: "hello"},
		{name: "over the cap is cut", in: "hello world", max: 5, want: "hello", truncated: true},
		{name: "a zero cap means no limit", in: "hello", max: 0, want: "hello"},
		{name: "a negative cap means no limit", in: "hello", max: -1, want: "hello"},
		{
			// Cutting mid-sequence would hand the tokenizer an invalid byte and
			// silently skew the word count. "ä" is two bytes, so a cap of 2
			// lands inside it.
			name:      "a cut inside a multi-byte rune backs off to the boundary",
			in:        "aäb",
			max:       2,
			want:      "a",
			truncated: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := capText(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
			if truncated != tc.truncated {
				t.Errorf("truncated = %v, want %v", truncated, tc.truncated)
			}
			if !isValidUTF8(got) {
				t.Errorf("text = %q, want valid UTF-8", got)
			}
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestFetchCacheKeyCoversWhatChangesTheText(t *testing.T) {
	base := fetch.Options{MainContentOnly: true}
	tests := []struct {
		name string
		a, b fetch.Options
		urlA string
		urlB string
		same bool
	}{
		{
			name: "the same request is the same key",
			a:    base, b: base,
			urlA: "https://a", urlB: "https://a", same: true,
		},
		{
			name: "a different URL is a different key",
			a:    base, b: base,
			urlA: "https://a", urlB: "https://b", same: false,
		},
		{
			name: "main-content changes the returned text",
			a:    fetch.Options{MainContentOnly: true}, b: fetch.Options{},
			urlA: "https://a", urlB: "https://a", same: false,
		},
		{
			name: "asking for links changes the payload",
			a:    base, b: fetch.Options{MainContentOnly: true, IncludeLinks: true},
			urlA: "https://a", urlB: "https://a", same: false,
		},
		{
			// MaxAge bounds staleness in the backend's cache, not in this one.
			// Including it would make --refresh and a normal call write two
			// entries for one page.
			name: "the backend's staleness bound does not split the key",
			a:    fetch.Options{MainContentOnly: true, MaxAge: 0},
			b:    fetch.Options{MainContentOnly: true, MaxAge: fetch.DefaultMaxAge},
			urlA: "https://a", urlB: "https://a", same: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ka := fetchCacheKey("firecrawl", tc.urlA, tc.a)
			kb := fetchCacheKey("firecrawl", tc.urlB, tc.b)
			if (ka == kb) != tc.same {
				t.Fatalf("keys equal = %v, want %v", ka == kb, tc.same)
			}
		})
	}
}

func TestFetchCacheKeyIsPerFetcher(t *testing.T) {
	o := fetch.Options{MainContentOnly: true}
	if fetchCacheKey("firecrawl", "https://a", o) == fetchCacheKey("other", "https://a", o) {
		t.Fatal("two fetchers share a cache key; one backend's page could be served as another's")
	}
}

func TestFetchOptionsRefreshBypassesTheBackendCache(t *testing.T) {
	tests := []struct {
		name       string
		state      State
		wantMaxAge int
	}{
		{name: "a normal call accepts a cached page", state: State{}, wantMaxAge: fetch.DefaultMaxAge},
		// Honouring --refresh only locally would re-request the page and get
		// the backend's cached copy straight back.
		{name: "--refresh forces a live scrape", state: State{Refresh: true}, wantMaxAge: 0},
		{name: "--no-cache forces a live scrape", state: State{NoCache: true}, wantMaxAge: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.fetchOptions().MaxAge; got != tc.wantMaxAge {
				t.Fatalf("MaxAge = %d, want %d", got, tc.wantMaxAge)
			}
		})
	}
}

func TestFetcherNameResolutionOrder(t *testing.T) {
	tests := []struct {
		name  string
		state State
		want  string
	}{
		{
			name:  "the flag wins",
			state: State{Fetcher: "firecrawl", Cfg: &config.Config{Fetch: config.Fetch{Provider: "other"}}},
			want:  "firecrawl",
		},
		{
			name:  "then the config",
			state: State{Cfg: &config.Config{Fetch: config.Fetch{Provider: "firecrawl"}}},
			want:  "firecrawl",
		},
		{
			name:  "then the registry default",
			state: State{Cfg: config.Default()},
			want:  fetch.Default(),
		},
		{
			name:  "a nil config still resolves",
			state: State{},
			want:  fetch.Default(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.fetcherName(); got != tc.want {
				t.Fatalf("fetcherName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoadURLsNormalizesAndDeduplicates(t *testing.T) {
	tests := []struct {
		name    string
		flag    []string
		args    []string
		want    []string
		wantErr bool
	}{
		{
			name: "arguments are each their own URL",
			args: []string{"https://a.com", "https://b.com"},
			want: []string{"https://a.com", "https://b.com"},
		},
		{
			name: "the --url flag and arguments combine",
			flag: []string{"https://a.com"}, args: []string{"https://b.com"},
			want: []string{"https://a.com", "https://b.com"},
		},
		{
			// A pipeline repeats URLs; fetching one twice would bill twice for
			// one answer.
			name: "a repeat is fetched once",
			args: []string{"https://a.com", "https://a.com"},
			want: []string{"https://a.com"},
		},
		{
			name: "a bare host gains a scheme",
			args: []string{"example.com/post"},
			want: []string{"https://example.com/post"},
		},
		{
			name: "an unfetchable scheme fails loudly",
			args: []string{"file:///etc/passwd"}, wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &State{URLs: tc.flag}
			got, err := s.loadURLs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("loadURLs = %v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadURLs errored: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("loadURLs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsFatalFetchErrSeparatesRepeatingFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// These repeat for every remaining URL, so a whole batch failing on one
		// is the command's error rather than a per-URL warning.
		{name: "a missing key repeats", err: errs.New(errs.CodeAuthMissing, "no key"), want: true},
		{name: "a bad key repeats", err: errs.New(errs.CodeAuthDenied, "bad key"), want: true},
		{name: "a rate limit repeats", err: errs.New(errs.CodeRateLimited, "slow down"), want: true},
		{name: "exhausted credits repeat", err: errs.New(errs.CodeQuotaExceeded, "broke"), want: true},
		// These are about one page and must not abort the batch.
		{name: "a dead link does not repeat", err: errs.New(errs.CodeNotFound, "404"), want: false},
		{name: "an empty page does not repeat", err: errs.New(errs.CodeEmptyInput, "no text"), want: false},
		{name: "a bad URL does not repeat", err: errs.New(errs.CodeInvalidArgs, "bad url"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFatalFetchErr(tc.err); got != tc.want {
				t.Fatalf("isFatalFetchErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFetcherForRoutesPerURL(t *testing.T) {
	const docURL = "https://docs.google.com/document/d/1BxiMVs0XRA5nFMdKvBd/edit"
	const pageURL = "https://example.com/post"

	tests := []struct {
		name  string
		state State
		url   string
		want  string
	}{
		{
			// A Google Doc cannot be scraped at all, so the claim wins over the
			// configured preference — routing it to the scraper would score a
			// login page.
			name:  "a document routes to the backend that claims it",
			state: State{Cfg: config.Default()},
			url:   docURL,
			want:  fetch.FetcherGoogleDocs,
		},
		{
			name:  "a claim beats the configured default",
			state: State{Cfg: &config.Config{Fetch: config.Fetch{Provider: fetch.FetcherFirecrawl}}},
			url:   docURL,
			want:  fetch.FetcherGoogleDocs,
		},
		{
			// An explicit --fetcher is the user naming a backend for this run.
			name:  "an explicit --fetcher still wins",
			state: State{Fetcher: fetch.FetcherFirecrawl, Cfg: config.Default()},
			url:   docURL,
			want:  fetch.FetcherFirecrawl,
		},
		{
			name:  "an unclaimed URL falls through to the default",
			state: State{Cfg: config.Default()},
			url:   pageURL,
			want:  fetch.Default(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.fetcherFor(tc.url); got != tc.want {
				t.Fatalf("fetcherFor(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestFetchTTLHonorsTheBackendsPolicy(t *testing.T) {
	s := &State{Cfg: config.Default()}

	scraper, err := fetch.Open(fetch.FetcherFirecrawl)
	if err != nil {
		t.Fatalf("open firecrawl: %v", err)
	}
	if got := s.fetchTTL(scraper); got <= 0 {
		t.Fatalf("fetchTTL(firecrawl) = %v, want the configured page TTL — a billed scrape must be cached", got)
	}

	docs, err := fetch.Open(fetch.FetcherGoogleDocs)
	if err != nil {
		t.Fatalf("open gdocs: %v", err)
	}
	if got := s.fetchTTL(docs); got != 0 {
		t.Fatalf("fetchTTL(gdocs) = %v, want 0 — a document being edited must not be served from a store", got)
	}
}
