package research

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
)

// SourceFirecrawl is the registered name of this backend.
const SourceFirecrawl = "firecrawl"

func init() {
	Register(SourceFirecrawl, func() (Source, error) { return &firecrawlSource{}, nil })
}

// firecrawlSource searches Firecrawl's research index, which spans arXiv,
// PubMed, and DOI-addressed literature behind one identifier scheme.
//
// It implements all three interfaces in this package. That is a property of
// this backend, not of the design: the capability split exists so the next
// source does not have to.
type firecrawlSource struct {
	apiKey  string
	baseURL string
}

func (s *firecrawlSource) Name() string { return SourceFirecrawl }

var (
	_ APIConfigurer  = (*firecrawlSource)(nil)
	_ PaperInspector = (*firecrawlSource)(nil)
	_ SimilarFinder  = (*firecrawlSource)(nil)
)

func (s *firecrawlSource) SetAPIKey(key string)   { s.apiKey = key }
func (s *firecrawlSource) SetBaseURL(base string) { s.baseURL = base }

// Note what is absent from every method below: a firecrawl.RequireKey check.
// Unlike the scrape endpoint, the research endpoints answer unauthenticated
// requests, so demanding a key here would fail calls that would have
// succeeded. The key is still sent when there is one — an authenticated caller
// gets their own rate limit rather than the shared anonymous one.

// paperRecord is one search or similar-papers hit on the wire.
type paperRecord struct {
	PaperID   string              `json:"paperId"`
	PrimaryID string              `json:"primaryId"`
	IDs       map[string][]string `json:"ids"`
	Title     string              `json:"title"`
	Abstract  string              `json:"abstract"`
	Score     float64             `json:"score"`
	// Only the inspect endpoint fills these; on a search hit they are empty
	// and the omitempty tags on Paper keep them out of the output.
	Authors     string   `json:"authors"`
	Categories  []string `json:"categories"`
	CreatedDate string   `json:"createdDate"`
	UpdateDate  string   `json:"updateDate"`
}

func (r paperRecord) toPaper() Paper {
	p := Paper{
		ID:         r.PaperID,
		PrimaryID:  r.PrimaryID,
		IDs:        r.IDs,
		Title:      strings.TrimSpace(r.Title),
		Abstract:   strings.TrimSpace(r.Abstract),
		Authors:    strings.TrimSpace(r.Authors),
		Categories: r.Categories,
		Published:  strings.TrimSpace(r.CreatedDate),
		Updated:    strings.TrimSpace(r.UpdateDate),
		Score:      r.Score,
	}
	if p.PrimaryID == "" {
		p.PrimaryID = preferredID(r.IDs)
	}
	p.URL = LandingURL(p.PrimaryID)
	return p
}

type resultsResponse struct {
	Success bool          `json:"success"`
	Results []paperRecord `json:"results"`
}

type inspectResponse struct {
	PaperID string      `json:"paperId"`
	Paper   paperRecord `json:"paper"`
	// The inspect endpoint returns no "success" field, so its absence is not
	// an error — only an HTTP failure or a missing paper is.
	Passages []struct {
		Text  string  `json:"text"`
		Score float64 `json:"score"`
	} `json:"passages"`
}

func (s *firecrawlSource) SearchPapers(ctx context.Context, opts SearchOptions) ([]Paper, error) {
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "empty research query").
			WithHint(`Pass a question, e.g. text research papers "how is readability measured?".`)
	}

	q := url.Values{}
	q.Set("query", query)
	q.Set("k", strconv.Itoa(opts.EffectiveLimit()))
	if v := strings.TrimSpace(opts.Authors); v != "" {
		q.Set("authors", v)
	}
	if v := strings.TrimSpace(opts.Categories); v != "" {
		q.Set("categories", v)
	}
	if v := strings.TrimSpace(opts.From); v != "" {
		q.Set("from", v)
	}
	if v := strings.TrimSpace(opts.To); v != "" {
		q.Set("to", v)
	}

	c := firecrawl.New(s.apiKey, s.baseURL)
	c.SetTimeout(opts.EffectiveTimeout())

	var resp resultsResponse
	if err := c.Get(ctx, "/v2/search/research/papers", q, &resp); err != nil {
		return nil, err
	}
	return toPapers(resp.Results), nil
}

