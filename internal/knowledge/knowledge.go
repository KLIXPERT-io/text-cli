// Package knowledge looks named things up in encyclopedia-shaped databases.
//
// It is the read side of `text entities`: the entity providers hand back a
// name, a Wikipedia URL, and a machine id, and this package turns one of those
// into prose a human or an LLM can actually use — a one-line description and a
// lead paragraph.
//
// The types here are source-neutral on purpose. Wikipedia is the first backend
// because it needs no key and covers the identifiers Cloud Natural Language
// already returns, but a caller that parses `text kb` output must not have to
// change when a second source (Wikidata, an internal company database) is
// added. Anything only one source can produce is `omitempty`, so a thinner
// backend leaves the field out rather than lying with a zero value.
//
// Adding a source is one file plus one Register call in its init — the same
// "register, don't wire" rule the entity providers follow.
package knowledge

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultTimeout bounds a single HTTP call when the caller sets none. Public
// encyclopedia APIs are fast when they are healthy and unbounded when they are
// not, so there is always a deadline.
const DefaultTimeout = 15 * time.Second

// DefaultLang is the language a lookup falls back to.
//
// "auto" is a legitimate value for the CLI's --lang, but it is not a language:
// there is no auto.wikipedia.org. Every source-facing path runs its language
// through NormalizeLang, so an unresolved "auto" becomes "en" rather than a
// DNS failure the user has to decode.
const DefaultLang = "en"

