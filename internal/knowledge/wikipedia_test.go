package knowledge

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

// newTestWikipedia points the client at a local server. Every test in this file
// goes through it: the suite must never reach the real API, both because CI has
// no network guarantee and because hammering Wikipedia from a test loop is
// exactly what its user-agent policy exists to stop.
func newTestWikipedia(t *testing.T, h http.HandlerFunc) *wikipedia {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	w := newWikipedia()
	w.baseURL = srv.URL
	return w
}

const summaryJSON = `{
  "type": "standard",
  "title": "Ada Lovelace",
  "displaytitle": "<span>Ada Lovelace</span>",
  "titles": {"canonical": "Ada_Lovelace", "normalized": "Ada Lovelace", "display": "Ada Lovelace"},
  "description": "English mathematician and writer (1815-1852)",
  "extract": "Augusta Ada King, Countess of Lovelace was an English mathematician.",
  "lang": "en",
  "thumbnail": {"source": "https://upload.wikimedia.org/thumb/Ada.jpg", "width": 240},
  "content_urls": {
    "desktop": {"page": "https://en.wikipedia.org/wiki/Ada_Lovelace"},
    "mobile": {"page": "https://en.m.wikipedia.org/wiki/Ada_Lovelace"}
  }
}`

func TestLookupParsesSummary(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/rest_v1/page/summary/") {
			t.Errorf("summary requested %q, want the REST summary path", r.URL.Path)
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(summaryJSON))
	})

	got, err := w.Lookup(context.Background(), "Ada Lovelace", "en")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Title != "Ada Lovelace" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Description != "English mathematician and writer (1815-1852)" {
		t.Fatalf("description = %q", got.Description)
	}
	if !strings.HasPrefix(got.Extract, "Augusta Ada King") {
		t.Fatalf("extract = %q", got.Extract)
	}
	if got.URL != "https://en.wikipedia.org/wiki/Ada_Lovelace" {
		t.Fatalf("url = %q, want content_urls.desktop.page", got.URL)
	}
	if got.ThumbnailURL != "https://upload.wikimedia.org/thumb/Ada.jpg" {
		t.Fatalf("thumbnail_url = %q, want thumbnail.source", got.ThumbnailURL)
	}
	if got.Lang != "en" {
		t.Fatalf("lang = %q", got.Lang)
	}
	if got.Disambiguation {
		t.Fatal("a standard page must not be marked as a disambiguation")
	}
}

// TestLookupAliasesRecordRedirect pins the redirect case: the requested title
// differs from the resolved one and must survive in Aliases.
func TestLookupAliasesRecordRedirect(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"type":"standard","title":"John F. Kennedy",
			"titles":{"canonical":"John_F._Kennedy","normalized":"John F. Kennedy"},"lang":"en"}`))
	})

	got, err := w.Lookup(context.Background(), "JFK", "en")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "JFK" {
		t.Fatalf("aliases = %v, want the requested title", got.Aliases)
	}
	// No content_urls in this payload: the URL must still be usable.
	if !strings.HasSuffix(got.URL, "/wiki/John_F._Kennedy") {
		t.Fatalf("url = %q, want a derived article URL", got.URL)
	}
}

func TestLookupDisambiguation(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"type":"disambiguation","title":"Mercury",
			"extract":"Mercury may refer to:","lang":"en"}`))
	})

	got, err := w.Lookup(context.Background(), "Mercury", "en")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !got.Disambiguation {
		t.Fatal("type=disambiguation must set Disambiguation")
	}
}

func TestLookupNotFound(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
		_, _ = rw.Write([]byte(`{"type":"https://mediawiki.org/wiki/HyperSwitch/errors/not_found","title":"Not found.","detail":"Page or revision not found."}`))
	})

	_, err := w.Lookup(context.Background(), "Nichtvorhanden", "de")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeNotFound {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeNotFound)
	}
	if !strings.Contains(e.Hint, "kb search") {
		t.Fatalf("hint %q must point at `text kb search`", e.Hint)
	}
}

func TestLookupRateLimited(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusTooManyRequests)
		_, _ = rw.Write([]byte(`{"detail":"Too many requests."}`))
	})

	_, err := w.Lookup(context.Background(), "Ada Lovelace", "en")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeRateLimited {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeRateLimited)
	}
	if !e.Retriable || e.RetryAfterSec != 5 {
		t.Fatalf("retry = (%v, %d), want retriable after 5s", e.Retriable, e.RetryAfterSec)
	}
}

func TestLookupServerError(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte("upstream connect error"))
	})

	_, err := w.Lookup(context.Background(), "Ada Lovelace", "en")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeAPI5xx {
		t.Fatalf("error = %v, want api_5xx", err)
	}
}

