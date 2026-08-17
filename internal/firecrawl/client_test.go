package firecrawl

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

func TestTranslateMapsStatusToCode(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		hadKey    bool
		want      errs.Code
		retriable bool
		wantMsg   string
	}{
		{
			name:   "401 without a key blames the missing credential",
			status: http.StatusUnauthorized,
			body:   `{"success":false,"error":"Unauthorized"}`,
			hadKey: false,
			want:   errs.CodeAuthMissing,
		},
		{
			name:   "401 with a key blames the credential that was sent",
			status: http.StatusUnauthorized,
			body:   `{"success":false,"error":"Unauthorized: Invalid token"}`,
			hadKey: true,
			want:   errs.CodeAuthDenied,
		},
		{
			name:   "403 is treated as a rejected credential",
			status: http.StatusForbidden,
			body:   `{"success":false,"error":"Forbidden"}`,
			hadKey: true,
			want:   errs.CodeAuthDenied,
		},
		{
			name:   "402 is out of credits, not a rate limit",
			status: http.StatusPaymentRequired,
			body:   `{"success":false,"error":"Insufficient credits"}`,
			hadKey: true,
			want:   errs.CodeQuotaExceeded,
		},
		{
			name:      "429 is retriable",
			status:    http.StatusTooManyRequests,
			body:      `{"success":false,"error":"Rate limit exceeded"}`,
			hadKey:    true,
			want:      errs.CodeRateLimited,
			retriable: true,
		},
		{
			name:   "404 is not found",
			status: http.StatusNotFound,
			body:   `{"success":false,"error":"Not found"}`,
			hadKey: true,
			want:   errs.CodeNotFound,
		},
		{
			name:      "504 is a gateway timeout, reported as unreachable",
			status:    http.StatusGatewayTimeout,
			body:      `{"success":false,"error":"Timeout"}`,
			hadKey:    true,
			want:      errs.CodeNetworkUnreachable,
			retriable: true,
		},
		{
			name:      "500 is a server error and retriable",
			status:    http.StatusInternalServerError,
			body:      `{"success":false,"error":"boom"}`,
			hadKey:    true,
			want:      errs.CodeAPI5xx,
			retriable: true,
		},
		{
			name:    "400 details name the offending parameter",
			status:  http.StatusBadRequest,
			body:    `{"success":false,"error":"Invalid query parameters","details":[{"code":"custom","path":["k"],"message":"k is only valid when query is present"}]}`,
			hadKey:  true,
			want:    errs.CodeInvalidArgs,
			wantMsg: "k: k is only valid when query is present",
		},
		{
			name:      "a non-JSON body still yields a usable message",
			status:    http.StatusBadGateway,
			body:      "<html>bad gateway</html>",
			hadKey:    true,
			want:      errs.CodeAPI5xx,
			retriable: true,
			wantMsg:   "bad gateway",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := translate(tc.status, []byte(tc.body), "", tc.hadKey)
			if e.Code != tc.want {
				t.Fatalf("code = %q, want %q", e.Code, tc.want)
			}
			if e.Retriable != tc.retriable {
				t.Errorf("retriable = %v, want %v", e.Retriable, tc.retriable)
			}
			if tc.wantMsg != "" && !strings.Contains(e.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", e.Message, tc.wantMsg)
			}
		})
	}
}

