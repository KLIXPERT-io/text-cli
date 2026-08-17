// Package entity wraps semantic analysis of prose: named things — people,
// places, organizations, works of art — plus the knowledge-base identifiers
// that let a caller look them up elsewhere, and, through capability
// interfaces, document sentiment and content classification.
//
// The name is historical: entities came first. A backend implements the
// Provider interface to supply entities, and optionally SentimentAnalyzer or
// TextClassifier on top. Those are deliberately separate interfaces rather
// than methods on Provider, so a knowledge-only backend such as the planned
// Wikipedia one does not have to stub out calls it can never serve.
//
// The types here are deliberately provider-neutral. Google Cloud Natural
// Language is the first backend, but a caller that parses `text entities`
// output must not have to change when the backend does. Anything that only one
// provider can produce (per-entity sentiment, for instance) is a pointer or an
// omitempty field, so a thinner provider simply leaves it out rather than lying
// with a zero value.
package entity

import (
	"math"
	"sort"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// Metadata keys that every provider is expected to use for the two identifiers
// the planned knowledge-database features key off. Google returns exactly these
// names; a future provider should populate them too rather than inventing a
// synonym.
const (
	MetaWikipediaURL = "wikipedia_url"
	MetaMID          = "mid"
)

// Entity is one named thing found in a document.
type Entity struct {
	Name string `json:"name"`
	// Type is the provider's category, upper-cased: PERSON, LOCATION,
	// ORGANIZATION, EVENT, WORK_OF_ART, CONSUMER_GOOD, NUMBER, DATE, ADDRESS,
	// PHONE_NUMBER, PRICE, OTHER, UNKNOWN.
	Type string `json:"type"`
	// Salience is how central this entity is to its document, in [0, 1]. Within
	// one document the salience of all entities sums to roughly 1.0, so it is a
	// share of attention, not a confidence: 0.4 means "this document is largely
	// about this", never "we are 40% sure this is a person".
	//
	// The Google backend reports it (Cloud Natural Language v1). A provider that
	// has no such notion leaves it at 0.
	Salience float64 `json:"salience"`
	// Probability is the confidence that this really is an entity of this type:
	// the maximum probability across its mentions. 0 when the provider does not
	// report one.
	//
	// The Google v1 backend does NOT report it — v1 has no per-mention
	// probability, only salience — so it is 0 for every Google entity today. It
	// stays in the contract because it is the natural confidence field for other
	// backends, and because dropping a documented JSON key is a breaking change.
	// Ranking and filtering go through Score, which prefers salience and falls
	// back to probability, so both kinds of provider behave sensibly.
	Probability  float64   `json:"probability"`
	Mentions     []Mention `json:"mentions,omitempty"`
	MentionCount int       `json:"mention_count"`
	// Metadata is passed through from the provider unchanged.
	Metadata map[string]string `json:"metadata,omitempty"`
	// WikipediaURL and MID are lifted out of Metadata for convenience — the
	// knowledge-database features planned next key off exactly these.
	WikipediaURL string `json:"wikipedia_url,omitempty"`
	MID          string `json:"mid,omitempty"`
	// Sentiment is nil unless the provider returned one.
	Sentiment *Sentiment `json:"sentiment,omitempty"`
}

// Mention is one occurrence of an entity in the source text.
type Mention struct {
	Text string `json:"text"`
	// Type is PROPER or COMMON.
	Type string `json:"type"`
	// BeginOffset is the offset into the source text, in the units the provider
	// was asked for (UTF-8 bytes for the Google provider).
	BeginOffset int     `json:"begin_offset"`
	Probability float64 `json:"probability,omitempty"`
}

// Sentiment is the polarity a provider attached to an entity.
type Sentiment struct {
	Score     float64 `json:"score"`
	Magnitude float64 `json:"magnitude"`
}

// Result is one provider's answer for one document.
type Result struct {
	Provider string `json:"provider"`
	// Language is the document language the provider used — the one that was
	// requested, or the one it detected when none was.
	Language string `json:"language,omitempty"`
	// LanguageSupported reports whether the provider officially supports that
	// language. A false here with entities present means best-effort output.
	//
	// Not every backend has such a signal: Cloud Natural Language v1 rejects an
	// unsupported language with InvalidArgument instead of answering with a flag,
	// so a successful v1 response sets this true. The key stays in the JSON
	// either way — a consumer must not have to branch on its presence.
	LanguageSupported bool     `json:"language_supported"`
	Entities          []Entity `json:"entities"`
}

// Score is the number the CLI ranks and filters entities by.
//
// Salience wins when the provider reports one, because "how central is this to
// the document" is the more useful ordering; probability is the fallback for a
// backend that reports confidence instead. Exactly one of the two is populated
// per provider today, so this never mixes the two scales within one result —
// but a caller comparing scores across providers is comparing different things,
// and should read the per-entity fields instead.
func (e Entity) Score() float64 {
	if e.Salience > 0 {
		return e.Salience
	}
	return e.Probability
}

// Round4 rounds a score to four decimals. Salience arrives as a float32 whose
// float64 widening is full of noise (0.20000000298023224); four decimals is
// more precision than the model's ranking carries and keeps output diffable.
func Round4(v float64) float64 {
	return math.Round(v*1e4) / 1e4
}

// LiftMetadata copies the well-known knowledge-base identifiers out of Metadata
// into their own fields. Providers call this after filling Metadata so the
// promotion rule lives in exactly one place.
func (e *Entity) LiftMetadata() {
	if e.Metadata == nil {
		return
	}
	if v := e.Metadata[MetaWikipediaURL]; v != "" {
		e.WikipediaURL = v
	}
	if v := e.Metadata[MetaMID]; v != "" {
		e.MID = v
	}
}

// ParseTypes splits a comma-separated --types value into a normalized set.
// Values are upper-cased and trimmed, so `person,Organization` and
// `PERSON, ORGANIZATION` mean the same thing. An empty or blank input yields
// nil, meaning "no filter".
func ParseTypes(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		p := strings.ToUpper(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// FilterTypes keeps only entities whose type is in the given set. A nil or
// empty set is a no-op, so callers can pass an unset flag straight through.
func FilterTypes(entities []Entity, types []string) []Entity {
	if len(types) == 0 {
		return entities
	}
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[strings.ToUpper(strings.TrimSpace(t))] = true
	}
	out := make([]Entity, 0, len(entities))
	for _, e := range entities {
		if want[strings.ToUpper(e.Type)] {
			out = append(out, e)
		}
	}
	return out
}

// FilterMinScore keeps entities whose Score is at or above min. min <= 0 is a
// no-op: dropping zero-score entities would silently hide everything a provider
// that reports neither salience nor confidence returns.
//
// Note the scale. Salience is a share of one document's attention and sums to
// about 1.0 across its entities, so useful thresholds are small (0.01–0.1). A
// 0.8 that made sense as a confidence cut-off will return almost nothing.
func FilterMinScore(entities []Entity, min float64) []Entity {
	if min <= 0 {
		return entities
	}
	out := make([]Entity, 0, len(entities))
	for _, e := range entities {
		if e.Score() >= min {
			out = append(out, e)
		}
	}
	return out
}

// SortBy names an ordering for entity output.
type SortBy string

const (
	// SortSalience orders by score (salience, else probability) desc. It is the
	// default: with the Google v1 backend it answers "what is this text about",
	// which is the question `text entities` is usually asked.
	SortSalience SortBy = "salience"
	// SortMentions orders by how often the entity was mentioned.
	SortMentions SortBy = "mentions"
	// SortName orders alphabetically, for diffing two runs by hand.
	SortName SortBy = "name"
)

// ParseSortBy validates a --sort value. An empty string means the default.
func ParseSortBy(s string) (SortBy, error) {
	switch SortBy(strings.ToLower(strings.TrimSpace(s))) {
	case "", SortSalience:
		return SortSalience, nil
	case SortMentions:
		return SortMentions, nil
	case SortName:
		return SortName, nil
	}
	return "", errs.Newf(errs.CodeInvalidArgs, "unknown --sort value: %q", s).
		WithHint("Use --sort salience, mentions, or name.")
}

// Sort orders entities in the default order: score desc, then mention count
// desc, then name ascending. The name tiebreak is what makes the output
// byte-stable across runs — a diffable CLI is worth more than the microsecond
// it costs.
func Sort(entities []Entity) { SortEntities(entities, SortSalience) }

// SortEntities orders entities by the requested key. Every ordering ends in the
// same tiebreak chain, so two runs over the same data always agree.
func SortEntities(entities []Entity, by SortBy) {
	sort.SliceStable(entities, func(i, j int) bool {
		a, b := entities[i], entities[j]
		switch by {
		case SortName:
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Type < b.Type
		case SortMentions:
			if a.MentionCount != b.MentionCount {
				return a.MentionCount > b.MentionCount
			}
		}
		if as, bs := a.Score(), b.Score(); as != bs {
			return as > bs
		}
		if a.MentionCount != b.MentionCount {
			return a.MentionCount > b.MentionCount
		}
		return a.Name < b.Name
	})
}

// Top returns at most n entities. n <= 0 means all of them.
func Top(entities []Entity, n int) []Entity {
	if n <= 0 || n >= len(entities) {
		return entities
	}
	return entities[:n]
}

// FilterOptions is the post-processing a command applies to a provider result.
// It is deliberately separate from Options (what the provider is asked for):
// filtering happens after the cache, so changing --top never costs an API call.
type FilterOptions struct {
	Types    []string
	MinScore float64
	Top      int
	// Sort is the ordering key; the zero value means SortSalience.
	Sort SortBy
}

// Apply filters, sorts, and truncates a copy of the result's entities.
func (r *Result) Apply(o FilterOptions) *Result {
	if r == nil {
		return nil
	}
	by := o.Sort
	if by == "" {
		by = SortSalience
	}
	out := *r
	ents := append([]Entity(nil), r.Entities...)
	ents = FilterTypes(ents, o.Types)
	ents = FilterMinScore(ents, o.MinScore)
	SortEntities(ents, by)
	ents = Top(ents, o.Top)
	if ents == nil {
		ents = []Entity{}
	}
	out.Entities = ents
	return &out
}

// AggregatedEntity is one entity merged across every document of a run: the
// corpus-level view `--aggregate` produces.
type AggregatedEntity struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// CombinedSalience is the sum of the entity's per-document salience.
	//
	// Read it as a count, not a probability. Salience is per document and sums
	// to about 1.0 within one document, so a corpus of N documents distributes
	// about N salience in total: 1.8 combined over 12 documents means this
	// entity owns nearly two documents' worth of attention. It is therefore
	// bounded by the number of documents it appeared in, not by 1.0, and it
	// deliberately rewards an entity that is central to many documents over one
	// that is central to a single document.
	CombinedSalience float64 `json:"combined_salience"`
	// AvgSalience is CombinedSalience divided by Documents — how central the
	// entity is to the documents that mention it at all, ignoring reach.
	AvgSalience float64 `json:"avg_salience"`
	// Mentions is the total mention count across all documents.
	Mentions int `json:"mentions"`
	// Documents is how many documents mentioned the entity.
	Documents    int    `json:"documents"`
	WikipediaURL string `json:"wikipedia_url,omitempty"`
	MID          string `json:"mid,omitempty"`
}

// mergeKey is the identity two entities must share to be merged across
// documents: the case-folded, whitespace-collapsed name plus the upper-cased
// type.
//
// Type belongs in the key. "Apple" the ORGANIZATION and "apple" the
// CONSUMER_GOOD are different things, and adding their salience together would
// invent an importance neither of them has — while name alone would also merge
// "Washington" the person with "Washington" the location. Case and internal
// whitespace are folded instead, because those vary with the surface form the
// provider happened to find ("Ada Lovelace" in one document, "ADA LOVELACE" in
// the next) and always mean the same thing.
func mergeKey(name, typ string) string {
	folded := strings.Join(strings.Fields(strings.ToLower(name)), " ")
	return folded + "\x00" + strings.ToUpper(strings.TrimSpace(typ))
}

// Aggregate merges the entities of many documents into one corpus-level list,
// sorted by combined salience desc, then mentions desc, then name asc.
//
// It is pure: no network, no provider, no command state — just the per-document
// results in input order. Nil results are skipped, so a caller does not have to
// pre-clean the slice.
//
// The reported Name is the first surface form seen, and the knowledge-base
// identifiers are the first non-empty ones seen, so the output is a function of
// input order alone and never of map iteration.
func Aggregate(results []*Result) []AggregatedEntity {
	type acc struct {
		agg  AggregatedEntity
		seen int // index of the last result counted, +1; guards double counting
	}
	byKey := make(map[string]*acc)
	order := make([]string, 0, 16)

	for i, r := range results {
		if r == nil {
			continue
		}
		for _, e := range r.Entities {
			key := mergeKey(e.Name, e.Type)
			a := byKey[key]
			if a == nil {
				a = &acc{agg: AggregatedEntity{Name: e.Name, Type: strings.ToUpper(strings.TrimSpace(e.Type))}}
				byKey[key] = a
				order = append(order, key)
			}
			a.agg.CombinedSalience += e.Salience
			// An entity that appears at all was mentioned at least once, even if
			// the provider reported no mention list. That keeps the invariant
			// mentions >= documents true for every backend.
			n := e.MentionCount
			if n < len(e.Mentions) {
				n = len(e.Mentions)
			}
			if n < 1 {
				n = 1
			}
			a.agg.Mentions += n
			// Documents counts documents, not occurrences: an entity listed
			// twice within one result still only makes that document count once.
			if a.seen != i+1 {
				a.agg.Documents++
				a.seen = i + 1
			}
			if a.agg.WikipediaURL == "" {
				a.agg.WikipediaURL = e.WikipediaURL
			}
			if a.agg.MID == "" {
				a.agg.MID = e.MID
			}
		}
	}

	out := make([]AggregatedEntity, 0, len(order))
	for _, key := range order {
		a := byKey[key].agg
		if a.Documents > 0 {
			a.AvgSalience = Round4(a.CombinedSalience / float64(a.Documents))
		}
		a.CombinedSalience = Round4(a.CombinedSalience)
		out = append(out, a)
	}
	SortAggregated(out, SortSalience)
	return out
}

// SortAggregated orders merged entities by the requested key, ending in the
// same tiebreak chain so the output is stable across runs.
func SortAggregated(aggs []AggregatedEntity, by SortBy) {
	sort.SliceStable(aggs, func(i, j int) bool {
		a, b := aggs[i], aggs[j]
		switch by {
		case SortName:
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.Type < b.Type
		case SortMentions:
			if a.Mentions != b.Mentions {
				return a.Mentions > b.Mentions
			}
		}
		if a.CombinedSalience != b.CombinedSalience {
			return a.CombinedSalience > b.CombinedSalience
		}
		if a.Mentions != b.Mentions {
			return a.Mentions > b.Mentions
		}
		return a.Name < b.Name
	})
}

// TopAggregated returns at most n merged entities. n <= 0 means all of them.
func TopAggregated(aggs []AggregatedEntity, n int) []AggregatedEntity {
	if n <= 0 || n >= len(aggs) {
		return aggs
	}
	return aggs[:n]
}
