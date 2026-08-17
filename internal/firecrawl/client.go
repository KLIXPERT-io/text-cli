// Package firecrawl is the shared transport for every Firecrawl-backed feature.
//
// It exists so the two capabilities built on Firecrawl — the page fetcher in
// internal/fetch and the paper search in internal/research — do not each grow
// their own HTTP client, their own key resolution, and their own idea of what a
// 429 means. Those two packages own their request and response shapes; this one
// owns the wire.
//
// It deliberately knows nothing about pages or papers. Adding a third Firecrawl
// capability should mean a new package with a Register call, not a change here.
package firecrawl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

const (
	// DefaultBaseURL is the public API. It is overridable so a self-hosted
	// Firecrawl — the project ships one — is a config key rather than a fork.
	DefaultBaseURL = "https://api.firecrawl.dev"

	// DefaultTimeout bounds a single call. It is far longer than the other
	// backends in this repo because a scrape really does render a page: a slow
	// site behind the "auto" proxy retry can take the better part of a minute,
	// and a 15s deadline would turn working pages into timeouts.
	DefaultTimeout = 60 * time.Second

	// EnvAPIKey is the environment variable the key is read from. It is
	// Firecrawl's own documented name, so a shell already set up for their SDK
	// needs no second export.
	EnvAPIKey = "FIRECRAWL_API_KEY"
)

// Client is a Firecrawl HTTP client. The zero value is not usable; call New.
//
// It is safe for concurrent use: net/http.Client is, and nothing here mutates
// the struct after construction.
type Client struct {
	// APIKey is sent as a bearer token. Empty is allowed — the research
	// endpoints answer anonymously — and only the callers that need a key say
	// so, via RequireKey.
	APIKey string
	// BaseURL has no trailing slash.
	BaseURL string
	Timeout time.Duration
	HTTP    *http.Client
}

// New builds a client. Empty arguments fall back to the defaults, so a caller
// that has nothing to say passes nothing.
func New(apiKey, baseURL string) *Client {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		APIKey:  strings.TrimSpace(apiKey),
		BaseURL: base,
		Timeout: DefaultTimeout,
		HTTP:    &http.Client{},
	}
}

// SetTimeout bounds one call. Zero or negative restores the default rather than
// meaning "no deadline": an unbounded scrape is a hung CLI.
func (c *Client) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultTimeout
	}
	c.Timeout = d
}

// ResolveKey applies the credential precedence: $FIRECRAWL_API_KEY, then the
// config file.
//
// The environment beats the config file on purpose: a config value is a durable
// default a user set once, while an exported variable is what CI and a
// throwaway shell use to override it for one run.
//
// There is deliberately no --api-key flag feeding this. A credential on the
// command line lands in shell history and in the process list, and both of the
// layers here are strictly better places to put one.
func ResolveKey(configured string) string {
	if v := strings.TrimSpace(os.Getenv(EnvAPIKey)); v != "" {
		return v
	}
	return strings.TrimSpace(configured)
}

// RequireKey turns a missing credential into the error the CLI documents,
// before any request is made.
//
// It is a separate call rather than a check inside do() because not every
// Firecrawl endpoint needs a key: the research paper search answers
// unauthenticated requests, and failing those for a missing key would be a
// wrong error about a call that would have worked.
func RequireKey(key string) error {
	if strings.TrimSpace(key) != "" {
		return nil
	}
	return errs.New(errs.CodeAuthMissing, "no Firecrawl API key").
		WithHint("Set one with `text config set firecrawl.api_key <key>` or export " +
			EnvAPIKey + ". Keys come from https://firecrawl.dev/app/api-keys.")
}

// Get performs a GET and decodes the JSON body into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// Post performs a POST with a JSON body and decodes the JSON response into out.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return errs.Newf(errs.CodeGeneric, "encode request: %s", err.Error())
		}
		payload = bytes.NewReader(b)
	}

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, u, payload)
	if err != nil {
		return errs.Newf(errs.CodeInvalidArgs, "build request: %s", err.Error())
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// A cancelled parent context is the user pressing Ctrl-C, not an
		// unreachable network; reporting it as the latter would send them
		// debugging their DNS.
		if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
			return errs.New(errs.CodeGeneric, "request cancelled")
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return errs.Newf(errs.CodeNetworkUnreachable, "Firecrawl request timed out after %s", timeout).
				WithHint("Raise the deadline with --timeout, e.g. --timeout 120s.").
				WithRetry(0)
		}
		return errs.Newf(errs.CodeNetworkUnreachable, "reach Firecrawl: %s", err.Error()).WithRetry(0)
	}
	defer resp.Body.Close()

	// Capped: an error page from a proxy in front of the API can be a whole
	// HTML document, and none of it belongs in an error message.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return errs.Newf(errs.CodeNetworkUnreachable, "read Firecrawl response: %s", err.Error()).WithRetry(0)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return translate(resp.StatusCode, raw, resp.Header.Get("Retry-After"), c.APIKey != "")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errs.Newf(errs.CodeGeneric, "decode Firecrawl response: %s", err.Error()).
			WithHint("This usually means the API changed shape. Re-run with --verbose and report it.")
	}
	return nil
}

