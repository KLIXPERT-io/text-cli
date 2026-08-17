package fetch

import "testing"

func TestGoogleDocsFetcherClaimsOnlyDocuments(t *testing.T) {
	f := &gdocsFetcher{}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "a document is claimed", url: "https://docs.google.com/document/d/1BxiMVs0XRA5nFMdKvBd/edit", want: true},
		// Claiming a URL this backend cannot read would turn a working scrape
		// into an authentication error.
		{name: "a spreadsheet is left alone", url: "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5/edit", want: false},
		{name: "an ordinary article is left alone", url: "https://example.com/post", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.Handles(tc.url); got != tc.want {
				t.Fatalf("Handles(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestForURLRoutesToTheBackendThatCanRead(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			// A Google Doc is not a scrapeable page: the generic backend gets a
			// login screen, so the claim is a statement of fact rather than a
			// preference.
			name: "a document routes to the docs backend",
			url:  "https://docs.google.com/document/d/1BxiMVs0XRA5nFMdKvBd/edit",
			want: FetcherGoogleDocs,
		},
		{
			// Nothing claims an ordinary page, so the caller's default applies.
			name: "an article is claimed by nobody",
			url:  "https://example.com/post",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForURL(tc.url); got != tc.want {
				t.Fatalf("ForURL(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestGoogleDocsPagesAreNotCached(t *testing.T) {
	// The page cache exists so a billed scrape is paid for once. Reading a
	// document is free and the document may be being typed into right now, so
	// there is nothing to save and correctness to lose.
	var f Fetcher = &gdocsFetcher{}
	hinter, ok := f.(CacheTTLHinter)
	if !ok {
		t.Fatal("the docs backend does not declare a cache policy; pages would be stored for 24h")
	}
	if hinter.CacheTTL() != 0 {
		t.Fatalf("CacheTTL() = %v, want 0", hinter.CacheTTL())
	}
}

func TestDefaultIsStillTheScraper(t *testing.T) {
	// A second registered backend must not change which one answers a URL that
	// nobody claims.
	if got := Default(); got != FetcherFirecrawl {
		t.Fatalf("Default() = %q, want %q", got, FetcherFirecrawl)
	}
}
