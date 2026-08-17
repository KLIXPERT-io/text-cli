package fetch

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
)

// FetcherFirecrawl is the registered name of this backend.
const FetcherFirecrawl = "firecrawl"

func init() {
	Register(FetcherFirecrawl, func() (Fetcher, error) { return &firecrawlFetcher{}, nil })
}

// firecrawlFetcher reads pages through Firecrawl's /v2/scrape endpoint.
//
// It asks for markdown rather than HTML or the API's own text extraction. That
// is the whole reason this backend is worth having: `text` already knows how to
// reduce markdown to prose (internal/strip), and markdown preserves the
// heading and list structure that a plain-text extraction flattens into
// run-together sentences — which is exactly the input that makes a readability
// score wrong.
type firecrawlFetcher struct {
	apiKey  string
	baseURL string
}

func (f *firecrawlFetcher) Name() string { return FetcherFirecrawl }

var _ APIConfigurer = (*firecrawlFetcher)(nil)

func (f *firecrawlFetcher) SetAPIKey(key string)   { f.apiKey = key }
func (f *firecrawlFetcher) SetBaseURL(base string) { f.baseURL = base }

// scrapeRequest is the subset of /v2/scrape this CLI sends.
//
// It is deliberately small. The endpoint also takes actions, screenshots,
// JSON extraction prompts, and change tracking; none of those produce prose,
// and every one of them would be a flag on `text fetch` that no analysis
// command could consume.
type scrapeRequest struct {
	URL             string   `json:"url"`
	Formats         []string `json:"formats"`
	OnlyMainContent bool     `json:"onlyMainContent"`
	MaxAge          int      `json:"maxAge"`
}

type scrapeResponse struct {
	Success bool   `json:"success"`
	Warning string `json:"warning"`
	Data    struct {
		Markdown string   `json:"markdown"`
		Links    []string `json:"links"`
		Warning  string   `json:"warning"`
		Metadata struct {
			Title       flexString `json:"title"`
			Description flexString `json:"description"`
			Language    flexString `json:"language"`
			SourceURL   string     `json:"sourceURL"`
			URL         string     `json:"url"`
			StatusCode  int        `json:"statusCode"`
			CreditsUsed int        `json:"creditsUsed"`
			Error       string     `json:"error"`
		} `json:"metadata"`
	} `json:"data"`
}

// flexString decodes a metadata field that the API types as "string or array of
// strings". A page with two <meta name="description"> tags really does come
// back as an array, and a plain string field would fail the whole decode over
// a duplicated tag.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = flexString(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err == nil {
		// First wins: the duplicates are the page's mistake, and joining them
		// would invent a title no tag actually contains.
		for _, v := range many {
			if strings.TrimSpace(v) != "" {
				*s = flexString(v)
				return nil
			}
		}
		*s = ""
		return nil
	}
	// Any other shape (a number, an object) is metadata this CLI does not need
	// badly enough to fail a scrape over.
	*s = ""
	return nil
}

func (f *firecrawlFetcher) Fetch(ctx context.Context, rawURL string, opts Options) (*Page, error) {
	target, err := firecrawl.ValidateURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := firecrawl.RequireKey(f.apiKey); err != nil {
		return nil, err
	}

	client := firecrawl.New(f.apiKey, f.baseURL)
	client.SetTimeout(effectiveClientTimeout(opts.EffectiveTimeout()))

	formats := []string{"markdown"}
	if opts.IncludeLinks {
		formats = append(formats, "links")
	}
	req := scrapeRequest{
		URL:             target,
		Formats:         formats,
		OnlyMainContent: opts.MainContentOnly,
		MaxAge:          opts.MaxAge,
	}

	var resp scrapeResponse
	if err := client.Post(ctx, "/v2/scrape", req, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errs.Newf(errs.CodeProviderUnavailable, "Firecrawl could not scrape %s", target).
			WithHint("Try --no-main-content, or check the page loads in a browser.")
	}

	md := strings.TrimSpace(resp.Data.Markdown)
	if md == "" {
		// A successful scrape with no text is a real outcome — a login wall, a
		// pure-canvas app — and the caller must not be handed empty input that
		// a later command reports as a mysterious empty_input.
		hint := "The page may require JavaScript beyond the scrape, be login-walled, or hold no text."
		if opts.MainContentOnly {
			hint = "Retry with --no-main-content: the extractor may have discarded the whole body as boilerplate."
		}
		return nil, errs.Newf(errs.CodeEmptyInput, "Firecrawl returned no text for %s", target).WithHint(hint)
	}

	final := resp.Data.Metadata.URL
	if final == "" {
		final = resp.Data.Metadata.SourceURL
	}
	if final == "" {
		final = target
	}
	page := &Page{
		URL:         final,
		Title:       strings.TrimSpace(string(resp.Data.Metadata.Title)),
		Description: strings.TrimSpace(string(resp.Data.Metadata.Description)),
		Language:    normalizeLang(string(resp.Data.Metadata.Language)),
		Content:     md,
		Links:       resp.Data.Links,
		StatusCode:  resp.Data.Metadata.StatusCode,
		Fetcher:     FetcherFirecrawl,
		Credits:     resp.Data.Metadata.CreditsUsed,
	}
	// Only when it actually differs: echoing the same URL twice in every
	// result is noise in a format whose point is being compact.
	if final != target {
		page.RequestedURL = target
	}
	return page, nil
}

// effectiveClientTimeout leaves the HTTP deadline a little longer than the
// scrape the caller asked for.
//
// Firecrawl enforces its own limit server-side and answers a slow page with a
// structured error; cutting the connection first would replace that message
// with a generic timeout and lose the reason.
func effectiveClientTimeout(d time.Duration) time.Duration {
	return d + 10*time.Second
}

// normalizeLang reduces a page's declared language to the base code the rest of
// the CLI speaks: "en-GB" and "en_US" are both "en", because --lang selects a
// set of syllable rules and a metric family, not a locale.
//
// The length check is load-bearing rather than cosmetic. "x-default" is a real
// hreflang value that appears on plenty of pages, and cutting it at the dash
// would declare the language "x" — which is not a language, and which the
// analysis commands would then have to reject as unsupported instead of simply
// detecting the language from the text as they do when nothing is declared.
func normalizeLang(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexAny(v, "-_"); i > 0 {
		v = v[:i]
	}
	// ISO 639-1 is two letters and 639-2/3 are three; anything else is not a
	// language code.
	if len(v) < 2 || len(v) > 3 {
		return ""
	}
	for _, r := range v {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	return v
}