// apiError is Firecrawl's error envelope. Every field is optional: a 502 from
// the edge is not JSON at all, and the decode is best-effort by design.
type apiError struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details []struct {
		Message string   `json:"message"`
		Path    []string `json:"path"`
	} `json:"details"`
}

// message renders the most specific description available. The details array
// is what names the offending parameter ("k is only valid when query is
// present"), so it wins over the generic "Invalid query parameters".
func (e apiError) message() string {
	var parts []string
	for _, d := range e.Details {
		if d.Message == "" {
			continue
		}
		if len(d.Path) > 0 {
			parts = append(parts, strings.Join(d.Path, ".")+": "+d.Message)
			continue
		}
		parts = append(parts, d.Message)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; ")
	}
	return strings.TrimSpace(e.Error)
}

// translate maps an HTTP failure onto the CLI's structured error codes.
//
// The mapping is what an agent branches on, so the distinctions matter more
// than the prose: 402 is "you ran out of credits" and is not retriable, while
// 429 is "you went too fast" and is. A 401 splits on whether a key was sent at
// all — auth_missing tells the user to set one, auth_denied tells them the one
// they set is wrong, and those are different fixes.
func translate(status int, raw []byte, retryAfter string, hadKey bool) *errs.E {
	var api apiError
	_ = json.Unmarshal(raw, &api)
	msg := api.message()
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	// A non-JSON body is an infrastructure page, not an API message. Truncate
	// it: the status code carries the information, the HTML does not.
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		if !hadKey {
			return errs.Newf(errs.CodeAuthMissing, "Firecrawl rejected an unauthenticated request: %s", msg).
				WithHint("Set a key with `text config set firecrawl.api_key <key>` or export " + EnvAPIKey + ".")
		}
		return errs.Newf(errs.CodeAuthDenied, "Firecrawl rejected the API key: %s", msg).
			WithHint("Check the key at https://firecrawl.dev/app/api-keys.")

	case status == http.StatusPaymentRequired:
		return errs.Newf(errs.CodeQuotaExceeded, "Firecrawl credits exhausted: %s", msg).
			WithHint("Top up or change plan at https://firecrawl.dev/app/usage.")

	case status == http.StatusTooManyRequests:
		e := errs.Newf(errs.CodeRateLimited, "Firecrawl rate limit: %s", msg).
			WithHint("Retry after the reported delay, or lower concurrency.")
		return e.WithRetry(parseRetryAfter(retryAfter))

	case status == http.StatusNotFound:
		return errs.Newf(errs.CodeNotFound, "Firecrawl: %s", msg)

	case status == http.StatusRequestTimeout, status == http.StatusGatewayTimeout:
		return errs.Newf(errs.CodeNetworkUnreachable, "Firecrawl timed out: %s", msg).
			WithHint("Raise the deadline with --timeout.").
			WithRetry(0)

	case status >= 500:
		return errs.Newf(errs.CodeAPI5xx, "Firecrawl server error (%d): %s", status, msg).WithRetry(0)

	default:
		return errs.Newf(errs.CodeInvalidArgs, "Firecrawl rejected the request (%d): %s", status, msg)
	}
}

// parseRetryAfter reads the Retry-After header, which is either a delay in
// seconds or an HTTP date. An unreadable value yields 0, meaning "retriable,
// delay unknown" — the field is a hint, never a reason to fail.
func parseRetryAfter(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return n
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := int(time.Until(t).Seconds()); d > 0 {
			return d
		}
	}
	return 0
}

// ValidateURL rejects input that is not an http(s) URL before it costs a
// request.
//
// The check is here rather than in the fetcher because the failure is the
// user's typo, and a local invalid_args naming the problem is a better answer
// than a 400 relayed from the API a second later. It is deliberately narrow:
// anything beyond scheme and host is Firecrawl's call, not this CLI's.
func ValidateURL(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", errs.New(errs.CodeInvalidArgs, "empty URL").
			WithHint("Pass a page to fetch, e.g. text fetch https://example.com.")
	}
	// A bare "example.com" is what people type; assuming https is friendlier
	// than an error, and matches what every browser does.
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", errs.Newf(errs.CodeInvalidArgs, "not a valid URL: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errs.Newf(errs.CodeInvalidArgs, "unsupported URL scheme %q", u.Scheme).
			WithHint("Only http and https can be fetched. Use --file for a local path.")
	}
	if u.Host == "" {
		return "", errs.Newf(errs.CodeInvalidArgs, "URL has no host: %q", raw)
	}
	return u.String(), nil
}
