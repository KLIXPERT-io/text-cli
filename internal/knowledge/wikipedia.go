package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// SourceWikipedia is the registry name of the Wikipedia backend.
const SourceWikipedia = "wikipedia"

// userAgent identifies this client to Wikipedia.
//
// This is not cosmetic. The Wikimedia user-agent policy requires a descriptive
// agent with a way to contact the operator, and requests without one are rate
// limited or answered with 403 — which is the single most common cause of a
// "mysterious" failure against these endpoints. The version is not part of it
// on purpose: internal/knowledge has no access to the ldflags-injected version,
// and the policy asks for an identifiable, contactable client, not a precise
// build.
//
// https://foundation.wikimedia.org/wiki/Policy:Wikimedia_Foundation_User-Agent_Policy
const userAgent = "text-cli (+https://github.com/KLIXPERT-io/text-cli)"

// maxBodyBytes caps a single response. A summary is a few kilobytes; anything
// approaching this is a captive portal or a proxy error page, and reading it
// into memory unbounded would be the bug.
const maxBodyBytes = 4 << 20

// searchLimitMax caps --limit. The API allows 500, but a knowledge search feeds
// a human or a prompt, and neither wants 500 candidates.
const searchLimitMax = 50

// DefaultSearchLimit is the number of hits returned when none is asked for.
const DefaultSearchLimit = 10

func init() {
	Register(SourceWikipedia, func() (Source, error) { return newWikipedia(), nil })
}

// wikipedia reads the public Wikipedia REST and Action APIs. Neither needs a
// key, which is why this is the source that ships first.
type wikipedia struct {
	// baseURL overrides the per-language host. Empty — the production value —
	// means "https://<lang>.wikipedia.org", derived per call so one source
	// instance can serve both --lang en and --lang de. Tests set it to an
	// httptest.Server so the suite never touches the real API.
	baseURL string
	client  *http.Client
	timeout time.Duration
}

func newWikipedia() *wikipedia {
	return &wikipedia{
		client:  &http.Client{Timeout: DefaultTimeout},
		timeout: DefaultTimeout,
	}
}

func (w *wikipedia) Name() string { return SourceWikipedia }

// SetTimeout applies the command's --timeout. It sets the client deadline as
// well as the per-request context deadline: the context bounds the whole call
// including the body read, the client deadline is the backstop for a hung TLS
// handshake.
func (w *wikipedia) SetTimeout(d time.Duration) {
	if d <= 0 {
		d = DefaultTimeout
	}
	w.timeout = d
	w.client.Timeout = d
}

// endpoint returns the scheme+host for a language edition.
func (w *wikipedia) endpoint(lang string) string {
	if w.baseURL != "" {
		return strings.TrimSuffix(w.baseURL, "/")
	}
	// NormalizeLang, not the raw value: "auto" is a --lang value, never a
	// subdomain, and an unvalidated string here would be interpolated into a
	// hostname.
	return "https://" + NormalizeLang(lang) + ".wikipedia.org"
}

// pageTitle is the path-segment form of a title: spaces as underscores, then
// percent-escaped as a single segment, so "Große Koalition" becomes
// "Gro%C3%9Fe_Koalition" and a title containing a slash ("AC/DC") stays one
// segment instead of turning into a second path element.
func pageTitle(title string) string {
	return url.PathEscape(strings.ReplaceAll(CanonicalTitle(title), " ", "_"))
}

// summaryResponse is the subset of the REST summary payload this CLI uses.
type summaryResponse struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
	Lang        string `json:"lang"`
	Titles      struct {
		Canonical  string `json:"canonical"`
		Normalized string `json:"normalized"`
		Display    string `json:"display"`
	} `json:"titles"`
	ContentURLs struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
	} `json:"content_urls"`
	Thumbnail struct {
		Source string `json:"source"`
	} `json:"thumbnail"`
}

