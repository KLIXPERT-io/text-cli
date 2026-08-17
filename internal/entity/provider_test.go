package entity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

type fakeProvider struct {
	name string
	res  *Result
	err  error
	// last records what the provider was asked for.
	lastText string
	lastOpts Options
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) AnalyzeEntities(_ context.Context, text string, opts Options) (*Result, error) {
	f.lastText, f.lastOpts = text, opts
	return f.res, f.err
}

func TestRegisterAndOpen(t *testing.T) {
	want := &fakeProvider{name: "fake-open"}
	Register("fake-open", func() (Provider, error) { return want, nil })

	got, err := Open("fake-open")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != Provider(want) {
		t.Fatalf("Open returned %#v, want the registered provider", got)
	}
	if got.Name() != "fake-open" {
		t.Fatalf("Name = %q", got.Name())
	}

	// Names are case-insensitive on lookup.
	if _, err := Open("  FAKE-OPEN "); err != nil {
		t.Fatalf("Open with mixed case/whitespace: %v", err)
	}
	if !Registered("fake-open") {
		t.Fatal("Registered = false for a registered provider")
	}
}

func TestOpenUnknownProvider(t *testing.T) {
	_, err := Open("wikipedia-not-yet")
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeInvalidArgs {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeInvalidArgs)
	}
	if !strings.Contains(e.Hint, ProviderGoogle) {
		t.Fatalf("hint %q does not list the known providers", e.Hint)
	}
}

func TestOpenFactoryErrorBecomesProviderUnavailable(t *testing.T) {
	Register("fake-broken", func() (Provider, error) { return nil, errors.New("boom") })
	_, err := Open("fake-broken")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeProviderUnavailable)
	}
}

func TestOpenFactoryStructuredErrorPassesThrough(t *testing.T) {
	Register("fake-noauth", func() (Provider, error) {
		return nil, errs.New(errs.CodeAuthMissing, "nope")
	})
	_, err := Open("fake-noauth")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeAuthMissing {
		t.Fatalf("error = %v, want auth_missing passed through", err)
	}
}

func TestNamesIncludesGoogle(t *testing.T) {
	found := false
	for _, n := range Names() {
		if n == ProviderGoogle {
			found = true
		}
	}
	if !found {
		t.Fatalf("Names() = %v, want it to include %q", Names(), ProviderGoogle)
	}
}

func TestEffectiveTimeout(t *testing.T) {
	if got := (Options{}).EffectiveTimeout(); got != DefaultTimeout {
		t.Fatalf("zero timeout = %v, want %v", got, DefaultTimeout)
	}
	if got := (Options{Timeout: time.Second}).EffectiveTimeout(); got != time.Second {
		t.Fatalf("timeout = %v, want 1s", got)
	}
}

func sample() []Entity {
	return []Entity{
		{Name: "Zurich", Type: "LOCATION", Probability: 0.5, MentionCount: 1},
		{Name: "Ada Lovelace", Type: "PERSON", Probability: 0.99, MentionCount: 2},
		{Name: "Analytical Engine", Type: "WORK_OF_ART", Probability: 0.5, MentionCount: 3},
		{Name: "Amber", Type: "OTHER", Probability: 0.5, MentionCount: 1},
		{Name: "1843", Type: "DATE", Probability: 0.1, MentionCount: 1},
	}
}

