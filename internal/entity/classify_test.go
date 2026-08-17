package entity

import (
	"context"
	"strings"
	"testing"

	"cloud.google.com/go/language/apiv1/languagepb"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

func TestConvertClassificationResponse(t *testing.T) {
	resp := &languagepb.ClassifyTextResponse{
		Categories: []*languagepb.ClassificationCategory{
			{Name: "/Computers & Electronics/Software", Confidence: 0.62000002},
			nil, // must not panic or produce a phantom category
			{Name: "/Science/Computer Science", Confidence: 0.91},
			{Name: "/Arts & Entertainment", Confidence: 0.91},
		},
	}

	got := convertClassificationResponse(resp, "en")
	if got.Provider != ProviderGoogle {
		t.Fatalf("provider = %q", got.Provider)
	}
	if got.Language != "en" {
		t.Fatalf("language = %q, want the requested language echoed back", got.Language)
	}
	if len(got.Categories) != 3 {
		t.Fatalf("categories = %#v, want 3", got.Categories)
	}
	// Confidence desc, then name asc for the tie — stable across runs.
	want := []string{"/Arts & Entertainment", "/Science/Computer Science", "/Computers & Electronics/Software"}
	for i, w := range want {
		if got.Categories[i].Name != w {
			t.Fatalf("category %d = %q, want %q (order: confidence desc, name asc)", i, got.Categories[i].Name, w)
		}
	}
	// float32 noise rounded to four decimals.
	if got.Categories[2].Confidence != 0.62 {
		t.Fatalf("confidence = %v, want 0.62", got.Categories[2].Confidence)
	}
}

func TestConvertClassificationResponseNil(t *testing.T) {
	got := convertClassificationResponse(nil, "")
	if got == nil {
		t.Fatal("convertClassificationResponse(nil) returned nil")
	}
	if got.Categories == nil {
		t.Fatal("categories must be an empty slice, not nil, so JSON renders [] rather than null")
	}
	if got.Language != "" {
		t.Fatalf("language = %q, want empty when none was requested", got.Language)
	}
}

func TestClassificationApplyFilters(t *testing.T) {
	base := &ClassificationResult{
		Provider: "fake",
		Categories: []Category{
			{Name: "/Science", Confidence: 0.3},
			{Name: "/Computers & Electronics/Software", Confidence: 0.9},
			{Name: "/Business & Industrial", Confidence: 0.55},
		},
	}

	// No filters: only sorted.
	all := base.Apply(ClassifyFilterOptions{})
	if len(all.Categories) != 3 || all.Categories[0].Name != "/Computers & Electronics/Software" {
		t.Fatalf("unfiltered = %#v", all.Categories)
	}

	got := base.Apply(ClassifyFilterOptions{MinConfidence: 0.5})
	if len(got.Categories) != 2 {
		t.Fatalf("--min-confidence 0.5 kept %#v, want 2", got.Categories)
	}
	for _, c := range got.Categories {
		if c.Confidence < 0.5 {
			t.Fatalf("threshold leaked %#v", c)
		}
	}

	got = base.Apply(ClassifyFilterOptions{Top: 1})
	if len(got.Categories) != 1 || got.Categories[0].Name != "/Computers & Electronics/Software" {
		t.Fatalf("--top 1 = %#v", got.Categories)
	}

	// Both together: the threshold runs first, then the truncation.
	got = base.Apply(ClassifyFilterOptions{MinConfidence: 0.5, Top: 1})
	if len(got.Categories) != 1 || got.Categories[0].Confidence != 0.9 {
		t.Fatalf("combined filters = %#v", got.Categories)
	}

	// Filtering everything out leaves an empty list, never nil.
	got = base.Apply(ClassifyFilterOptions{MinConfidence: 0.99})
	if got.Categories == nil || len(got.Categories) != 0 {
		t.Fatalf("categories = %#v, want an empty slice", got.Categories)
	}

	// The receiver is untouched: Apply works on a copy.
	if len(base.Categories) != 3 || base.Categories[0].Name != "/Science" {
		t.Fatalf("Apply mutated the receiver: %#v", base.Categories)
	}
	if (*ClassificationResult)(nil).Apply(ClassifyFilterOptions{}) != nil {
		t.Fatal("nil receiver must stay nil")
	}
}

// TestCheckClassifiable pins the pre-check that keeps a confusing raw
// InvalidArgument from the API off the user's screen.
func TestCheckClassifiable(t *testing.T) {
	short := "Ada Lovelace worked in London."
	err := CheckClassifiable(short)
	var e *errs.E
	if !asE(err, &e) {
		t.Fatalf("err = %v (%T), want *errs.E", err, err)
	}
	if e.Code != errs.CodeInvalidArgs {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeInvalidArgs)
	}
	if !strings.Contains(e.Message, "5 words") {
		t.Fatalf("message %q should report the actual word count", e.Message)
	}
	if !strings.Contains(e.Hint, "20+") {
		t.Fatalf("hint %q should say roughly how many words are needed", e.Hint)
	}
	if errs.ExitCode(err) != 5 {
		t.Fatalf("exit code = %d, want 5", errs.ExitCode(err))
	}

	if err := CheckClassifiable(""); err == nil {
		t.Fatal("empty text must be rejected too")
	}

	// Exactly at the floor is accepted: the guard must not be stricter than the
	// documented minimum.
	atFloor := strings.TrimSpace(strings.Repeat("word ", MinClassifyWords))
	if n := WordCount(atFloor); n != MinClassifyWords {
		t.Fatalf("fixture has %d words, want %d", n, MinClassifyWords)
	}
	if err := CheckClassifiable(atFloor); err != nil {
		t.Fatalf("%d words rejected: %v", MinClassifyWords, err)
	}

	long := "Cloud Natural Language sorts a document into a taxonomy of content categories, " +
		"which is only meaningful once the document carries enough context to be about something. " +
		"A single sentence is not enough for the classifier to work with."
	if err := CheckClassifiable(long); err != nil {
		t.Fatalf("a full paragraph was rejected: %v", err)
	}
}