// Article is one entry in a knowledge database.
type Article struct {
	// Title is the resolved title, which may differ from the one requested:
	// a redirect ("JFK") lands on the canonical page.
	Title string `json:"title"`
	// Description is the short Wikidata one-liner ("English mathematician,
	// 1815-1852"). It is what to show when there is room for one line.
	Description string `json:"description,omitempty"`
	// Extract is the lead paragraph as plain text, no markup.
	Extract string `json:"extract,omitempty"`
	URL     string `json:"url,omitempty"`
	// Lang is the language edition the article came from, not the language of
	// the query.
	Lang         string `json:"lang,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	// Aliases are the other titles this article answers to — the requested
	// spelling when a redirect moved it, plus the source's normalized and
	// canonical forms. It is how a caller can tell that "JFK" and
	// "John F. Kennedy" are the same page.
	Aliases []string `json:"aliases,omitempty"`
	// Disambiguation reports that the title resolved to a disambiguation page
	// rather than to a thing. The extract of such a page is useless prose
	// ("X may refer to:"), so a caller should treat it as a miss and search
	// instead of quoting it.
	Disambiguation bool `json:"disambiguation,omitempty"`
}

// SearchHit is one candidate from a full-text search.
type SearchHit struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	// Score is the source's relevance number when it reports one. Wikipedia's
	// search API stopped returning it, so it is usually 0 and hits are ranked
	// by their position in the list.
	Score float64 `json:"score,omitempty"`
}

// Source is a knowledge database.
//
// Adding one is a single file plus one Register call in its init: no command
// wiring, no switch statement, no change to the output shapes. A source is
// expected to be safe for reuse across calls within a process and to translate
// its own failures into *errs.E, because the caller renders the code, not the
// prose.
type Source interface {
	// Name is the stable identifier used by --source and echoed in output.
	Name() string
	// Lookup resolves one title. A missing article is errs.CodeNotFound, not a
	// nil article: "no such page" is an answer the caller must be able to
	// branch on.
	Lookup(ctx context.Context, title, lang string) (*Article, error)
	// Search returns candidate titles for a free-text query, best first.
	Search(ctx context.Context, query, lang string, limit int) ([]SearchHit, error)
}

// TimeoutSetter is implemented by sources whose per-request deadline can be
// configured. It is deliberately not part of Source: a source backed by a local
// index has nothing to time out, and should not have to implement a no-op. The
// command type-asserts for it, the same way it type-asserts for io.Closer.
type TimeoutSetter interface {
	SetTimeout(d time.Duration)
}

var (
	mu        sync.RWMutex
	factories = map[string]func() (Source, error){}
)

// Register adds a source factory. Factories are called lazily by Open, so
// registering must stay cheap — no clients, no credentials, no network. It
// panics on a duplicate name, which can only be a programming error and only
// ever at init time.
func Register(name string, factory func() (Source, error)) {
	mu.Lock()
	defer mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		panic("knowledge: Register with empty name")
	}
	if _, dup := factories[key]; dup {
		panic("knowledge: duplicate source " + key)
	}
	factories[key] = factory
}

// Open constructs the named source.
func Open(name string) (Source, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	mu.RLock()
	factory, ok := factories[key]
	mu.RUnlock()
	if !ok {
		known := strings.Join(Names(), ", ")
		if known == "" {
			known = "(none registered)"
		}
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown knowledge source: %q", name).
			WithHint("Known sources: " + known + ". Select one with --source.")
	}
	s, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "knowledge source %q: %s", key, err.Error())
	}
	return s, nil
}

// Names returns every registered source name, sorted.
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

// Registered reports whether a source name is known.
func Registered(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// NormalizeLang turns the CLI's --lang value into a concrete language edition.
//
// "" and "auto" become DefaultLang: a knowledge lookup has no text to detect a
// language from, so there is nothing to auto-detect and "auto" must never reach
// a URL. A regional tag is cut down to its base ("de-AT" -> "de"), because
// language editions are keyed by the base code. Anything that is not plain
// letters and digits is rejected back to DefaultLang rather than interpolated
// into a hostname.
func NormalizeLang(lang string) string {
	v := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(v, "-_"); i > 0 {
		v = v[:i]
	}
	if v == "" || v == "auto" {
		return DefaultLang
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return DefaultLang
		}
	}
	return v
}

// CanonicalTitle is the form a title is cached and requested under.
//
// Wikipedia writes spaces as underscores in URLs, so "Ada_Lovelace" and
// "Ada Lovelace" are the same page. Folding to spaces here means a title
// derived from a wikipedia_url by --enrich and a title a human typed at
// `text kb lookup` produce the same cache key, which is the difference between
// enrichment being free and it paying twice for the same article. Internal
// whitespace is collapsed for the same reason.
//
// Case is deliberately NOT folded: Wikipedia titles are case-sensitive after
// the first character ("iPhone", "eBay"), and lower-casing them would break the
// lookup to save a cache miss.
func CanonicalTitle(title string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(title, "_", " ")), " ")
}

// ParseWikipediaURL pulls the article title and the language edition out of a
// Wikipedia URL. It is the join between `text entities` and this package: the
// Cloud Natural Language v1 provider returns exactly such a URL per entity, and
// its last path segment is the title to look up.
//
// The language comes from the subdomain rather than from the caller's --lang,
// because the provider already picked an edition for the entity: a German
// document yields de.wikipedia.org URLs, and looking those titles up in the
// English edition would miss.
//
// ok is false for anything that is not a wikipedia.org article URL, so a
// caller can skip it instead of guessing.
func ParseWikipediaURL(raw string) (title, lang string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	host := strings.ToLower(u.Host)
	if h, _, found := strings.Cut(host, ":"); found {
		host = h
	}
	if !strings.HasSuffix(host, "wikipedia.org") {
		return "", "", false
	}

	// Path is /wiki/<Title>; the mobile host de.m.wikipedia.org and the
	// language-less www.wikipedia.org both show up in the wild.
	seg := u.Path
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	if seg == "" {
		return "", "", false
	}
	decoded, err := url.PathUnescape(seg)
	if err != nil {
		decoded = seg
	}
	title = CanonicalTitle(decoded)
	if title == "" {
		return "", "", false
	}

	lang = DefaultLang
	if labels := strings.Split(host, "."); len(labels) > 2 {
		switch labels[0] {
		case "www", "m", "wikipedia":
		default:
			lang = NormalizeLang(labels[0])
		}
	}
	return title, lang, true
}