// TestLookupEscapesTitle is the umlaut case. "Große Koalition" must arrive as
// one percent-escaped path segment with an underscore for the space — a raw
// space would be a malformed request and a UTF-8 byte would 400.
func TestLookupEscapesTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"umlaut and space", "Große Koalition", "/api/rest_v1/page/summary/Gro%C3%9Fe_Koalition"},
		{"already underscored", "Ada_Lovelace", "/api/rest_v1/page/summary/Ada_Lovelace"},
		{"slash stays in the segment", "AC/DC", "/api/rest_v1/page/summary/AC%2FDC"},
		{"collapses inner whitespace", "  Ada   Lovelace ", "/api/rest_v1/page/summary/Ada_Lovelace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			w := newTestWikipedia(t, func(rw http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.EscapedPath()
				_, _ = rw.Write([]byte(`{"type":"standard","title":"x","lang":"de"}`))
			})
			if _, err := w.Lookup(context.Background(), tc.title, "de"); err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if gotPath != tc.want {
				t.Fatalf("path = %q, want %q", gotPath, tc.want)
			}
		})
	}
}

func TestUserAgentIsSent(t *testing.T) {
	var ua, apiUA string
	w := newTestWikipedia(t, func(rw http.ResponseWriter, r *http.Request) {
		ua, apiUA = r.Header.Get("User-Agent"), r.Header.Get("Api-User-Agent")
		_, _ = rw.Write([]byte(`{"type":"standard","title":"x","lang":"en"}`))
	})
	if _, err := w.Lookup(context.Background(), "x", "en"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ua != userAgent || apiUA != userAgent {
		t.Fatalf("User-Agent = %q, Api-User-Agent = %q, want %q on both", ua, apiUA, userAgent)
	}
	if !strings.Contains(ua, "github.com/KLIXPERT-io/text-cli") {
		t.Fatalf("User-Agent %q must identify the client per Wikimedia policy", ua)
	}
}

func TestSearchParsesResults(t *testing.T) {
	var gotQuery string
	w := newTestWikipedia(t, func(rw http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = rw.Write([]byte(`{"query":{"search":[
			{"title":"Analytical Engine","snippet":"The <span class=\"searchmatch\">Analytical</span> Engine was a machine &amp; a design.","score":1.5},
			{"title":"Difference engine","snippet":"A mechanical calculator."}
		]}}`))
	})

	hits, err := w.Search(context.Background(), "analytical engine", "en", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Title != "Analytical Engine" {
		t.Fatalf("title = %q", hits[0].Title)
	}
	if hits[0].Description != "The Analytical Engine was a machine & a design." {
		t.Fatalf("description = %q, want the snippet as plain text", hits[0].Description)
	}
	if hits[0].Score != 1.5 {
		t.Fatalf("score = %v", hits[0].Score)
	}
	if !strings.HasSuffix(hits[0].URL, "/wiki/Analytical_Engine") {
		t.Fatalf("url = %q", hits[0].URL)
	}
	for _, want := range []string{"action=query", "list=search", "srsearch=analytical+engine", "srlimit=2", "format=json"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query %q is missing %q", gotQuery, want)
		}
	}
}

func TestSearchLimitDefaultsAndClamps(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{"zero defaults", 0, "srlimit=10"},
		{"negative defaults", -3, "srlimit=10"},
		{"clamped to the max", 5000, "srlimit=50"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string
			w := newTestWikipedia(t, func(rw http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				_, _ = rw.Write([]byte(`{"query":{"search":[]}}`))
			})
			if _, err := w.Search(context.Background(), "q", "en", tc.limit); err != nil {
				t.Fatalf("Search: %v", err)
			}
			if !strings.Contains(gotQuery, tc.want) {
				t.Fatalf("query %q, want %q", gotQuery, tc.want)
			}
		})
	}
}

// TestSearchAPIErrorIsNotZeroResults: the Action API answers errors with HTTP
// 200 and an error object, so a naive client reports "no hits" for a rejected
// query.
func TestSearchAPIErrorIsNotZeroResults(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		_, _ = rw.Write([]byte(`{"error":{"code":"nosrsearch","info":"The srsearch parameter must be set."}}`))
	})
	_, err := w.Search(context.Background(), "q", "en", 5)
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("error = %v, want invalid_args", err)
	}
	if !strings.Contains(e.Message, "nosrsearch") {
		t.Fatalf("message %q must carry the API's error code", e.Message)
	}
}

func TestEmptyTitleAndQueryAreInvalidArgs(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be made for empty input")
	})
	var e *errs.E
	if _, err := w.Lookup(context.Background(), "   ", "en"); !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("Lookup(empty) = %v, want invalid_args", err)
	}
	if _, err := w.Search(context.Background(), " ", "en", 5); !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
		t.Fatalf("Search(empty) = %v, want invalid_args", err)
	}
}

