package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// stubFetcher is a Fetcher that is not the Firecrawl one, used to prove the
// registry has no special knowledge of any particular backend.
type stubFetcher struct{ name string }

func (s stubFetcher) Name() string { return s.name }
func (s stubFetcher) Fetch(context.Context, string, Options) (*Page, error) {
	return &Page{Content: "stub", Fetcher: s.name}, nil
}

func TestRegistryResolvesTheFirecrawlBackend(t *testing.T) {
	// The init in firecrawl.go is the only registration in a normal build, so
	// this pins that "register, don't wire" actually reached the registry.
	if !Registered(FetcherFirecrawl) {
		t.Fatalf("Names() = %v, want the firecrawl backend registered by its init", Names())
	}
	f, err := Open(FetcherFirecrawl)
	if err != nil {
		t.Fatalf("Open(%q) errored: %v", FetcherFirecrawl, err)
	}
	if f.Name() != FetcherFirecrawl {
		t.Errorf("Name() = %q, want %q", f.Name(), FetcherFirecrawl)
	}
}

func TestOpenUnknownNamesTheKnownOnes(t *testing.T) {
	_, err := Open("nope")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *errs.E", err)
	}
	// invalid_args, not provider_unavailable: a name that does not exist is a
	// typo, which is a different fix from a backend that cannot do the job.
	if e.Code != errs.CodeInvalidArgs {
		t.Errorf("code = %q, want invalid_args", e.Code)
	}
	if !strings.Contains(e.Hint, FetcherFirecrawl) {
		t.Errorf("hint = %q, want it to name the registered fetchers", e.Hint)
	}
}

func TestOpenEmptyNameUsesTheDefault(t *testing.T) {
	f, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") errored: %v", err)
	}
	if f.Name() != Default() {
		t.Fatalf("Open(\"\").Name() = %q, want the default %q", f.Name(), Default())
	}
}

func TestRegisterRejectsADuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register of a duplicate name did not panic")
		}
	}()
	Register(FetcherFirecrawl, func() (Fetcher, error) { return stubFetcher{FetcherFirecrawl}, nil })
}

func TestRegisterRejectsAnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register of an empty name did not panic")
		}
	}()
	Register("  ", func() (Fetcher, error) { return stubFetcher{}, nil })
}

func TestOptionsDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want time.Duration
	}{
		{name: "zero means the default", in: Options{}, want: DefaultTimeout},
		{name: "a negative value means the default", in: Options{Timeout: -1}, want: DefaultTimeout},
		{name: "a set value is honoured", in: Options{Timeout: 5 * time.Second}, want: 5 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.EffectiveTimeout(); got != tc.want {
				t.Fatalf("EffectiveTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFlexStringDecodesEitherShape(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "a plain string", json: `"Example Domain"`, want: "Example Domain"},
		{name: "null becomes empty", json: `null`, want: ""},
		{name: "duplicate meta tags arrive as an array", json: `["first","second"]`, want: "first"},
		{name: "an array skips leading blanks", json: `["","real"]`, want: "real"},
		{name: "an empty array is empty", json: `[]`, want: ""},
		{name: "an unexpected shape degrades rather than failing", json: `{"a":1}`, want: ""},
		{name: "a number degrades rather than failing", json: `42`, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var s flexString
			if err := s.UnmarshalJSON([]byte(tc.json)); err != nil {
				t.Fatalf("UnmarshalJSON(%s) errored: %v", tc.json, err)
			}
			if string(s) != tc.want {
				t.Fatalf("= %q, want %q", s, tc.want)
			}
		})
	}
}

func TestNormalizeLang(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a base code passes through", in: "en", want: "en"},
		{name: "a region tag is cut", in: "en-GB", want: "en"},
		{name: "an underscore tag is cut", in: "de_AT", want: "de"},
		{name: "case is folded", in: "DE", want: "de"},
		{name: "empty stays empty", in: "", want: ""},
		{name: "a non-language value is dropped", in: "x-default", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLang(tc.in); got != tc.want {
				t.Fatalf("normalizeLang(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// newTestFetcher points the Firecrawl backend at a local server.
func newTestFetcher(t *testing.T, handler http.HandlerFunc) (*firecrawlFetcher, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	f := &firecrawlFetcher{}
	f.SetAPIKey("fc-test")
	f.SetBaseURL(srv.URL)
	return f, srv
}

func TestFirecrawlFetchMapsAScrape(t *testing.T) {
	var gotBody string
	f, _ := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		if r.URL.Path != "/v2/scrape" {
			t.Errorf("path = %q, want /v2/scrape", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{
			"markdown":"# Title\n\nBody text.",
			"links":["https://example.com/a"],
			"metadata":{"title":"T","description":"D","language":"en-GB",
			"sourceURL":"https://example.com","url":"https://example.com/final",
			"statusCode":200,"creditsUsed":1}}}`))
	})

	page, err := f.Fetch(context.Background(), "https://example.com", Options{MainContentOnly: true, IncludeLinks: true})
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if !strings.Contains(gotBody, `"formats":["markdown","links"]`) {
		t.Errorf("request body = %s, want links requested alongside markdown", gotBody)
	}
	if page.Content != "# Title\n\nBody text." {
		t.Errorf("Content = %q, want the markdown verbatim", page.Content)
	}
	// The redirect target is the page that was read; the requested URL is kept
	// separately rather than overwritten.
	if page.URL != "https://example.com/final" {
		t.Errorf("URL = %q, want the final URL", page.URL)
	}
	if page.RequestedURL != "https://example.com" {
		t.Errorf("RequestedURL = %q, want the URL that was asked for", page.RequestedURL)
	}
	if page.Language != "en" {
		t.Errorf("Language = %q, want the region tag stripped", page.Language)
	}
	if page.Fetcher != FetcherFirecrawl || page.Credits != 1 || page.StatusCode != 200 {
		t.Errorf("provenance = %+v, want the backend, credits, and status carried through", page)
	}
}

func TestFirecrawlFetchOmitsRequestedURLWhenUnchanged(t *testing.T) {
	f, _ := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"text",
			"metadata":{"url":"https://example.com","statusCode":200}}}`))
	})
	page, err := f.Fetch(context.Background(), "https://example.com", Options{})
	if err != nil {
		t.Fatalf("Fetch errored: %v", err)
	}
	if page.RequestedURL != "" {
		t.Fatalf("RequestedURL = %q, want it omitted when it equals URL", page.RequestedURL)
	}
}

func TestFirecrawlFetchEmptyPageIsEmptyInput(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		wantHint string
	}{
		{
			name:     "main-content extraction is the first thing to suspect",
			opts:     Options{MainContentOnly: true},
			wantHint: "--no-main-content",
		},
		{
			name:     "without it, the page really has no text",
			opts:     Options{},
			wantHint: "login-walled",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"   ","metadata":{"statusCode":200}}}`))
			})
			_, err := f.Fetch(context.Background(), "https://example.com", tc.opts)
			var e *errs.E
			if !errors.As(err, &e) || e.Code != errs.CodeEmptyInput {
				t.Fatalf("error = %v, want empty_input rather than a silent empty document", err)
			}
			if !strings.Contains(e.Hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to mention %q", e.Hint, tc.wantHint)
			}
		})
	}
}

func TestFirecrawlFetchRequiresAKey(t *testing.T) {
	f := &firecrawlFetcher{}
	_, err := f.Fetch(context.Background(), "https://example.com", Options{})
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeAuthMissing {
		t.Fatalf("error = %v, want auth_missing before any request is made", err)
	}
}

func TestFirecrawlFetchValidatesTheURLBeforeSpendingACall(t *testing.T) {
	called := false
	f, _ := newTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"x"}}`))
	})
	_, err := f.Fetch(context.Background(), "file:///etc/passwd", Options{})
	if err == nil {
		t.Fatal("Fetch of a file:// URL succeeded, want invalid_args")
	}
	if called {
		t.Error("a request was made for an unfetchable scheme")
	}
}
