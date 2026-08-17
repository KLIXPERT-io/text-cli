package gdocs

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

func TestParseDocID(t *testing.T) {
	const id = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr errs.Code
	}{
		{name: "a bare id passes through", in: id, want: id},
		{name: "the edit URL people copy", in: "https://docs.google.com/document/d/" + id + "/edit", want: id},
		{
			name: "an edit URL with a heading fragment",
			in:   "https://docs.google.com/document/d/" + id + "/edit#heading=h.abc123",
			want: id,
		},
		{name: "the multi-account URL form", in: "https://docs.google.com/document/u/2/d/" + id + "/edit", want: id},
		{name: "no trailing segment", in: "https://docs.google.com/document/d/" + id, want: id},
		{name: "surrounding whitespace from a paste", in: "  " + id + "\n", want: id},
		{name: "the legacy query form", in: "https://docs.google.com/open?id=" + id, want: id},
		// The neighbouring Workspace apps get an answer about themselves. "not a
		// valid id" would send someone hunting for a typo in a correct URL.
		{
			name:    "a spreadsheet is rejected by name",
			in:      "https://docs.google.com/spreadsheets/d/" + id + "/edit",
			wantErr: errs.CodeInvalidArgs,
		},
		{
			name:    "a slide deck is rejected by name",
			in:      "https://docs.google.com/presentation/d/" + id + "/edit",
			wantErr: errs.CodeInvalidArgs,
		},
		{name: "empty input", in: "   ", wantErr: errs.CodeInvalidArgs},
		{name: "a URL that is not Google Docs", in: "https://example.com/post", wantErr: errs.CodeInvalidArgs},
		// A short word is far more likely to be a mistyped flag value than a
		// real Drive id, and guessing would produce a confusing 404 later.
		{name: "a word is not an id", in: "draft", wantErr: errs.CodeInvalidArgs},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDocID(tc.in)
			if tc.wantErr != "" {
				var e *errs.E
				if err == nil {
					t.Fatalf("ParseDocID(%q) = %q, want an error", tc.in, got)
				}
				if !asE(err, &e) || e.Code != tc.wantErr {
					t.Fatalf("error = %v, want code %s", err, tc.wantErr)
				}
				if e.Hint == "" {
					t.Error("error carries no hint; the user has nothing to act on")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDocID(%q) errored: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseDocID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseDocIDNamesTheWrongAppInTheMessage(t *testing.T) {
	_, err := ParseDocID("https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit")
	if err == nil || !strings.Contains(err.Error(), "Google Sheet") {
		t.Fatalf("error = %v, want it to say the URL is a Google Sheet", err)
	}
}

func TestIsDocURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{name: "a document URL is claimed", in: "https://docs.google.com/document/d/1BxiMVs0XRA5nFMdKvBd/edit", want: true},
		{name: "the multi-account form is claimed", in: "https://docs.google.com/document/u/0/d/1BxiMVs0XRA5nFMdKvBd/edit", want: true},
		// Claiming any of these would route a URL this backend cannot read away
		// from the backend that can, turning a working scrape into an auth error.
		{name: "a spreadsheet is not claimed", in: "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5/edit", want: false},
		{name: "a Google Drive file is not claimed", in: "https://drive.google.com/file/d/1BxiMVs0XRA5/view", want: false},
		{name: "an ordinary page is not claimed", in: "https://example.com/document/d/thing", want: false},
		{name: "the docs marketing site is not claimed", in: "https://docs.google.com/", want: false},
		{name: "a bare id is not a URL", in: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDocURL(tc.in); got != tc.want {
				t.Fatalf("IsDocURL(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestOptionsScopes(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{
			// A read command holding a token that cannot write is the point: a
			// bug in this package cannot modify a document it was only asked to
			// measure.
			name: "a plain read asks for read-only Docs",
			opts: Options{},
			want: []string{ScopeDocumentsRead},
		},
		{
			name: "a write asks for Docs write and no Drive",
			opts: Options{Write: true},
			want: []string{ScopeDocuments},
		},
		{
			name: "reading comments adds read-only Drive",
			opts: Options{Comments: true},
			want: []string{ScopeDocumentsRead, ScopeDriveRead},
		},
		{
			name: "writing comments adds Drive write",
			opts: Options{Write: true, Comments: true},
			want: []string{ScopeDocuments, ScopeDrive},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.opts.Scopes()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Scopes() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShareHintNamesTheAddress(t *testing.T) {
	acct := Account{Email: "text-cli@proj.iam.gserviceaccount.com"}

	read := shareHint(acct, false)
	if !strings.Contains(read, acct.Email) {
		t.Fatalf("read hint = %q, want the service account address in it", read)
	}
	if !strings.Contains(read, "Viewer") {
		t.Errorf("read hint asks for more than it needs: %q", read)
	}
	if write := shareHint(acct, true); !strings.Contains(write, "Editor") {
		t.Errorf("write hint = %q, want it to ask for Editor", write)
	}
	// Application Default Credentials name no address this CLI can read, and a
	// hint that says "share it with " and stops is worse than one that explains.
	if adc := shareHint(Account{}, false); strings.Contains(adc, "Share the document with  ") {
		t.Errorf("ADC hint has an empty address in it: %q", adc)
	}
}

func TestCountOccurrences(t *testing.T) {
	const text = "Der Antrag. Die Antragstellung. der antrag."

	tests := []struct {
		name      string
		find      string
		matchCase bool
		want      int
	}{
		{name: "case-insensitive by default", find: "Der Antrag", want: 2},
		{name: "case-sensitive when asked", find: "Der Antrag", matchCase: true, want: 1},
		{name: "a substring of a longer word still counts", find: "Antrag", want: 3},
		{name: "no match is zero", find: "Widerspruch", want: 0},
		{name: "an empty needle matches nothing", find: "", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := countOccurrences(text, tc.find, tc.matchCase); got != tc.want {
				t.Fatalf("countOccurrences(%q) = %d, want %d", tc.find, got, tc.want)
			}
		})
	}
}

func TestParseIndex(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{name: "a position", in: "42", want: 42},
		{name: "the first character", in: "1", want: 1},
		// Index 0 is the segment, not a position in it; the API rejects it and
		// saying so locally beats relaying a 400.
		{name: "zero is not a position", in: "0", wantErr: true},
		{name: "a word is not a position", in: "middle", wantErr: true},
		{name: "a negative number is not a position", in: "-3", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseIndex(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseIndex(%q) = %d, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIndex(%q) errored: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("parseIndex(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// asE is errors.As specialised to *errs.E, kept local so the table tests read
// as assertions rather than as plumbing.
func asE(err error, target **errs.E) bool {
	e, ok := err.(*errs.E)
	if ok {
		*target = e
	}
	return ok
}
