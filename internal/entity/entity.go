// Package entity turns prose into named things — people, places, organizations,
// works of art — and the knowledge-base identifiers that let a caller look them
// up elsewhere.
//
// The types here are deliberately provider-neutral. Google Cloud Natural
// Language is the first backend, but Wikipedia and other knowledge databases
// are planned, and a caller that parses `text entities` output must not have to
// change when the backend does. Anything that only one provider can produce
// (per-entity sentiment, for instance) is a pointer or an omitempty field, so a
// thinner provider simply leaves it out rather than lying with a zero value.
package entity

import (
	"sort"
	"strings"
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
	// Probability is the confidence that this really is an entity of this type:
	// the maximum probability across its mentions. 0 when the provider does not
	// report one.
	//
	// Note for anyone porting from the v1 API: there is no salience in v2. The
	// old "how central is this entity to the document" number is gone, and
	// probability is not a drop-in replacement — it says nothing about
	// importance.
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
	LanguageSupported bool     `json:"language_supported"`
	Entities          []Entity `json:"entities"`
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

// FilterMinProbability keeps entities at or above min. min <= 0 is a no-op:
// dropping zero-probability entities would silently hide everything a provider
// that reports no confidence at all returns.
func FilterMinProbability(entities []Entity, min float64) []Entity {
	if min <= 0 {
		return entities
	}
	out := make([]Entity, 0, len(entities))
	for _, e := range entities {
		if e.Probability >= min {
			out = append(out, e)
		}
	}
	return out
}

// Sort orders entities by probability desc, then mention count desc, then name
// ascending. The name tiebreak is what makes the output byte-stable across runs
// — a diffable CLI is worth more than the microsecond it costs.
func Sort(entities []Entity) {
	sort.SliceStable(entities, func(i, j int) bool {
		a, b := entities[i], entities[j]
		if a.Probability != b.Probability {
			return a.Probability > b.Probability
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
	Types          []string
	MinProbability float64
	Top            int
}

// Apply filters, sorts, and truncates a copy of the result's entities.
func (r *Result) Apply(o FilterOptions) *Result {
	if r == nil {
		return nil
	}
	out := *r
	ents := append([]Entity(nil), r.Entities...)
	ents = FilterTypes(ents, o.Types)
	ents = FilterMinProbability(ents, o.MinProbability)
	Sort(ents)
	ents = Top(ents, o.Top)
	if ents == nil {
		ents = []Entity{}
	}
	out.Entities = ents
	return &out
}
