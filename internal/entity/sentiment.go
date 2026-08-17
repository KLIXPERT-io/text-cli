package entity

import (
	"context"

	"cloud.google.com/go/language/apiv1/languagepb"
)

// Sentiment labels. They are derived, never reported by a provider, so the
// derivation rule below is the contract a consumer branches on.
const (
	LabelPositive = "positive"
	LabelNegative = "negative"
	LabelNeutral  = "neutral"
	LabelMixed    = "mixed"
)

// Thresholds for deriving a label from (score, magnitude).
//
// Score alone cannot tell neutral from mixed, and that is the mistake worth
// spelling out: score is an *average* leaning, so a document that is half
// furious and half delighted averages to about 0.0 — exactly what a document
// with no feeling at all also scores. Magnitude is what separates them. It is
// the total amount of emotion found, in either direction and unbounded, so the
// furious/delighted document lands near zero score with a high magnitude while
// a dry technical note lands near zero score with a magnitude near zero.
const (
	// LabelScoreThreshold is the |score| at which a document counts as
	// one-sided. 0.25 is the boundary Google's own interpretation guide uses
	// for "clearly positive/negative"; below it the sign is noise.
	LabelScoreThreshold = 0.25
	// LabelMagnitudeThreshold is the magnitude at which a document with no
	// clear leaning is called mixed rather than neutral.
	//
	// Magnitude is roughly additive over emotional sentences, so 1.0 is about
	// "one sentence's worth of real feeling". Below it there is essentially
	// nothing to be conflicted about. Note that magnitude also grows with
	// length: a long, mildly emotional document can cross this while no single
	// sentence is strong, and calling that mixed is the honest reading — the
	// feeling is there, it just does not point one way.
	LabelMagnitudeThreshold = 1.0
)

// Label derives the document label from a score/magnitude pair.
//
// The four quadrants:
//
//	score        magnitude   label
//	>= +0.25     any         positive
//	<= -0.25     any         negative
//	near 0       >= 1.0      mixed     (strong feeling, both directions)
//	near 0       <  1.0      neutral   (no feeling at all)
func Label(score, magnitude float64) string {
	switch {
	case score >= LabelScoreThreshold:
		return LabelPositive
	case score <= -LabelScoreThreshold:
		return LabelNegative
	case magnitude >= LabelMagnitudeThreshold:
		return LabelMixed
	default:
		return LabelNeutral
	}
}

// SentimentResult is one provider's polarity answer for one document.
//
// Provider-neutral like Result: a backend that cannot break a document into
// sentences leaves Sentences empty rather than inventing one entry for the
// whole text.
type SentimentResult struct {
	Provider string `json:"provider"`
	// Language is the language the provider analysed in — the one requested, or
	// the one it detected when none was.
	Language string `json:"language,omitempty"`
	// Score is the document's average emotional leaning, -1.0 (negative) to
	// +1.0 (positive). It says which way, not how much.
	Score float64 `json:"score"`
	// Magnitude is the total strength of emotion, 0 to +inf. It is NOT
	// normalized and grows with document length, so it is only comparable
	// between documents of similar size.
	Magnitude float64 `json:"magnitude"`
	// Label is derived from Score and Magnitude by Label().
	Label string `json:"label"`
	// Sentences is the per-sentence breakdown, in document order.
	Sentences []SentenceSentiment `json:"sentences,omitempty"`
}

// SentenceSentiment is one sentence's polarity.
type SentenceSentiment struct {
	Text      string  `json:"text"`
	Score     float64 `json:"score"`
	Magnitude float64 `json:"magnitude"`
	// BeginOffset is the offset into the source text, in the units the provider
	// was asked for (UTF-8 bytes for the Google backend), so a caller can slice
	// the original document with it.
	BeginOffset int `json:"begin_offset"`
}

// WithoutSentences returns a copy with the per-sentence breakdown dropped.
//
// It exists so `--sentences=false` is a rendering choice rather than a request
// choice: the provider is always asked for sentences and the full payload is
// what gets cached, so toggling the flag re-renders instead of re-billing.
func (r *SentimentResult) WithoutSentences() *SentimentResult {
	if r == nil {
		return nil
	}
	out := *r
	out.Sentences = nil
	return &out
}

var _ SentimentAnalyzer = (*googleProvider)(nil)

// AnalyzeSentiment implements SentimentAnalyzer for Cloud Natural Language v1.
func (p *googleProvider) AnalyzeSentiment(ctx context.Context, text string, opts Options) (*SentimentResult, error) {
	c, err := p.clientFor(ctx, opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.EffectiveTimeout())
	defer cancel()

	resp, err := c.AnalyzeSentiment(ctx, &languagepb.AnalyzeSentimentRequest{
		Document: &languagepb.Document{
			Type:   languagepb.Document_PLAIN_TEXT,
			Source: &languagepb.Document_Content{Content: text},
			// Empty asks the API to detect the language.
			Language: opts.Language,
		},
		// UTF8 makes the sentence offsets byte offsets into the text we sent,
		// which is what a Go caller can slice with directly.
		EncodingType: languagepb.EncodingType_UTF8,
	})
	if err != nil {
		return nil, Translate(err)
	}
	return convertSentimentResponse(resp), nil
}

// convertSentimentResponse maps the protobuf response onto the neutral domain
// type. Pure and total — nil-safe on every nested pointer — so the mapping is
// unit-testable without a network or a credential.
func convertSentimentResponse(resp *languagepb.AnalyzeSentimentResponse) *SentimentResult {
	out := &SentimentResult{Provider: ProviderGoogle}
	if resp == nil {
		out.Label = Label(0, 0)
		return out
	}
	out.Language = resp.GetLanguage()

	ds := resp.GetDocumentSentiment()
	// Scores arrive as float32 whose widening to float64 is noisy
	// (0.20000000298023224); four decimals is the same convention salience uses
	// and keeps output diffable.
	out.Score = Round4(float64(ds.GetScore()))
	out.Magnitude = Round4(float64(ds.GetMagnitude()))
	out.Label = Label(out.Score, out.Magnitude)

	for _, s := range resp.GetSentences() {
		if s == nil {
			continue
		}
		ss := s.GetSentiment()
		out.Sentences = append(out.Sentences, SentenceSentiment{
			Text:        s.GetText().GetContent(),
			Score:       Round4(float64(ss.GetScore())),
			Magnitude:   Round4(float64(ss.GetMagnitude())),
			BeginOffset: int(s.GetText().GetBeginOffset()),
		})
	}
	return out
}
