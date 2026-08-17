package fetch

import (
	"context"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/gdocs"
)

// FetcherGoogleDocs is the registry name of the Google Docs backend.
const FetcherGoogleDocs = "gdocs"

func init() {
	Register(FetcherGoogleDocs, func() (Fetcher, error) { return &gdocsFetcher{}, nil })
}

// gdocsFetcher reads a Google Doc as a page.
//
// It exists so that --url works on a document the same way it works on an
// article: `text lint --url <doc-url>` is the command an editor actually wants,
// and without this it would scrape a Google login page and report the sign-in
// form's reading level.
//
// It claims docs.google.com/document URLs through URLMatcher rather than
// waiting to be selected, because for those URLs it is not a preference — the
// generic scraper cannot read a private document at all.
type gdocsFetcher struct {
	// serviceAccount is injected after construction via
	// ServiceAccountConfigurer, so the factory stays free of credentials.
	serviceAccount string
}

func (f *gdocsFetcher) Name() string { return FetcherGoogleDocs }

// Handles claims Google Docs URLs and nothing else.
func (f *gdocsFetcher) Handles(url string) bool { return gdocs.IsDocURL(url) }

// SetServiceAccount receives the configured key path.
func (f *gdocsFetcher) SetServiceAccount(path string) { f.serviceAccount = strings.TrimSpace(path) }

// CacheTTL keeps documents out of the page cache.
//
// The cache is there so a billed scrape is paid for once. Reading a document is
// free, and a document is the one input in this CLI that somebody may be typing
// into while it is read — serving a copy from an hour ago would make `text lint
// --url <doc>` disagree with the tab the user is looking at, and there is
// nothing saved in exchange.
func (f *gdocsFetcher) CacheTTL() time.Duration { return 0 }

// Fetch reads one document.
//
// MainContentOnly and IncludeLinks are accepted and ignored: a document has no
// navigation to strip and no outbound link list to collect. Honouring them
// would mean inventing a difference that the source does not have.
func (f *gdocsFetcher) Fetch(ctx context.Context, url string, opts Options) (*Page, error) {
	id, err := gdocs.ParseDocID(url)
	if err != nil {
		return nil, err
	}

	client, err := gdocs.Open(ctx, gdocs.Options{
		ServiceAccountPath: f.serviceAccount,
		Timeout:            opts.Timeout,
	})
	if err != nil {
		return nil, err
	}
	doc, err := client.Read(ctx, id, gdocs.ReadOptions{})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.Content) == "" {
		return nil, errs.Newf(errs.CodeEmptyInput, "the document %q has no text", doc.Title).
			WithHint("An empty document, or one whose text is all in images. Open it to check: " + doc.URL)
	}

	page := &Page{
		URL:     doc.URL,
		Title:   doc.Title,
		Content: doc.Content,
		Fetcher: FetcherGoogleDocs,
	}
	if url != doc.URL {
		page.RequestedURL = url
	}
	return page, nil
}
