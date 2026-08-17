package research

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// searchOnlySource implements Source and neither capability, which is the case
// the capability split exists for.
type searchOnlySource struct{}

func (searchOnlySource) Name() string { return "search-only" }
func (searchOnlySource) SearchPapers(context.Context, SearchOptions) ([]Paper, error) {
	return nil, nil
}

func TestRegistryResolvesTheFirecrawlBackend(t *testing.T) {
	if !Registered(SourceFirecrawl) {
		t.Fatalf("Names() = %v, want the firecrawl source registered by its init", Names())
	}
	s, err := Open(SourceFirecrawl)
	if err != nil {
		t.Fatalf("Open(%q) errored: %v", SourceFirecrawl, err)
	}
	if s.Name() != SourceFirecrawl {
		t.Errorf("Name() = %q, want %q", s.Name(), SourceFirecrawl)
	}
}

func TestOpenUnknownIsInvalidArgs(t *testing.T) {
	_, err := Open("nope")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args", err)
	}
	if !strings.Contains(e.Hint, SourceFirecrawl) {
		t.Errorf("hint = %q, want it to name the registered sources", e.Hint)
	}
}

func TestCapabilitiesAreSatisfiedByFirecrawl(t *testing.T) {
	s, err := Open(SourceFirecrawl)
	if err != nil {
		t.Fatalf("Open errored: %v", err)
	}
	if _, err := RequireInspector(s); err != nil {
		t.Errorf("RequireInspector errored: %v", err)
	}
	if _, err := RequireSimilarFinder(s); err != nil {
		t.Errorf("RequireSimilarFinder errored: %v", err)
	}
}

func TestCapabilityErrorIsProviderUnavailableAndNamesWhoCan(t *testing.T) {
	tests := []struct {
		name string
		call func(Source) error
	}{
		{
			name: "inspect",
			call: func(s Source) error { _, err := RequireInspector(s); return err },
		},
		{
			name: "similar",
			call: func(s Source) error { _, err := RequireSimilarFinder(s); return err },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(searchOnlySource{})
			var e *errs.E
			if !errors.As(err, &e) {
				t.Fatalf("error = %v, want an *errs.E", err)
			}
			// provider_unavailable, not invalid_args: the source name can come
			// from the config, so blaming the arguments would point the user
			// at a flag they never typed.
			if e.Code != errs.CodeProviderUnavailable {
				t.Errorf("code = %q, want provider_unavailable", e.Code)
			}
			if !strings.Contains(e.Hint, SourceFirecrawl) {
				t.Errorf("hint = %q, want it to name a source that can", e.Hint)
			}
		})
	}
}