func (s *firecrawlSource) InspectPaper(ctx context.Context, id string, opts InspectOptions) (*PaperDetail, error) {
	pid, err := NormalizeID(id)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	if query := strings.TrimSpace(opts.Query); query != "" {
		q.Set("query", query)
		// k is only accepted alongside a question: the API rejects a passage
		// count with nothing to rank passages against, so it is set here
		// rather than unconditionally.
		limit := opts.Limit
		if limit <= 0 {
			limit = DefaultLimit
		}
		if limit > MaxLimit {
			limit = MaxLimit
		}
		q.Set("k", strconv.Itoa(limit))
	}

	c := firecrawl.New(s.apiKey, s.baseURL)
	c.SetTimeout(opts.EffectiveTimeout())

	var resp inspectResponse
	// PathEscape, not raw interpolation: an id is namespaced with a colon and a
	// DOI contains slashes, and "doi:10.1145/3442188" must not be read as three
	// path segments.
	if err := c.Get(ctx, "/v2/search/research/papers/"+url.PathEscape(pid), q, &resp); err != nil {
		return nil, err
	}
	if resp.Paper.Title == "" && resp.PaperID == "" {
		return nil, errs.Newf(errs.CodeNotFound, "no paper with id %q", pid).
			WithHint(`Ids are namespaced: arxiv:1706.03762, doi:10.1145/3442188, pmid:18027780. Find one with "text research papers".`)
	}

	rec := resp.Paper
	if rec.PaperID == "" {
		rec.PaperID = resp.PaperID
	}
	detail := &PaperDetail{Paper: rec.toPaper()}
	// The requested id is the better citation: a caller who asked for
	// "arxiv:1706.03762" should not get "pmid:…" back because the index
	// happens to prefer it.
	if detail.Paper.PrimaryID == "" || strings.Contains(pid, ":") {
		detail.Paper.PrimaryID = pid
		detail.Paper.URL = LandingURL(pid)
	}
	for _, p := range resp.Passages {
		text := strings.TrimSpace(p.Text)
		if text == "" {
			continue
		}
		detail.Passages = append(detail.Passages, Passage{Text: text, Score: p.Score})
	}
	return detail, nil
}

func (s *firecrawlSource) SimilarPapers(ctx context.Context, id string, opts SimilarOptions) ([]Paper, error) {
	pid, err := NormalizeID(id)
	if err != nil {
		return nil, err
	}
	intent := strings.TrimSpace(opts.Intent)
	if intent == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "no --intent given").
			WithHint(`Say what makes a neighbour interesting, e.g. --intent "cheaper attention variants". "Similar" is ambiguous without it.`)
	}

	q := url.Values{}
	q.Set("intent", intent)
	q.Set("mode", string(opts.EffectiveRelation()))
	q.Set("k", strconv.Itoa(opts.EffectiveLimit()))

	c := firecrawl.New(s.apiKey, s.baseURL)
	c.SetTimeout(opts.EffectiveTimeout())

	var resp resultsResponse
	if err := c.Get(ctx, "/v2/search/research/papers/"+url.PathEscape(pid)+"/similar", q, &resp); err != nil {
		return nil, err
	}
	return toPapers(resp.Results), nil
}

func toPapers(records []paperRecord) []Paper {
	// Never nil: an empty result set renders as [] rather than null, so a
	// consumer can iterate the field without a nil check.
	out := make([]Paper, 0, len(records))
	for _, r := range records {
		if strings.TrimSpace(r.Title) == "" && strings.TrimSpace(r.Abstract) == "" {
			continue
		}
		out = append(out, r.toPaper())
	}
	return out
}

// idNamespacePreference is the order a primary identifier is chosen in when the
// source does not name one. DOI first because it is the durable, publisher-side
// identifier; arXiv next because it resolves to a free full text; the PubMed
// ids last because they are index-specific.
var idNamespacePreference = []string{"doi", "arxiv", "pmcid", "pmid"}

func preferredID(ids map[string][]string) string {
	for _, ns := range idNamespacePreference {
		for _, v := range ids[ns] {
			if v = strings.TrimSpace(v); v != "" {
				return ns + ":" + v
			}
		}
	}
	return ""
}

// NormalizeID validates a paper identifier and returns it in the namespaced
// form the API expects.
//
// A bare "1706.03762" is what people copy out of a URL bar, and guessing arXiv
// for it would be wrong as often as right — a bare number is also a PMID. So a
// namespace is required, and the error names the four that work rather than
// letting the API answer with a 400 a second later.
func NormalizeID(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", errs.New(errs.CodeInvalidArgs, "empty paper id").
			WithHint(`Pass a namespaced id, e.g. arxiv:1706.03762.`)
	}
	ns, rest, ok := strings.Cut(v, ":")
	if !ok || strings.TrimSpace(rest) == "" {
		return "", errs.Newf(errs.CodeInvalidArgs, "paper id %q has no namespace", raw).
			WithHint(`Use arxiv:<id>, doi:<doi>, pmid:<id>, or pmcid:<id>. Find one with "text research papers".`)
	}
	// The index's own opaque numeric paperId has no namespace and is not
	// accepted here on purpose: it is not portable, and PrimaryID is what the
	// search output puts in front of the user for exactly this reason.
	return strings.ToLower(strings.TrimSpace(ns)) + ":" + strings.TrimSpace(rest), nil
}

// LandingURL turns a namespaced id into a page a human can open. An unknown
// namespace yields "" rather than a guessed URL: a broken link in cited output
// is worse than no link.
func LandingURL(primaryID string) string {
	ns, rest, ok := strings.Cut(strings.TrimSpace(primaryID), ":")
	if !ok || rest == "" {
		return ""
	}
	switch strings.ToLower(ns) {
	case "arxiv":
		return "https://arxiv.org/abs/" + rest
	case "doi":
		return "https://doi.org/" + rest
	case "pmid":
		return "https://pubmed.ncbi.nlm.nih.gov/" + rest + "/"
	case "pmcid":
		return "https://www.ncbi.nlm.nih.gov/pmc/articles/" + rest + "/"
	default:
		return ""
	}
}
