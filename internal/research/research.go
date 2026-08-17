// Package research searches scientific literature.
//
// It is the citation half of this CLI. `text kb` answers "what is this thing?"
// from an encyclopedia; this package answers "what has been published about
// it?" from a paper index — and returns the abstracts, which are prose, so the
// rest of the toolchain composes directly:
//
//	text research papers "readability formulas" --output ndjson |
//	  jq -r .abstract | text readability
//
// The types are source-neutral. Firecrawl is the first backend because its
// index spans arXiv, PubMed, and DOI-addressed work behind one identifier
// scheme, but a caller parsing `text research` output must not have to change
// when a second source (OpenAlex, Semantic Scholar, a private corpus) is added.
// Anything only one source can produce is `omitempty`.
//
// Searching is the one thing every source must do. Reading passages out of a
// specific paper and finding related work are capability interfaces — the same
// design as entity.SentimentAnalyzer — so a source that only has titles and
// abstracts implements Source and nothing else, rather than stubbing out
// methods it cannot honour.
package research

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

const (
	// DefaultTimeout bounds one call.
	DefaultTimeout = 30 * time.Second
	// DefaultLimit is how many results a search returns when the caller says
	// nothing. Ten is a screenful and a reasonable prompt budget.
	DefaultLimit = 10
	// MaxLimit caps --limit. The API accepts more, but a hundred abstracts is
	// already a large payload and an accidental --limit 100000 should fail
	// locally rather than after a slow request.
	MaxLimit = 100
)

// Paper is one record from a literature index.
type Paper struct {
	// ID is the source's own stable identifier for the record.
	ID string `json:"id"`
	// PrimaryID is the preferred external identifier, in the source's
	// namespaced form ("arxiv:1706.03762", "pmid:18027780", "doi:10.1145/…").
	// It is what a human cites and what the inspect and similar lookups take.
	PrimaryID string `json:"primary_id,omitempty"`
	// IDs is every external identifier the record carries, keyed by namespace.
	// A paper commonly has both a DOI and a PubMed id, and a caller joining
	// against another database needs whichever one that database uses.
	IDs map[string][]string `json:"ids,omitempty"`

	Title    string `json:"title"`
	Abstract string `json:"abstract,omitempty"`
	// Authors is the byline as the source renders it — one string, not a
	// parsed list. Splitting it would invent a structure the source does not
	// guarantee, and every consumer that wants names can split on ", ".
	Authors    string   `json:"authors,omitempty"`
	Categories []string `json:"categories,omitempty"`
	// Published and Updated are the source's dates, verbatim. They are strings
	// rather than time.Time because the formats are not uniform across the
	// indexes behind a single source (an RFC 1123 arXiv submission date next
	// to a bare YYYY-MM-DD), and normalising them would mean discarding the
	// ones that do not parse.
	Published string `json:"published,omitempty"`
	Updated   string `json:"updated,omitempty"`
	// Score is the source's relevance number for the query that produced this
	// record. It is comparable within one result set and meaningless across
	// two: the similar-papers ranking and the search ranking are different
	// scales, which is why nothing here thresholds on it.
	Score float64 `json:"score,omitempty"`
	// URL is a canonical landing page, derived from PrimaryID when the source
	// does not supply one.
	URL string `json:"url,omitempty"`
}

// Passage is one excerpt from a paper's full text, retrieved for a question.
type Passage struct {
	Text  string  `json:"text"`
	Score float64 `json:"score,omitempty"`
}

// PaperDetail is one paper plus the passages a question retrieved from it.
type PaperDetail struct {
	Paper    Paper     `json:"paper"`
	Passages []Passage `json:"passages,omitempty"`
}