func TestEffectiveLimitDefaultsAndCaps(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "zero means the default", in: 0, want: DefaultLimit},
		{name: "negative means the default", in: -5, want: DefaultLimit},
		{name: "a sane value passes through", in: 25, want: 25},
		{name: "an absurd value is capped locally", in: 100000, want: MaxLimit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (SearchOptions{Limit: tc.in}).EffectiveLimit(); got != tc.want {
				t.Errorf("SearchOptions.EffectiveLimit() = %d, want %d", got, tc.want)
			}
			if got := (SimilarOptions{Limit: tc.in}).EffectiveLimit(); got != tc.want {
				t.Errorf("SimilarOptions.EffectiveLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestValidRelation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "similar", in: "similar", want: true},
		{name: "citers", in: "citers", want: true},
		{name: "references", in: "references", want: true},
		{name: "case is folded", in: "Citers", want: true},
		{name: "space is trimmed", in: "  similar ", want: true},
		{name: "an invented relation is rejected", in: "cited-by", want: false},
		{name: "empty is rejected", in: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidRelation(tc.in); got != tc.want {
				t.Fatalf("ValidRelation(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEffectiveRelationDefaultsToSimilar(t *testing.T) {
	if got := (SimilarOptions{}).EffectiveRelation(); got != RelationSimilar {
		t.Fatalf("EffectiveRelation() = %q, want %q", got, RelationSimilar)
	}
}

func TestNormalizeID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "an arxiv id passes through", in: "arxiv:1706.03762", want: "arxiv:1706.03762"},
		{name: "the namespace is folded to lower case", in: "ArXiv:1706.03762", want: "arxiv:1706.03762"},
		{name: "a DOI keeps its slashes", in: "doi:10.1145/3442188", want: "doi:10.1145/3442188"},
		{name: "space is trimmed", in: "  pmid:18027780 ", want: "pmid:18027780"},
		// A bare number is as likely a PMID as an arXiv id, so guessing would
		// be wrong about half the time.
		{name: "a bare number is rejected as ambiguous", in: "1706.03762", wantErr: true},
		{name: "a namespace with no id is rejected", in: "arxiv:", wantErr: true},
		{name: "empty is rejected", in: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeID(tc.in)
			if tc.wantErr {
				var e *errs.E
				if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
					t.Fatalf("NormalizeID(%q) = %q, %v; want an invalid_args error", tc.in, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeID(%q) errored: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLandingURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "arxiv", in: "arxiv:1706.03762", want: "https://arxiv.org/abs/1706.03762"},
		{name: "doi", in: "doi:10.1145/3442188", want: "https://doi.org/10.1145/3442188"},
		{name: "pmid", in: "pmid:18027780", want: "https://pubmed.ncbi.nlm.nih.gov/18027780/"},
		{name: "pmcid", in: "pmcid:PMC1431743", want: "https://www.ncbi.nlm.nih.gov/pmc/articles/PMC1431743/"},
		// A guessed URL in cited output is worse than no URL.
		{name: "an unknown namespace yields nothing", in: "wat:123", want: ""},
		{name: "an unnamespaced id yields nothing", in: "12345", want: ""},
		{name: "empty yields nothing", in: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LandingURL(tc.in); got != tc.want {
				t.Fatalf("LandingURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPreferredIDPrefersTheDurableNamespace(t *testing.T) {
	tests := []struct {
		name string
		ids  map[string][]string
		want string
	}{
		{
			name: "DOI beats the index-specific ids",
			ids:  map[string][]string{"pmid": {"18027780"}, "doi": {"10.1/x"}},
			want: "doi:10.1/x",
		},
		{
			name: "arXiv beats PubMed when there is no DOI",
			ids:  map[string][]string{"pmid": {"1"}, "arxiv": {"1706.03762"}},
			want: "arxiv:1706.03762",
		},
		{
			name: "a lone PMID is still better than nothing",
			ids:  map[string][]string{"pmid": {"18027780"}},
			want: "pmid:18027780",
		},
		{name: "no ids yield nothing", ids: nil, want: ""},
		{name: "an empty value is skipped", ids: map[string][]string{"doi": {"  "}}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := preferredID(tc.ids); got != tc.want {
				t.Fatalf("preferredID = %q, want %q", got, tc.want)
			}
		})
	}
}

func newTestSource(t *testing.T, handler http.HandlerFunc) *firecrawlSource {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := &firecrawlSource{}
	s.SetBaseURL(srv.URL)
	return s
}

func TestSearchPapersMapsResults(t *testing.T) {
	var gotQuery string
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true,"results":[
			{"paperId":"1","primaryId":"arxiv:1706.03762","ids":{"arxiv":["1706.03762"]},
			 "title":"Attention Is All You Need","abstract":"We propose…","score":0.96}]}`))
	})

	papers, err := s.SearchPapers(context.Background(), SearchOptions{
		Query: "attention", Limit: 3, Categories: "cs.LG", From: "2017-01-01",
	})
	if err != nil {
		t.Fatalf("SearchPapers errored: %v", err)
	}
	for _, want := range []string{"query=attention", "k=3", "categories=cs.LG", "from=2017-01-01"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q, want it to contain %q", gotQuery, want)
		}
	}
	if len(papers) != 1 {
		t.Fatalf("got %d papers, want 1", len(papers))
	}
	if papers[0].URL != "https://arxiv.org/abs/1706.03762" {
		t.Errorf("URL = %q, want one derived from the primary id", papers[0].URL)
	}
}

func TestSearchPapersOmitsUnsetFilters(t *testing.T) {
	var gotQuery string
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true,"results":[]}`))
	})
	if _, err := s.SearchPapers(context.Background(), SearchOptions{Query: "x"}); err != nil {
		t.Fatalf("SearchPapers errored: %v", err)
	}
	// An empty filter must not be sent as authors=&categories=: the API reads
	// those as a filter matching nothing.
	for _, absent := range []string{"authors=", "categories=", "from=", "to="} {
		if strings.Contains(gotQuery, absent) {
			t.Errorf("query = %q, want %q omitted when unset", gotQuery, absent)
		}
	}
}

func TestSearchPapersReturnsEmptyNotNil(t *testing.T) {
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"results":[]}`))
	})
	papers, err := s.SearchPapers(context.Background(), SearchOptions{Query: "x"})
	if err != nil {
		t.Fatalf("SearchPapers errored: %v", err)
	}
	if papers == nil {
		t.Fatal("papers is nil, want an empty slice so it renders as [] rather than null")
	}
}

func TestSearchPapersRejectsAnEmptyQuery(t *testing.T) {
	s := &firecrawlSource{}
	_, err := s.SearchPapers(context.Background(), SearchOptions{Query: "   "})
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args before a request is made", err)
	}
}

func TestInspectPaperSendsKOnlyWithAQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantK   bool
		wantQry bool
	}{
		// The API rejects k without a query, so sending it unconditionally
		// would turn a plain lookup into a 400.
		{name: "no query sends neither", query: "", wantK: false, wantQry: false},
		{name: "a query sends both", query: "what is attention?", wantK: true, wantQry: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string
			s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				_, _ = w.Write([]byte(`{"paperId":"1","paper":{"paperId":"1","title":"T"},"passages":[]}`))
			})
			_, err := s.InspectPaper(context.Background(), "arxiv:1706.03762",
				InspectOptions{Query: tc.query, Limit: 4})
			if err != nil {
				t.Fatalf("InspectPaper errored: %v", err)
			}
			if strings.Contains(gotQuery, "k=") != tc.wantK {
				t.Errorf("query = %q, want k present = %v", gotQuery, tc.wantK)
			}
			if strings.Contains(gotQuery, "query=") != tc.wantQry {
				t.Errorf("query = %q, want query present = %v", gotQuery, tc.wantQry)
			}
		})
	}
}

func TestInspectPaperEscapesTheID(t *testing.T) {
	var gotPath string
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"paperId":"1","paper":{"title":"T"}}`))
	})
	// A DOI contains slashes; unescaped it would read as extra path segments
	// and hit a different route.
	if _, err := s.InspectPaper(context.Background(), "doi:10.1145/3442188", InspectOptions{}); err != nil {
		t.Fatalf("InspectPaper errored: %v", err)
	}
	if strings.Contains(gotPath, "10.1145/3442188") {
		t.Fatalf("path = %q, want the DOI's slash escaped", gotPath)
	}
}