func (w *wikipedia) Lookup(ctx context.Context, title, lang string) (*Article, error) {
	title = CanonicalTitle(title)
	if title == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "empty article title").
			WithHint(`Pass a title: text kb lookup "Ada Lovelace".`)
	}
	lang = NormalizeLang(lang)

	u := w.endpoint(lang) + "/api/rest_v1/page/summary/" + pageTitle(title)
	body, err := w.get(ctx, u, title, lang)
	if err != nil {
		return nil, err
	}

	var sr summaryResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, errs.Newf(errs.CodeAPI5xx, "wikipedia returned an unreadable summary for %q: %s", title, err.Error()).
			WithHint("Retry; if it persists the API contract changed and text-cli needs an update.")
	}

	art := &Article{
		Title:       firstNonEmpty(sr.Title, sr.Titles.Normalized, title),
		Description: strings.TrimSpace(sr.Description),
		Extract:     strings.TrimSpace(sr.Extract),
		URL:         sr.ContentURLs.Desktop.Page,
		// The response's own lang wins: a redirect across editions, or a
		// language whose edition reports a different code, should be reported
		// as what actually answered.
		Lang:         firstNonEmpty(sr.Lang, lang),
		ThumbnailURL: sr.Thumbnail.Source,
		// The REST summary types are "standard", "disambiguation", "mainpage"
		// and "no-extract"; only the second one changes how a caller should
		// treat the extract.
		Disambiguation: sr.Type == "disambiguation",
	}
	if art.URL == "" {
		art.URL = w.endpoint(lang) + "/wiki/" + pageTitle(art.Title)
	}
	art.Aliases = aliases(art.Title, title, sr.Titles.Normalized, sr.Titles.Canonical)
	return art, nil
}

// searchResponse is the subset of the Action API's list=search payload used
// here. The Action API answers errors with HTTP 200 and an error object, so
// that field is parsed too — without it a bad query looks like zero results.
type searchResponse struct {
	Query struct {
		Search []struct {
			Title   string  `json:"title"`
			Snippet string  `json:"snippet"`
			Score   float64 `json:"score"`
		} `json:"search"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error"`
}

func (w *wikipedia) Search(ctx context.Context, query, lang string, limit int) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "empty search query").
			WithHint(`Pass a query: text kb search "analytical engine".`)
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > searchLimitMax {
		limit = searchLimitMax
	}
	lang = NormalizeLang(lang)

	q := url.Values{
		"action":   {"query"},
		"list":     {"search"},
		"srsearch": {query},
		"format":   {"json"},
		"srlimit":  {strconv.Itoa(limit)},
	}
	u := w.endpoint(lang) + "/w/api.php?" + q.Encode()
	body, err := w.get(ctx, u, query, lang)
	if err != nil {
		return nil, err
	}

	var sr searchResponse
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, errs.Newf(errs.CodeAPI5xx, "wikipedia returned unreadable search results: %s", err.Error()).
			WithHint("Retry; if it persists the API contract changed and text-cli needs an update.")
	}
	if sr.Error != nil {
		return nil, errs.Newf(errs.CodeInvalidArgs, "wikipedia rejected the search (%s): %s", sr.Error.Code, sr.Error.Info).
			WithHint("Check the query and --limit.")
	}

	hits := make([]SearchHit, 0, len(sr.Query.Search))
	for _, h := range sr.Query.Search {
		if h.Title == "" {
			continue
		}
		hits = append(hits, SearchHit{
			Title: h.Title,
			// The snippet arrives as HTML with the matched terms wrapped in
			// <span class="searchmatch">. A CLI writes plain text, and a CSV
			// cell full of markup is unusable, so the tags come out here rather
			// than in every consumer.
			Description: plainText(h.Snippet),
			URL:         w.endpoint(lang) + "/wiki/" + pageTitle(h.Title),
			Score:       h.Score,
		})
	}
	return hits, nil
}

// get performs one GET and returns the body, translating every failure mode
// into a structured error. subject is the title or query, used to make the
// not-found message actionable.
func (w *wikipedia) get(ctx context.Context, rawurl, subject, lang string) ([]byte, error) {
	timeout := w.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, errs.Newf(errs.CodeInvalidArgs, "bad request URL: %s", err.Error())
	}
	req.Header.Set("User-Agent", userAgent)
	// Api-User-Agent is what the Action API reads when a browser-shaped client
	// cannot set User-Agent; setting both costs nothing and is what the policy
	// asks for.
	req.Header.Set("Api-User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	client := w.client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, translateTransport(err, lang)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, translateTransport(err, lang)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, translateHTTP(resp.StatusCode, string(body), subject)
	}
	return body, nil
}