func names(ents []Entity) []string {
	out := make([]string, len(ents))
	for i, e := range ents {
		out[i] = e.Name
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSortIsProbabilityThenMentionsThenName(t *testing.T) {
	ents := sample()
	Sort(ents)
	want := []string{
		"Ada Lovelace",      // 0.99
		"Analytical Engine", // 0.5, 3 mentions
		"Amber",             // 0.5, 1 mention, "Amber" < "Zurich"
		"Zurich",            // 0.5, 1 mention
		"1843",              // 0.1
	}
	if got := names(ents); !eqStrings(got, want) {
		t.Fatalf("Sort = %v, want %v", got, want)
	}
}

func TestFilterTypesIsCaseInsensitive(t *testing.T) {
	got := FilterTypes(sample(), ParseTypes(" person , location "))
	if want := []string{"Zurich", "Ada Lovelace"}; !eqStrings(names(got), want) {
		t.Fatalf("FilterTypes = %v, want %v", names(got), want)
	}
	if got := FilterTypes(sample(), nil); len(got) != 5 {
		t.Fatalf("nil filter dropped entities: %v", names(got))
	}
	if got := FilterTypes(sample(), ParseTypes("")); len(got) != 5 {
		t.Fatalf("empty filter dropped entities: %v", names(got))
	}
}

func TestParseTypes(t *testing.T) {
	if got := ParseTypes("person,,  Work_Of_Art "); !eqStrings(got, []string{"PERSON", "WORK_OF_ART"}) {
		t.Fatalf("ParseTypes = %v", got)
	}
	if got := ParseTypes("  ,, "); got != nil {
		t.Fatalf("ParseTypes(blank) = %v, want nil", got)
	}
}

func TestFilterMinProbability(t *testing.T) {
	got := FilterMinProbability(sample(), 0.5)
	if len(got) != 4 {
		t.Fatalf("min 0.5 kept %v", names(got))
	}
	if got := FilterMinProbability(sample(), 0); len(got) != 5 {
		t.Fatalf("min 0 must be a no-op, kept %v", names(got))
	}
}

func TestTop(t *testing.T) {
	ents := sample()
	Sort(ents)
	if got := Top(ents, 2); !eqStrings(names(got), []string{"Ada Lovelace", "Analytical Engine"}) {
		t.Fatalf("Top(2) = %v", names(got))
	}
	if got := Top(ents, 0); len(got) != 5 {
		t.Fatalf("Top(0) must return everything, got %d", len(got))
	}
	if got := Top(ents, 99); len(got) != 5 {
		t.Fatalf("Top(99) = %d, want 5", len(got))
	}
}

func TestResultApply(t *testing.T) {
	r := &Result{Provider: "fake", Language: "en", LanguageSupported: true, Entities: sample()}
	got := r.Apply(FilterOptions{Types: []string{"PERSON", "WORK_OF_ART", "DATE"}, MinProbability: 0.2, Top: 1})
	if len(got.Entities) != 1 || got.Entities[0].Name != "Ada Lovelace" {
		t.Fatalf("Apply = %v", names(got.Entities))
	}
	if got.Provider != "fake" || got.Language != "en" || !got.LanguageSupported {
		t.Fatalf("Apply lost result metadata: %+v", got)
	}
	// The original must be untouched: the cached payload is reused across
	// documents in a batch.
	if len(r.Entities) != 5 {
		t.Fatalf("Apply mutated the source result: %v", names(r.Entities))
	}
}

func TestResultApplyEmptyStaysNonNil(t *testing.T) {
	r := &Result{Provider: "fake", Entities: sample()}
	got := r.Apply(FilterOptions{MinProbability: 1.5})
	if got.Entities == nil {
		t.Fatal("Apply returned nil entities; JSON must render [] not null")
	}
	if len(got.Entities) != 0 {
		t.Fatalf("expected no entities, got %v", names(got.Entities))
	}
}

func TestLiftMetadata(t *testing.T) {
	e := Entity{Metadata: map[string]string{
		MetaWikipediaURL: "https://en.wikipedia.org/wiki/Ada_Lovelace",
		MetaMID:          "/m/0ff4d",
		"other":          "kept",
	}}
	e.LiftMetadata()
	if e.WikipediaURL != "https://en.wikipedia.org/wiki/Ada_Lovelace" || e.MID != "/m/0ff4d" {
		t.Fatalf("LiftMetadata = %+v", e)
	}
	if e.Metadata["other"] != "kept" {
		t.Fatal("LiftMetadata dropped passthrough metadata")
	}
	var empty Entity
	empty.LiftMetadata() // must not panic on a nil map
}
