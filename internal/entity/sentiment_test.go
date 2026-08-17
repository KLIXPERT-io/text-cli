package entity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cloud.google.com/go/language/apiv1/languagepb"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// TestLabelQuadrants pins the whole point of the label rule: score alone cannot
// separate neutral from mixed, so all four (score, magnitude) quadrants are
// checked, including the one everybody gets wrong — score ~0 with a high
// magnitude is a document that feels strongly in both directions, not a
// document that feels nothing.
func TestLabelQuadrants(t *testing.T) {
	cases := []struct {
		name      string
		score     float64
		magnitude float64
		want      string
	}{
		{"high score, high magnitude", 0.8, 3.0, LabelPositive},
		{"high score, low magnitude", 0.6, 0.6, LabelPositive},
		{"negative score, high magnitude", -0.7, 4.0, LabelNegative},
		{"negative score, low magnitude", -0.5, 0.2, LabelNegative},
		{"low score, high magnitude", 0.0, 4.0, LabelMixed},
		{"low score, low magnitude", 0.1, 0.0, LabelNeutral},

		// Boundaries: the thresholds are inclusive on the label-changing side.
		{"exactly the score threshold", LabelScoreThreshold, 0, LabelPositive},
		{"exactly minus the score threshold", -LabelScoreThreshold, 0, LabelNegative},
		{"just under the score threshold with feeling", 0.24, LabelMagnitudeThreshold, LabelMixed},
		{"just under the score threshold without feeling", 0.24, 0.99, LabelNeutral},
		{"negative but flat", -0.2, 0.1, LabelNeutral},
		{"negative but conflicted", -0.2, 2.5, LabelMixed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Label(tc.score, tc.magnitude); got != tc.want {
				t.Fatalf("Label(%v, %v) = %q, want %q", tc.score, tc.magnitude, got, tc.want)
			}
		})
	}
}

func TestConvertSentimentResponse(t *testing.T) {
	resp := &languagepb.AnalyzeSentimentResponse{
		Language:          "en",
		DocumentSentiment: &languagepb.Sentiment{Score: 0.80000001, Magnitude: 1.60000002},
		Sentences: []*languagepb.Sentence{
			{
				Text:      &languagepb.TextSpan{Content: "This is wonderful.", BeginOffset: 0},
				Sentiment: &languagepb.Sentiment{Score: 0.9, Magnitude: 0.9},
			},
			nil, // a nil entry must not panic or produce a phantom sentence
			{
				Text:      &languagepb.TextSpan{Content: "This part is awful.", BeginOffset: 19},
				Sentiment: &languagepb.Sentiment{Score: -0.7, Magnitude: 0.7},
			},
			// A sentence with no sentiment at all: still a sentence, zeroed.
			{Text: &languagepb.TextSpan{Content: "It exists.", BeginOffset: 39}},
		},
	}

	got := convertSentimentResponse(resp)
	if got.Provider != ProviderGoogle {
		t.Fatalf("provider = %q", got.Provider)
	}
	if got.Language != "en" {
		t.Fatalf("language = %q", got.Language)
	}
	// float32 noise is rounded away to four decimals.
	if got.Score != 0.8 || got.Magnitude != 1.6 {
		t.Fatalf("score/magnitude = %v/%v, want 0.8/1.6", got.Score, got.Magnitude)
	}
	if got.Label != LabelPositive {
		t.Fatalf("label = %q", got.Label)
	}
	if len(got.Sentences) != 3 {
		t.Fatalf("sentences = %#v, want 3 (the nil dropped)", got.Sentences)
	}
	if got.Sentences[0].Text != "This is wonderful." || got.Sentences[0].Score != 0.9 {
		t.Fatalf("first sentence = %#v", got.Sentences[0])
	}
	if got.Sentences[1].BeginOffset != 19 || got.Sentences[1].Score != -0.7 {
		t.Fatalf("second sentence = %#v", got.Sentences[1])
	}
	if got.Sentences[2].Score != 0 || got.Sentences[2].Magnitude != 0 {
		t.Fatalf("sentiment-less sentence = %#v", got.Sentences[2])
	}
}

// TestConvertSentimentResponseMixedDocument is the realistic mixed case: the
// sentences disagree violently, the document average is ~0, and the label must
// not read "neutral".
func TestConvertSentimentResponseMixedDocument(t *testing.T) {
	got := convertSentimentResponse(&languagepb.AnalyzeSentimentResponse{
		Language:          "en",
		DocumentSentiment: &languagepb.Sentiment{Score: 0.0, Magnitude: 3.8},
		Sentences: []*languagepb.Sentence{
			{Text: &languagepb.TextSpan{Content: "I love the design."}, Sentiment: &languagepb.Sentiment{Score: 0.9, Magnitude: 0.9}},
			{Text: &languagepb.TextSpan{Content: "I hate the price."}, Sentiment: &languagepb.Sentiment{Score: -0.9, Magnitude: 0.9}},
		},
	})
	if got.Label != LabelMixed {
		t.Fatalf("label = %q, want mixed: score 0.0 with magnitude 3.8 is conflict, not silence", got.Label)
	}
}

