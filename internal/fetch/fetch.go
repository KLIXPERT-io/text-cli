// Package fetch turns a URL into prose the rest of the CLI can measure.
//
// It is the missing input source. Every other command in this repo reads text
// from stdin, a file, or an argument; a web page reaches them only if a human
// copies it out of a browser first. A fetcher closes that gap: `text fetch`
// prints a clean page, and `--url` lets `entities`, `readability`, and `lint`
// read one directly.
//
// The types here are backend-neutral on purpose. Firecrawl is the first
// implementation because it renders JavaScript, follows redirects, and returns
// markdown rather than a tag soup — but a caller parsing `text fetch` output
// must not have to change when a second backend (a local headless browser, a
// plain http.Get with an HTML-to-prose pass) is added. Anything only one
// backend can produce is `omitempty`, so a thinner fetcher leaves the field out
// rather than lying with a zero value.
//
// Adding a fetcher is one file plus one Register call in its init — the same
// "register, don't wire" rule the metrics, lint rules, entity providers, and
// knowledge sources follow.
package fetch

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultTimeout bounds one fetch. A scrape renders a page, so this is
// deliberately generous compared to the other backends in this repo.
const DefaultTimeout = 60 * time.Second

// DefaultMaxAge is how stale a backend-side cached copy may be and still be
// served, in milliseconds.
//
// This is the *provider's* cache, not this CLI's: Firecrawl bills a re-scrape
// but serves its own cached page for free and roughly instantly. Two days
// matches their default and is the right trade for text analysis, where the
// question is how a page reads rather than whether it changed in the last hour.
// `--refresh` sets it to 0 and pays for a live scrape.
const DefaultMaxAge = 2 * 24 * 60 * 60 * 1000

// Page is one fetched document, reduced to what an analysis command needs.
//
// It is not a scrape API response: the screenshots, the raw HTML, and the
// change-tracking payloads a backend may support are deliberately absent,
// because this type feeds a readability score and an entity extractor. What it
// carries is the prose and enough provenance to cite it.
type Page struct {
	// URL is the final address after redirects — the page that was actually
	// read. RequestedURL is what the user asked for; they differ often enough
	// (http→https, a trailing slash, a canonical redirect) that reporting only
	// one of them loses information.
	URL          string `json:"url"`
	RequestedURL string `json:"requested_url,omitempty"`
	// Title and Description come from the page's own metadata.
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Language is the page's declared language (an HTML lang attribute or a
	// meta tag), not a detected one. It is a hint for --lang auto, not an
	// answer: a page that declares "en" and is written in German is common.
	Language string `json:"language,omitempty"`
	// Content is the page as markdown. It is the field every downstream
	// command reads, and it is markdown rather than plain text because the
	// CLI's own strip pass reduces markdown to prose better than a scraper's
	// text extraction does — headings stay headings until the last moment.
	Content string `json:"content"`
	// Links are the outbound links, when the caller asked for them.
	Links []string `json:"links,omitempty"`
	// StatusCode is the HTTP status the page itself returned. A 200 from the
	// scrape API wrapping a 404 page is a successful fetch of an error page,
	// and a caller filtering a crawl needs to see the difference.
	StatusCode int `json:"status_code,omitempty"`
	// Fetcher names the backend, echoed in output so a cached result is
	// attributable.
	Fetcher string `json:"fetcher,omitempty"`
	// Credits is what the call cost, when the backend reports it. Omitted by
	// backends that are free.
	Credits int `json:"credits,omitempty"`
}

// Options is what a fetcher is asked for. It is the whole contract: anything a
// backend needs that is not in here belongs in its own config section, not in
// this struct — otherwise every new backend widens it for everyone.
type Options struct {
	// MainContentOnly asks the backend to drop navigation, headers, and
	// footers. It defaults to true at the flag layer because a sidebar full of
	// link text ruins a readability score, but it is a request, not a promise:
	// a backend that cannot tell boilerplate from body simply returns
	// everything.
	MainContentOnly bool
	// IncludeLinks asks for the outbound link list.
	IncludeLinks bool
	// MaxAge bounds how stale a backend-side cached copy may be, in
	// milliseconds. Zero forces a live fetch.
	MaxAge int
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o Options) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// Fetcher turns a URL into a Page.
//
// A fetcher is expected to be safe for reuse across calls within a process and
// to translate its own failures into *errs.E, because the caller renders the
// code, not the prose.
type Fetcher interface {
	// Name is the stable identifier used by --fetcher and echoed in output.
	Name() string
	// Fetch reads one URL. A page that does not exist is errs.CodeNotFound.
	Fetch(ctx context.Context, url string, opts Options) (*Page, error)
}