// translateHTTP maps an HTTP status onto the CLI's error vocabulary.
//
// It is a pure function of (status, body, title) so the whole mapping is
// unit-testable without a network: every branch here is a documented exit code,
// and an agent branches on the code rather than on the prose.
func translateHTTP(status int, body, title string) error {
	detail := apiDetail(body)
	switch {
	case status >= 200 && status <= 299:
		return nil

	case status == http.StatusNotFound:
		msg := "no article titled " + strconv.Quote(title)
		if detail != "" {
			msg += ": " + detail
		}
		return errs.New(errs.CodeNotFound, msg).
			WithHint("Titles are case- and spelling-sensitive. Find the right one with `text kb search " + strconv.Quote(title) + "`.")

	case status == http.StatusTooManyRequests:
		return errs.New(errs.CodeRateLimited, joinDetail("wikipedia rate limited this client", detail)).
			WithHint("Wait for the window to reset, or lower the request rate — cached lookups are free, so avoid --no-cache in a loop.").
			WithRetry(5)

	case status == http.StatusForbidden, status == http.StatusUnauthorized:
		// Wikipedia answers 403 to clients it has blocked, and the usual cause
		// is a missing or generic User-Agent. Naming that saves an hour.
		return errs.New(errs.CodeAuthDenied, joinDetail("wikipedia refused the request", detail)).
			WithHint("The API blocks unidentified clients. If you are behind a proxy that strips User-Agent, fix that first; a persistent block needs a different network or an API key-based mirror.")

	case status >= 500:
		return errs.Newf(errs.CodeAPI5xx, "wikipedia failed with HTTP %d%s", status, prefixDetail(detail)).
			WithHint("The API failed on its side. Retry; if it persists check https://www.wikimediastatus.net.").
			WithRetry(5)

	case status >= 400:
		return errs.Newf(errs.CodeInvalidArgs, "wikipedia rejected the request with HTTP %d%s", status, prefixDetail(detail)).
			WithHint("Check the title, --lang, and --limit.")
	}
	return errs.Newf(errs.CodeGeneric, "wikipedia returned HTTP %d%s", status, prefixDetail(detail))
}

// translateTransport classifies failures that never produced a status: DNS,
// dial, TLS, and the deadline this client sets itself.
func translateTransport(err error, lang string) error {
	if err == nil {
		return nil
	}
	var e *errs.E
	if errors.As(err, &e) {
		return e
	}
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return errs.New(errs.CodeGeneric, msg)

	case errors.Is(err, context.DeadlineExceeded), strings.Contains(strings.ToLower(msg), "timeout"):
		return errs.New(errs.CodeNetworkUnreachable, msg).
			WithHint("The call exceeded --timeout. Raise it, or retry.").
			WithRetry(5)
	}
	return errs.New(errs.CodeNetworkUnreachable, msg).
		WithHint("Could not reach " + NormalizeLang(lang) + ".wikipedia.org. Check the network or proxy settings.").
		WithRetry(5)
}

// apiDetail pulls the human-readable reason out of an error body.
//
// A JSON body yields its message field or nothing at all: the REST API answers
// a missing page with {"status":404,"type":"Internal error"}, and echoing that
// back would add a scary, wrong "Internal error" to a perfectly ordinary miss.
// The raw-snippet fallback is only for a plain-text body, and an HTML error
// page from a proxy is dropped outright.
func apiDetail(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(body), &obj); err == nil {
		// "detail" is the REST API's field, "title" its short form; the Action
		// API nests its message under error.info.
		for _, k := range []string{"detail", "title"} {
			if v, ok := obj[k].(string); ok && v != "" {
				return collapse(v)
			}
		}
		if e, ok := obj["error"].(map[string]any); ok {
			if v, ok := e["info"].(string); ok && v != "" {
				return collapse(v)
			}
		}
		return ""
	}
	if strings.HasPrefix(body, "<") {
		return ""
	}
	return truncate(collapse(body), 200)
}

func joinDetail(msg, detail string) string {
	if detail == "" {
		return msg
	}
	return msg + ": " + detail
}

func prefixDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return ": " + detail
}

// tagRe strips HTML elements from search snippets. A parser would be overkill:
// the snippet is a single line of Wikipedia-generated markup with no attributes
// worth keeping.
var tagRe = regexp.MustCompile(`<[^>]*>`)

func plainText(s string) string {
	return collapse(html.UnescapeString(tagRe.ReplaceAllString(s, "")))
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// aliases collects the other spellings an article answers to, in a stable
// order, skipping the resolved title itself and any duplicate. The requested
// title is included because a redirect is exactly the case a caller wants to
// see: asking for "JFK" and getting "John F. Kennedy" is information.
func aliases(resolved string, candidates ...string) []string {
	seen := map[string]bool{CanonicalTitle(resolved): true}
	var out []string
	for _, c := range candidates {
		c = CanonicalTitle(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
