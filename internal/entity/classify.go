package entity

import (
	"context"
	"sort"
	"strings"

	"cloud.google.com/go/language/apiv1/languagepb"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// MinClassifyWords is the shortest document the content classifier will accept.
//
// Cloud Natural Language rejects anything shorter with InvalidArgument and a
// message about the document being too small; ~20 words is the documented floor
// and the number the pre-check below reports, so the user gets told what to fix
// instead of a raw API complaint.
const MinClassifyWords = 20

// ClassificationResult is one provider's category answer for one document.
type ClassificationResult struct {
	Provider string `json:"provider"`
	// Language is the language the document was analysed in.
	//
	// Cloud Natural Language v1's ClassifyText response carries no language
	// field — unlike AnalyzeEntities and AnalyzeSentiment — so for that backend
	// this echoes the requested language and stays empty when the language was
	// left to auto-detection. It is not a detection result.
	Language   string     `json:"language,omitempty"`
	Categories []Category `json:"categories"`
}

// Category is one content category the classifier assigned.
type Category struct {
	// Name is the full taxonomy path, e.g. "/Computers & Electronics/Software".
	// The leading slash and the levels are the provider's; they are passed
	// through unchanged so a caller can split on "/" to get a hierarchy.
	Name string `json:"name"`
	// Confidence is how sure the classifier is, in (0, 1]. Unlike salience these
	// do not sum to 1.0 across the list: a document can belong to several
	// categories with high confidence in each.
	Confidence float64 `json:"confidence"`
}

// ClassifyFilterOptions is the post-processing a command applies to a
// classification. Separate from Options for the same reason FilterOptions is:
// filtering happens after the cache, so changing --top never costs a call.
type ClassifyFilterOptions struct {
	// MinConfidence drops categories below this confidence. <= 0 is a no-op.
	MinConfidence float64
	// Top keeps only the N most confident categories. <= 0 means all.
	Top int
}

// Apply filters, sorts, and truncates a copy of the result's categories.
func (r *ClassificationResult) Apply(o ClassifyFilterOptions) *ClassificationResult {
	if r == nil {
		return nil
	}
	out := *r
	cats := make([]Category, 0, len(r.Categories))
	for _, c := range r.Categories {
		if o.MinConfidence > 0 && c.Confidence < o.MinConfidence {
			continue
		}
		cats = append(cats, c)
	}
	SortCategories(cats)
	if o.Top > 0 && o.Top < len(cats) {
		cats = cats[:o.Top]
	}
	out.Categories = cats
	return &out
}

// SortCategories orders by confidence desc, then name asc. The name tiebreak is
// what makes two runs over the same data byte-identical.
func SortCategories(cats []Category) {
	sort.SliceStable(cats, func(i, j int) bool {
		if cats[i].Confidence != cats[j].Confidence {
			return cats[i].Confidence > cats[j].Confidence
		}
		return cats[i].Name < cats[j].Name
	})
}

// CheckClassifiable rejects text the classifier is guaranteed to refuse.
//
// Without it the user pays a round trip to be told "invalid argument" by an API
// that does not say why. The word count is the same one `text metrics` reports,
// so the number in the error matches the number the rest of the CLI shows.
func CheckClassifiable(text string) error {
	n := WordCount(text)
	if n >= MinClassifyWords {
		return nil
	}
	return errs.Newf(errs.CodeInvalidArgs, "text is too short to classify: %d words, the classifier needs about %d", n, MinClassifyWords).
		WithHint("Content classification only works on a document with enough context — roughly 20+ words, a full paragraph. Pass a longer text, or use `text entities` or `text sentiment`, which work on short input.")
}

// WordCount counts words the way the rest of the CLI does, falling back to
// whitespace splitting for input the tokenizer refuses.
func WordCount(text string) int {
	if doc, err := textproc.Analyze(text, textproc.LangAuto); err == nil && doc != nil {
		return doc.Stats.Words
	}
	return len(strings.Fields(text))
}

var _ TextClassifier = (*googleProvider)(nil)

// ClassifyText implements TextClassifier for Cloud Natural Language v1.
func (p *googleProvider) ClassifyText(ctx context.Context, text string, opts Options) (*ClassificationResult, error) {
	c, err := p.clientFor(ctx, opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.EffectiveTimeout())
	defer cancel()

	resp, err := c.ClassifyText(ctx, &languagepb.ClassifyTextRequest{
		Document: &languagepb.Document{
			Type:   languagepb.Document_PLAIN_TEXT,
			Source: &languagepb.Document_Content{Content: text},
			// Empty asks the API to detect the language.
			Language: opts.Language,
		},
		// ClassificationModelOptions is left unset on purpose. Unset means the
		// v1 model and the ~700-category v1 taxonomy, which is what the
		// documented category strings ("/Computers & Electronics/Software")
		// belong to; selecting the v2 model would silently change the taxonomy
		// under callers that match on those strings. There is also no
		// EncodingType on this request — the response has no offsets.
	})
	if err != nil {
		return nil, Translate(err)
	}
	return convertClassificationResponse(resp, opts.Language), nil
}

// convertClassificationResponse maps the protobuf response onto the neutral
// domain type. Pure and total, so it is unit-testable without a credential.
//
// language is the requested language, echoed into the result: the v1 response
// does not report one.
func convertClassificationResponse(resp *languagepb.ClassifyTextResponse, language string) *ClassificationResult {
	out := &ClassificationResult{Provider: ProviderGoogle, Language: language, Categories: []Category{}}
	if resp == nil {
		return out
	}
	for _, c := range resp.GetCategories() {
		if c == nil {
			continue
		}
		out.Categories = append(out.Categories, Category{
			Name: c.GetName(),
			// float32 -> float64 widening is noisy; four decimals, the same
			// convention salience uses.
			Confidence: Round4(float64(c.GetConfidence())),
		})
	}
	SortCategories(out.Categories)
	return out
}