func TestInspectPaperMissingIsNotFound(t *testing.T) {
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"paper":{}}`))
	})
	_, err := s.InspectPaper(context.Background(), "arxiv:0000.00000", InspectOptions{})
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeNotFound {
		t.Fatalf("error = %v, want not_found", err)
	}
}

func TestInspectPaperKeepsTheRequestedCitation(t *testing.T) {
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		// The index prefers a PMID; the caller asked by arXiv id.
		_, _ = w.Write([]byte(`{"paperId":"1","paper":{"paperId":"1","title":"T",
			"primaryId":"pmid:999","ids":{"pmid":["999"]}},"passages":[{"text":"p","score":0.5}]}`))
	})
	got, err := s.InspectPaper(context.Background(), "arxiv:1706.03762", InspectOptions{})
	if err != nil {
		t.Fatalf("InspectPaper errored: %v", err)
	}
	if got.Paper.PrimaryID != "arxiv:1706.03762" {
		t.Errorf("PrimaryID = %q, want the id that was asked for", got.Paper.PrimaryID)
	}
	if got.Paper.URL != "https://arxiv.org/abs/1706.03762" {
		t.Errorf("URL = %q, want it to match the requested id", got.Paper.URL)
	}
	if len(got.Passages) != 1 {
		t.Errorf("got %d passages, want 1", len(got.Passages))
	}
}

func TestSimilarPapersRequiresAnIntent(t *testing.T) {
	s := &firecrawlSource{}
	_, err := s.SimilarPapers(context.Background(), "arxiv:1706.03762", SimilarOptions{})
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args before a request is made", err)
	}
}

func TestSimilarPapersSendsTheRelationAsMode(t *testing.T) {
	var gotQuery, gotPath string
	s := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotPath = r.URL.RawQuery, r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"success":true,"results":[]}`))
	})
	_, err := s.SimilarPapers(context.Background(), "arxiv:1706.03762", SimilarOptions{
		Intent: "cheaper attention", Relation: RelationCiters, Limit: 5,
	})
	if err != nil {
		t.Fatalf("SimilarPapers errored: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/similar") {
		t.Errorf("path = %q, want the similar route", gotPath)
	}
	for _, want := range []string{"mode=citers", "k=5", "intent=cheaper+attention"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query = %q, want it to contain %q", gotQuery, want)
		}
	}
}

func TestToPapersSkipsEmptyRecords(t *testing.T) {
	got := toPapers([]paperRecord{
		{Title: "Real"},
		{},
		{Title: "  ", Abstract: "  "},
		{Abstract: "abstract only is still a record"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d papers, want 2 — blank records dropped, abstract-only kept", len(got))
	}
}