// classifierOnlyProvider implements the base Provider plus classification, but
// not sentiment — the mirror image of sentimentOnlyProvider in sentiment_test.
type classifierOnlyProvider struct{ name string }

func (f *classifierOnlyProvider) Name() string { return f.name }

func (f *classifierOnlyProvider) AnalyzeEntities(context.Context, string, Options) (*Result, error) {
	return &Result{Provider: f.name, Entities: []Entity{}}, nil
}

func (f *classifierOnlyProvider) ClassifyText(context.Context, string, Options) (*ClassificationResult, error) {
	return &ClassificationResult{
		Provider:   f.name,
		Categories: []Category{{Name: "/Science", Confidence: 0.8}},
	}, nil
}

func TestRequireClassifier(t *testing.T) {
	Register("cap-classify", func() (Provider, error) { return &classifierOnlyProvider{name: "cap-classify"}, nil })

	p, err := Open("cap-classify")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c, err := RequireClassifier(p)
	if err != nil {
		t.Fatalf("RequireClassifier: %v", err)
	}
	res, err := c.ClassifyText(context.Background(), "x", Options{})
	if err != nil || len(res.Categories) != 1 {
		t.Fatalf("ClassifyText = %#v, %v", res, err)
	}

	var found bool
	for _, n := range ClassifierProviders() {
		if n == "cap-classify" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ClassifierProviders() = %v, want it to include cap-classify", ClassifierProviders())
	}
}

// TestRequireSentimentRejectsIncapableProvider is the other half of the
// capability contract: entities+classification is not enough for `text
// sentiment`, and the refusal must name a backend that would have worked.
func TestRequireSentimentRejectsIncapableProvider(t *testing.T) {
	got, err := RequireSentiment(&classifierOnlyProvider{name: "classify-only"})
	if got != nil {
		t.Fatalf("RequireSentiment returned %#v for an incapable provider", got)
	}
	var e *errs.E
	if !asE(err, &e) {
		t.Fatalf("err = %v (%T), want *errs.E", err, err)
	}
	if e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeProviderUnavailable)
	}
	if !strings.Contains(e.Message, "classify-only") || !strings.Contains(e.Message, "sentiment") {
		t.Fatalf("message %q should name the provider and the missing capability", e.Message)
	}
	if !strings.Contains(e.Hint, ProviderGoogle) {
		t.Fatalf("hint %q should name a provider that does support it", e.Hint)
	}
}

// TestBaseProviderNeedsNoCapabilities is the design guarantee: a backend that
// only extracts entities — the shape the Wikipedia provider will have —
// compiles and registers without stubbing anything out.
func TestBaseProviderNeedsNoCapabilities(t *testing.T) {
	p := &fakeProvider{name: "entities-only"}
	if _, ok := any(p).(SentimentAnalyzer); ok {
		t.Fatal("the entities-only fake must not satisfy SentimentAnalyzer")
	}
	if _, ok := any(p).(TextClassifier); ok {
		t.Fatal("the entities-only fake must not satisfy TextClassifier")
	}
	var _ Provider = p
}