func TestConvertSentimentResponseNil(t *testing.T) {
	got := convertSentimentResponse(nil)
	if got == nil {
		t.Fatal("convertSentimentResponse(nil) returned nil")
	}
	if got.Label != LabelNeutral || got.Score != 0 || got.Magnitude != 0 {
		t.Fatalf("nil response = %#v", got)
	}
	if len(got.Sentences) != 0 {
		t.Fatalf("sentences = %#v", got.Sentences)
	}
}

func TestWithoutSentences(t *testing.T) {
	r := &SentimentResult{
		Score: 0.5, Magnitude: 2, Label: LabelPositive,
		Sentences: []SentenceSentiment{{Text: "hi", Score: 0.5}},
	}
	got := r.WithoutSentences()
	if len(got.Sentences) != 0 {
		t.Fatalf("sentences = %#v", got.Sentences)
	}
	if len(r.Sentences) != 1 {
		t.Fatal("WithoutSentences mutated the receiver")
	}
	if got.Score != 0.5 || got.Label != LabelPositive {
		t.Fatalf("copy lost fields: %#v", got)
	}
	if (*SentimentResult)(nil).WithoutSentences() != nil {
		t.Fatal("nil receiver must stay nil")
	}
}

// sentimentOnlyProvider implements the base Provider plus the sentiment
// capability, but not classification.
type sentimentOnlyProvider struct{ name string }

func (f *sentimentOnlyProvider) Name() string { return f.name }

func (f *sentimentOnlyProvider) AnalyzeEntities(context.Context, string, Options) (*Result, error) {
	return &Result{Provider: f.name, Entities: []Entity{}}, nil
}

func (f *sentimentOnlyProvider) AnalyzeSentiment(context.Context, string, Options) (*SentimentResult, error) {
	return &SentimentResult{Provider: f.name, Score: 0.5, Magnitude: 1.2, Label: LabelPositive}, nil
}

func TestRequireSentiment(t *testing.T) {
	Register("cap-sentiment", func() (Provider, error) { return &sentimentOnlyProvider{name: "cap-sentiment"}, nil })

	p, err := Open("cap-sentiment")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a, err := RequireSentiment(p)
	if err != nil {
		t.Fatalf("RequireSentiment: %v", err)
	}
	res, err := a.AnalyzeSentiment(context.Background(), "x", Options{})
	if err != nil || res.Label != LabelPositive {
		t.Fatalf("AnalyzeSentiment = %#v, %v", res, err)
	}

	// The capability list must name the provider that has it.
	var found bool
	for _, n := range SentimentProviders() {
		if n == "cap-sentiment" {
			found = true
		}
	}
	if !found {
		t.Fatalf("SentimentProviders() = %v, want it to include cap-sentiment", SentimentProviders())
	}
}

// TestRequireClassifierRejectsIncapableProvider is the capability-assertion
// failure: a backend that only does entities and sentiment must be refused with
// a code the caller can branch on, and a hint naming a backend that would work.
func TestRequireClassifierRejectsIncapableProvider(t *testing.T) {
	p := &sentimentOnlyProvider{name: "sentiment-only"}

	got, err := RequireClassifier(p)
	if got != nil {
		t.Fatalf("RequireClassifier returned %#v for an incapable provider", got)
	}
	var e *errs.E
	if !asE(err, &e) {
		t.Fatalf("err = %v (%T), want *errs.E", err, err)
	}
	if e.Code != errs.CodeProviderUnavailable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeProviderUnavailable)
	}
	if !strings.Contains(e.Message, "sentiment-only") || !strings.Contains(e.Message, "classification") {
		t.Fatalf("message %q should name the provider and the missing capability", e.Message)
	}
	if !strings.Contains(e.Hint, ProviderGoogle) {
		t.Fatalf("hint %q should name a provider that does support it", e.Hint)
	}
	if errs.ExitCode(err) != 6 {
		t.Fatalf("exit code = %d, want 6", errs.ExitCode(err))
	}
}

func TestGoogleProviderImplementsBothCapabilities(t *testing.T) {
	p, err := Open(ProviderGoogle)
	if err != nil {
		t.Fatalf("Open(google): %v", err)
	}
	if _, err := RequireSentiment(p); err != nil {
		t.Fatalf("google must analyse sentiment: %v", err)
	}
	if _, err := RequireClassifier(p); err != nil {
		t.Fatalf("google must classify: %v", err)
	}
}

// asE is errors.As with a shorter name, used by the capability assertions here
// and in classify_test.go.
func asE(err error, target **errs.E) bool {
	return errors.As(err, target)
}