// APIConfigurer is implemented by fetchers that talk to a hosted API and so
// need a credential and an endpoint.
//
// It is deliberately not part of Fetcher, for the same reason
// knowledge.TimeoutSetter is not part of knowledge.Source: a fetcher driving a
// local headless browser has no key and no base URL, and should not have to
// stub out two setters to say so. The command type-asserts for it, which also
// keeps Register's factories cheap — the key is injected after construction
// rather than resolved inside it.
type APIConfigurer interface {
	SetAPIKey(key string)
	SetBaseURL(url string)
}

// ServiceAccountConfigurer is implemented by fetchers authenticated with a
// Google service account key file rather than an API key.
//
// It is a second credential shape rather than a widening of APIConfigurer
// because the two have nothing in common: a service account is a file path with
// no endpoint, and a fetcher that wanted both would implement both. Same
// reasoning as APIConfigurer itself — the credential arrives after
// construction, so Register's "no credentials in a factory" rule still holds.
type ServiceAccountConfigurer interface {
	SetServiceAccount(path string)
}

// URLMatcher is the optional capability of owning a URL.
//
// It exists because a fetcher is not always a matter of preference. A Google
// Doc cannot be scraped — the generic backend gets a login page — and a public
// article does not need a Google credential. So a backend that is the only
// possible answer for a URL says so, and the command routes to it instead of
// making the user remember --fetcher.
//
// Handles must be cheap and must never claim a URL the backend cannot actually
// read: a false positive turns a working scrape into an authentication error.
// An explicit --fetcher always wins over a claim.
type URLMatcher interface {
	Handles(url string) bool
}

// CacheTTLHinter is the optional capability of overriding how long a fetched
// page may be reused.
//
// The page cache exists to avoid paying twice for one scrape. A backend that
// costs nothing and reads a document people are editing right now gets nothing
// from it and loses correctness to it, so it returns 0 here and is never
// stored. A backend that says nothing keeps the configured TTL.
type CacheTTLHinter interface {
	CacheTTL() time.Duration
}

// ForURL names the registered fetcher that claims a URL, or "" if none does.
//
// It constructs each registered backend to ask, which is affordable for exactly
// the reason Register documents: factories are cheap. The iteration order is
// the sorted name order, so two backends claiming one URL resolve the same way
// on every run rather than depending on map ordering.
func ForURL(rawurl string) string {
	for _, name := range Names() {
		f, err := Open(name)
		if err != nil {
			continue
		}
		if m, ok := f.(URLMatcher); ok && m.Handles(rawurl) {
			return name
		}
	}
	return ""
}

var (
	mu        sync.RWMutex
	factories = map[string]func() (Fetcher, error){}
)

// Register adds a fetcher factory. Factories are called lazily by Open, so
// registering must stay cheap — no clients, no credentials, no network. It
// panics on a duplicate name, which can only be a programming error and only
// ever at init time.
func Register(name string, factory func() (Fetcher, error)) {
	mu.Lock()
	defer mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		panic("fetch: Register with empty name")
	}
	if _, dup := factories[key]; dup {
		panic("fetch: duplicate fetcher " + key)
	}
	factories[key] = factory
}

// Open constructs the named fetcher.
func Open(name string) (Fetcher, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		key = Default()
	}
	mu.RLock()
	factory, ok := factories[key]
	mu.RUnlock()
	if !ok {
		known := strings.Join(Names(), ", ")
		if known == "" {
			known = "(none registered)"
		}
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown fetcher: %q", name).
			WithHint("Known fetchers: " + known + ". Select one with --fetcher, or set it with `text config set fetch.provider <name>`.")
	}
	f, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "fetcher %q: %s", key, err.Error())
	}
	return f, nil
}

// Names returns every registered fetcher name, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Registered reports whether a fetcher name is known.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// Default is the fetcher used when none is named.
//
// It resolves at call time rather than being a constant so the package has no
// opinion about which backend is compiled in: with exactly one registered, that
// one is the default, and only a genuinely ambiguous build falls back to a
// named preference.
func Default() string {
	names := Names()
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	}
	if Registered(FetcherFirecrawl) {
		return FetcherFirecrawl
	}
	return names[0]
}