func TestTranslateTruncatesAnOversizedBody(t *testing.T) {
	e := translate(http.StatusBadGateway, []byte(strings.Repeat("x", 5000)), "", true)
	if len(e.Message) > 400 {
		t.Fatalf("message length = %d, want it truncated", len(e.Message))
	}
	if !strings.HasSuffix(e.Message, "…") {
		t.Errorf("message = %q, want a truncation marker", e.Message)
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "empty is unknown", value: "", want: 0},
		{name: "seconds are read directly", value: "30", want: 30},
		{name: "zero seconds stays zero", value: "0", want: 0},
		{name: "garbage degrades to unknown", value: "soon", want: 0},
		{name: "a past date is not a negative delay", value: "Mon, 02 Jan 2006 15:04:05 GMT", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.value); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseRetryAfterReadsAnHTTPDate(t *testing.T) {
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	// A second of slack: the header has one-second resolution and the clock
	// moves between formatting and parsing.
	if got < 85 || got > 91 {
		t.Fatalf("parseRetryAfter(%q) = %d, want about 90", future, got)
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "an https URL passes through", in: "https://example.com/a", want: "https://example.com/a"},
		{name: "http is allowed", in: "http://example.com", want: "http://example.com"},
		{name: "a bare host gains https", in: "example.com/post", want: "https://example.com/post"},
		{name: "surrounding space is trimmed", in: "  https://example.com  ", want: "https://example.com"},
		{name: "empty is rejected", in: "", wantErr: true},
		{name: "a file scheme is rejected", in: "file:///etc/passwd", wantErr: true},
		{name: "a scheme with no host is rejected", in: "https://", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateURL(%q) = %q, want an error", tc.in, got)
				}
				var e *errs.E
				if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgs {
					t.Fatalf("error = %v, want an invalid_args *errs.E", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateURL(%q) errored: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ValidateURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveKeyPrefersTheEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		configured string
		want       string
	}{
		{name: "the environment wins", env: "fc-env", configured: "fc-cfg", want: "fc-env"},
		{name: "config is the fallback", env: "", configured: "fc-cfg", want: "fc-cfg"},
		{name: "neither yields empty", env: "", configured: "", want: ""},
		{name: "whitespace is not a key", env: "   ", configured: "fc-cfg", want: "fc-cfg"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvAPIKey, tc.env)
			if got := ResolveKey(tc.configured); got != tc.want {
				t.Fatalf("ResolveKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequireKey(t *testing.T) {
	if err := RequireKey("fc-abc"); err != nil {
		t.Fatalf("RequireKey with a key errored: %v", err)
	}
	err := RequireKey("  ")
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeAuthMissing {
		t.Fatalf("RequireKey(\"\") = %v, want auth_missing", err)
	}
}

func TestClientSendsBearerAndDecodes(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"value":42}`))
	}))
	defer srv.Close()

	c := New("fc-test", srv.URL)
	var out struct {
		Success bool `json:"success"`
		Value   int  `json:"value"`
	}
	q := map[string][]string{"query": {"a b"}}
	if err := c.Get(context.Background(), "/v2/thing", q, &out); err != nil {
		t.Fatalf("Get errored: %v", err)
	}
	if gotAuth != "Bearer fc-test" {
		t.Errorf("Authorization = %q, want a bearer token", gotAuth)
	}
	if gotPath != "/v2/thing" {
		t.Errorf("path = %q, want /v2/thing", gotPath)
	}
	if gotQuery != "query=a+b" {
		t.Errorf("query = %q, want the value encoded", gotQuery)
	}
	if !out.Success || out.Value != 42 {
		t.Errorf("decoded = %+v, want the body", out)
	}
}

func TestClientOmitsAuthorizationWithoutAKey(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	// The research endpoints answer anonymously; sending an empty bearer token
	// would be rejected where sending nothing is accepted.
	if err := New("", srv.URL).Get(context.Background(), "/v2/thing", nil, nil); err != nil {
		t.Fatalf("Get errored: %v", err)
	}
	if hadAuth {
		t.Fatal("an Authorization header was sent without a key")
	}
}

func TestClientTranslatesAnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"success":false,"error":"slow down"}`))
	}))
	defer srv.Close()

	err := New("fc-test", srv.URL).Post(context.Background(), "/v2/scrape", map[string]string{"url": "x"}, nil)
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *errs.E", err)
	}
	if e.Code != errs.CodeRateLimited {
		t.Errorf("code = %q, want rate_limited", e.Code)
	}
	if e.RetryAfterSec != 12 {
		t.Errorf("retry_after_sec = %d, want 12", e.RetryAfterSec)
	}
}

func TestClientTimeoutIsReportedAsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	c := New("fc-test", srv.URL)
	c.Timeout = 20 * time.Millisecond
	err := c.Get(context.Background(), "/v2/thing", nil, nil)
	var e *errs.E
	if !errors.As(err, &e) || e.Code != errs.CodeNetworkUnreachable {
		t.Fatalf("error = %v, want network_unreachable", err)
	}
}

func TestSetTimeoutRejectsUnbounded(t *testing.T) {
	c := New("k", "")
	c.SetTimeout(0)
	if c.Timeout != DefaultTimeout {
		t.Fatalf("Timeout = %v, want the default rather than no deadline", c.Timeout)
	}
}

func TestNewDefaultsTheBaseURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty uses the public API", in: "", want: DefaultBaseURL},
		{name: "a trailing slash is trimmed", in: "http://localhost:3002/", want: "http://localhost:3002"},
		{name: "a self-hosted URL is kept", in: "http://localhost:3002", want: "http://localhost:3002"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := New("k", tc.in).BaseURL; got != tc.want {
				t.Fatalf("BaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}