func TestTimeoutBecomesNetworkUnreachable(t *testing.T) {
	w := newTestWikipedia(t, func(rw http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = rw.Write([]byte(`{}`))
	})
	w.SetTimeout(20 * time.Millisecond)

	_, err := w.Lookup(context.Background(), "Ada Lovelace", "en")
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T, want *errs.E", err)
	}
	if e.Code != errs.CodeNetworkUnreachable {
		t.Fatalf("code = %q, want %q", e.Code, errs.CodeNetworkUnreachable)
	}
	if !e.Retriable {
		t.Fatal("a timeout must be retriable")
	}
}

func TestSetTimeoutZeroRestoresDefault(t *testing.T) {
	w := newWikipedia()
	w.SetTimeout(0)
	if w.timeout != DefaultTimeout || w.client.Timeout != DefaultTimeout {
		t.Fatalf("timeout = %v / %v, want %v", w.timeout, w.client.Timeout, DefaultTimeout)
	}
	w.SetTimeout(2 * time.Second)
	if w.timeout != 2*time.Second || w.client.Timeout != 2*time.Second {
		t.Fatalf("SetTimeout did not apply: %v / %v", w.timeout, w.client.Timeout)
	}
}

// TestEndpointNeverEmitsAutoSubdomain is the whole reason NormalizeLang exists:
// there is no auto.wikipedia.org, and a literal one is a DNS error the user has
// to decode.
func TestEndpointNeverEmitsAutoSubdomain(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "https://en.wikipedia.org"},
		{"auto", "https://en.wikipedia.org"},
		{"AUTO", "https://en.wikipedia.org"},
		{"en", "https://en.wikipedia.org"},
		{"de", "https://de.wikipedia.org"},
		{"de-AT", "https://de.wikipedia.org"},
		{"../evil", "https://en.wikipedia.org"},
	}
	w := newWikipedia()
	for _, tc := range tests {
		if got := w.endpoint(tc.in); got != tc.want {
			t.Fatalf("endpoint(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTranslateHTTP(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantCode  errs.Code
		wantRetry bool
		wantIn    string
	}{
		{"404 is not_found", 404, `{"detail":"Page or revision not found."}`, errs.CodeNotFound, false, "Ada Lovelace"},
		{"404 without a body", 404, "", errs.CodeNotFound, false, "Ada Lovelace"},
		{"429 is rate_limited", 429, "", errs.CodeRateLimited, true, "rate limited"},
		{"403 is auth_denied", 403, "blocked", errs.CodeAuthDenied, false, "refused"},
		{"500 is api_5xx", 500, "", errs.CodeAPI5xx, true, "500"},
		{"503 is api_5xx", 503, "", errs.CodeAPI5xx, true, "503"},
		{"400 is invalid_args", 400, `{"detail":"invalid title"}`, errs.CodeInvalidArgs, false, "invalid title"},
		{"action API error info", 400, `{"error":{"code":"x","info":"srsearch is required"}}`, errs.CodeInvalidArgs, false, "srsearch is required"},
		{"an HTML error page is not echoed", 500, "<html><body>Gateway</body></html>", errs.CodeAPI5xx, true, "500"},
		// The REST API answers an ordinary miss with a scary-looking
		// {"status":404,"type":"Internal error"}; echoing that would tell the
		// user their typo was a server crash.
		{"a JSON body with no message is not echoed", 404, `{"status":404,"type":"Internal error"}`, errs.CodeNotFound, false, "Ada Lovelace"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := translateHTTP(tc.status, tc.body, "Ada Lovelace")
			var e *errs.E
			if !errors.As(err, &e) {
				t.Fatalf("error is %T, want *errs.E", err)
			}
			if e.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", e.Code, tc.wantCode)
			}
			if e.Retriable != tc.wantRetry {
				t.Fatalf("retriable = %v, want %v", e.Retriable, tc.wantRetry)
			}
			if tc.wantIn != "" && !strings.Contains(e.Message, tc.wantIn) {
				t.Fatalf("message %q does not contain %q", e.Message, tc.wantIn)
			}
			if strings.Contains(e.Message, "<html>") || strings.Contains(e.Message, "Internal error") {
				t.Fatalf("message %q leaks a raw error body", e.Message)
			}
		})
	}
	if err := translateHTTP(200, "", "x"); err != nil {
		t.Fatalf("translateHTTP(200) = %v, want nil", err)
	}
}

func TestPlainText(t *testing.T) {
	in := `The <span class="searchmatch">Analytical</span> Engine &amp;   the  <b>mill</b>`
	if got, want := plainText(in), "The Analytical Engine & the mill"; got != want {
		t.Fatalf("plainText = %q, want %q", got, want)
	}
}