// SearchOptions is a literature query.
type SearchOptions struct {
	// Query is the natural-language search. The indexes behind these sources
	// are embedding-based, so a question reads better than a keyword list.
	Query string
	// Limit is the number of results wanted. Zero means DefaultLimit.
	Limit int
	// Authors filters by a substring of the byline.
	Authors string
	// Categories filters by the source's subject taxonomy (e.g. "cs.LG").
	Categories string
	// From and To bound the publication date, as YYYY-MM-DD.
	From string
	To   string
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// EffectiveLimit returns the result count, defaulted and capped.
func (o SearchOptions) EffectiveLimit() int {
	switch {
	case o.Limit <= 0:
		return DefaultLimit
	case o.Limit > MaxLimit:
		return MaxLimit
	default:
		return o.Limit
	}
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o SearchOptions) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// InspectOptions asks for one paper, optionally with passages answering a
// question.
type InspectOptions struct {
	// Query, when set, retrieves the passages of the paper most relevant to
	// it. Empty returns the record alone — and must: the API rejects a passage
	// count sent without a question.
	Query   string
	Limit   int
	Timeout time.Duration
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o InspectOptions) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// Relation selects what "related" means when finding neighbours of a paper.
type Relation string

const (
	// RelationSimilar is work on the same subject, by content.
	RelationSimilar Relation = "similar"
	// RelationCiters is later work citing the seed — reading forward in time.
	RelationCiters Relation = "citers"
	// RelationReferences is the seed's own bibliography — reading backward.
	RelationReferences Relation = "references"
)

// Relations returns every valid relation, in the order the help text lists them.
func Relations() []string {
	return []string{string(RelationSimilar), string(RelationCiters), string(RelationReferences)}
}

// ValidRelation reports whether a --relation value is known.
func ValidRelation(v string) bool {
	switch Relation(strings.ToLower(strings.TrimSpace(v))) {
	case RelationSimilar, RelationCiters, RelationReferences:
		return true
	}
	return false
}

// SimilarOptions asks for the neighbours of a paper.
type SimilarOptions struct {
	// Intent is a natural-language description of what makes a neighbour
	// interesting. It is required by the API and it is the point of the
	// endpoint: "papers like this one" is ambiguous until you say whether you
	// mean the method, the application, or the dataset.
	Intent   string
	Relation Relation
	Limit    int
	Timeout  time.Duration
}

// EffectiveLimit returns the result count, defaulted and capped.
func (o SimilarOptions) EffectiveLimit() int {
	switch {
	case o.Limit <= 0:
		return DefaultLimit
	case o.Limit > MaxLimit:
		return MaxLimit
	default:
		return o.Limit
	}
}

// EffectiveTimeout returns the per-call timeout, defaulted.
func (o SimilarOptions) EffectiveTimeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

// EffectiveRelation returns the relation, defaulted to RelationSimilar.
func (o SimilarOptions) EffectiveRelation() Relation {
	if o.Relation == "" {
		return RelationSimilar
	}
	return o.Relation
}

// Source is a literature index. Searching is the one thing every source must do.
//
// A source is expected to be safe for reuse across calls within a process and
// to translate its own failures into *errs.E, because the caller renders the
// code, not the prose.
type Source interface {
	// Name is the stable identifier used by --source and echoed in output.
	Name() string
	// SearchPapers returns records matching a query, best first.
	SearchPapers(ctx context.Context, opts SearchOptions) ([]Paper, error)
}

// PaperInspector is the optional capability of reading one paper by id,
// including retrieving passages from its full text.
//
// Capability, not requirement: an index built from titles and abstracts alone
// can search perfectly well and has no full text to quote. Commands ask via
// RequireInspector and get a provider_unavailable error naming the sources that
// would have worked.
type PaperInspector interface {
	InspectPaper(ctx context.Context, id string, opts InspectOptions) (*PaperDetail, error)
}

// SimilarFinder is the optional capability of finding a paper's neighbours.
// Same reasoning as PaperInspector: it needs a citation graph, which not every
// index has.
type SimilarFinder interface {
	SimilarPapers(ctx context.Context, id string, opts SimilarOptions) ([]Paper, error)
}

// RequireInspector narrows an opened source to the inspect capability.
func RequireInspector(s Source) (PaperInspector, error) {
	if i, ok := s.(PaperInspector); ok {
		return i, nil
	}
	return nil, capabilityError(s, "reading a paper by id", InspectorSources())
}

// RequireSimilarFinder narrows an opened source to the related-work capability.
func RequireSimilarFinder(s Source) (SimilarFinder, error) {
	if f, ok := s.(SimilarFinder); ok {
		return f, nil
	}
	return nil, capabilityError(s, "finding related papers", SimilarSources())
}

// InspectorSources lists the registered sources that can read a paper by id.
func InspectorSources() []string {
	return capableNames(func(s Source) bool { _, ok := s.(PaperInspector); return ok })
}

// SimilarSources lists the registered sources that can find related papers.
func SimilarSources() []string {
	return capableNames(func(s Source) bool { _, ok := s.(SimilarFinder); return ok })
}

// capableNames constructs every registered source and keeps the ones matching
// the predicate. That is only affordable because Register documents factories
// as cheap — no clients, no credentials, no network — and it is why the hint
// can name the sources that would have worked instead of saying "some other
// one".
func capableNames(supports func(Source) bool) []string {
	names := Names()
	out := make([]string, 0, len(names))
	for _, name := range names {
		s, err := Open(name)
		if err != nil || s == nil {
			continue
		}
		if supports(s) {
			out = append(out, name)
		}
	}
	return out
}

// capabilityError reports a source that exists but cannot answer this command.
//
// CodeProviderUnavailable rather than CodeInvalidArgs, for the reason
// entity.capabilityError spells out: the name can come from the config rather
// than from a flag, so blaming the arguments would point at something the user
// never typed. An unknown name stays CodeInvalidArgs.
func capabilityError(s Source, capability string, supported []string) error {
	name := "source"
	if s != nil {
		name = s.Name()
	}
	have := strings.Join(supported, ", ")
	if have == "" {
		have = "(none registered)"
	}
	return errs.Newf(errs.CodeProviderUnavailable, "research source %q does not support %s", name, capability).
		WithHint("Sources that do: " + have + ". Choose one with --source, or set it with `text config set research.source <name>`.")
}

// APIConfigurer is implemented by sources backed by a hosted API. Not part of
// Source, for the same reason it is not part of fetch.Fetcher: a source reading
// a local index has no key to set.
type APIConfigurer interface {
	SetAPIKey(key string)
	SetBaseURL(url string)
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
		panic("research: Register with empty name")
	}
	if _, dup := factories[key]; dup {
		panic("research: duplicate source " + key)
	}
	factories[key] = factory
}

// Open constructs the named source.
func Open(name string) (Source, error) {
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
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown research source: %q", name).
			WithHint("Known sources: " + known + ". Select one with --source, or set it with `text config set research.source <name>`.")
	}
	s, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "research source %q: %s", key, err.Error())
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

// Default is the source used when none is named. Same resolution as
// fetch.Default: one registered source is the default, and only an ambiguous
// build falls back to a named preference.
func Default() string {
	names := Names()
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		return names[0]
	}
	if Registered(SourceFirecrawl) {
		return SourceFirecrawl
	}
	return names[0]
}
