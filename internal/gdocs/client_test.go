package gdocs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	docs "google.golang.org/api/docs/v1"
)

// The tests in this file drive the real generated clients against an
// httptest.Server, the same way the Wikipedia and Firecrawl suites do. That
// covers the request shapes, the response mapping, and the error translation
// without a credential and without touching Google.

const testEmail = "text-cli@test-project.iam.gserviceaccount.com"

// server is a stand-in for both APIs. The Docs client asks for /v1/documents/…
// and the Drive client for /files/… , so one handler can serve both.
type server struct {
	t *testing.T
	// doc is returned by documents.get.
	doc *docs.Document
	// comments is returned by comments.list.
	comments []map[string]any
	// status and body, when set, replace the next response with a failure.
	status int
	body   string
	// captured records what the last write actually sent.
	batch   *docs.BatchUpdateDocumentRequest
	created map[string]any
	// calls counts requests, so a dry run can be shown not to have written.
	calls map[string]int
}

func (s *server) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.calls == nil {
			s.calls = map[string]int{}
		}
		path := r.URL.Path
		switch {
		case s.status != 0:
			s.calls["error"]++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.status)
			_, _ = w.Write([]byte(s.body))

		case strings.HasSuffix(path, ":batchUpdate"):
			s.calls["batchUpdate"]++
			var req docs.BatchUpdateDocumentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				s.t.Errorf("decode batchUpdate: %v", err)
			}
			s.batch = &req
			writeJSON(w, map[string]any{
				"documentId":   "doc-1",
				"writeControl": map[string]any{"requiredRevisionId": "rev-2"},
				"replies":      []any{map[string]any{"replaceAllText": map[string]any{"occurrencesChanged": 1}}},
			})

		case strings.HasPrefix(path, "/v1/documents/"):
			s.calls["get"]++
			writeJSON(w, s.doc)

		case strings.Contains(path, "/replies"):
			s.calls["reply"]++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.created = body
			body["id"] = "reply-1"
			writeJSON(w, body)

		case strings.HasSuffix(path, "/comments") && r.Method == http.MethodPost:
			s.calls["createComment"]++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.created = body
			body["id"] = "comment-new"
			writeJSON(w, body)

		case strings.HasSuffix(path, "/comments"):
			s.calls["comments"]++
			writeJSON(w, map[string]any{"comments": s.comments})

		default:
			s.t.Errorf("unexpected request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// open starts a server and returns a client pointed at it, with a service
// account key on disk so the identity in the error hints is a real one.
func open(t *testing.T, s *server, opts Options) (*Client, *httptest.Server) {
	t.Helper()
	s.t = t
	// Whatever the developer has exported must not reach these tests.
	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	key := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(key, []byte(`{"type":"service_account","client_email":"`+testEmail+`","project_id":"test-project"}`), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)

	opts.ServiceAccountPath = key
	opts.Endpoint = ts.URL
	c, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c, ts
}

// tab builds one document tab.
func tab(id, title string, content ...*docs.StructuralElement) *docs.Tab {
	return &docs.Tab{
		TabProperties: &docs.TabProperties{TabId: id, Title: title},
		DocumentTab:   &docs.DocumentTab{Body: &docs.Body{Content: content}},
	}
}

func sampleDoc() *docs.Document {
	return &docs.Document{
		DocumentId: "doc-1",
		Title:      "Leitfaden",
		RevisionId: "rev-1",
		Tabs: []*docs.Tab{tab("t.0", "Entwurf",
			para("HEADING_1", run("Leitfaden\n")),
			para("NORMAL_TEXT", run("Die Inanspruchnahme der Leistung erfolgt auf Antrag.\n")),
		)},
	}
}

func TestReadRendersMarkdownAndCarriesTheRevision(t *testing.T) {
	c, _ := open(t, &server{doc: sampleDoc()}, Options{})

	doc, err := c.Read(context.Background(), "doc-1", ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Content != "# Leitfaden\n\nDie Inanspruchnahme der Leistung erfolgt auf Antrag." {
		t.Fatalf("content = %q", doc.Content)
	}
	// The revision is what a follow-up write pins itself to; losing it here
	// would silently turn every edit into a last-write-wins edit.
	if doc.RevisionID != "rev-1" {
		t.Errorf("revision = %q, want rev-1", doc.RevisionID)
	}
	if doc.URL != "https://docs.google.com/document/d/doc-1/edit" {
		t.Errorf("url = %q", doc.URL)
	}
	if doc.Suggestions != SuggestionsClean {
		t.Errorf("suggestions = %q, want the clean view by default", doc.Suggestions)
	}
	if len(doc.Tabs) != 1 || doc.Tabs[0].TabID != "t.0" {
		t.Errorf("tabs = %+v, want the one tab reported", doc.Tabs)
	}
}

func TestReadMultipleTabs(t *testing.T) {
	doc := &docs.Document{
		DocumentId: "doc-1",
		RevisionId: "rev-1",
		Tabs: []*docs.Tab{
			tab("t.0", "Erster", para("NORMAL_TEXT", run("Text eins.\n"))),
			tab("t.1", "Zweiter", para("NORMAL_TEXT", run("Text zwei.\n"))),
		},
	}

	t.Run("every tab is read, with its boundary marked", func(t *testing.T) {
		c, _ := open(t, &server{doc: doc}, Options{})
		got, err := c.Read(context.Background(), "doc-1", ReadOptions{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		// Without the headings the two tabs would silently become one flow of
		// prose, and the sentence at the seam would be measured as one sentence.
		want := "# Erster\n\nText eins.\n\n# Zweiter\n\nText zwei."
		if got.Content != want {
			t.Fatalf("content = %q, want %q", got.Content, want)
		}
	})

	t.Run("one tab can be selected", func(t *testing.T) {
		c, _ := open(t, &server{doc: doc}, Options{})
		got, err := c.Read(context.Background(), "doc-1", ReadOptions{TabID: "t.1"})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got.Content != "Text zwei." {
			t.Fatalf("content = %q, want only the selected tab, with no synthetic heading", got.Content)
		}
	})

	t.Run("an unknown tab is not found", func(t *testing.T) {
		c, _ := open(t, &server{doc: doc}, Options{})
		_, err := c.Read(context.Background(), "doc-1", ReadOptions{TabID: "t.9"})
		assertCode(t, err, errs.CodeNotFound)
	})
}

func TestReadFallsBackToTheBodyForATablessDocument(t *testing.T) {
	c, _ := open(t, &server{doc: &docs.Document{
		DocumentId: "doc-1",
		Body:       &docs.Body{Content: []*docs.StructuralElement{para("NORMAL_TEXT", run("Nur Text.\n"))}},
	}}, Options{})

	doc, err := c.Read(context.Background(), "doc-1", ReadOptions{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if doc.Content != "Nur Text." {
		t.Fatalf("content = %q, want the body of a document that reports no tabs", doc.Content)
	}
}

func TestReadRejectsAnUnknownSuggestionsMode(t *testing.T) {
	c, _ := open(t, &server{doc: sampleDoc()}, Options{})
	_, err := c.Read(context.Background(), "doc-1", ReadOptions{Suggestions: "maybe"})
	assertCode(t, err, errs.CodeInvalidArgs)
}

func TestComments(t *testing.T) {
	all := []map[string]any{
		{
			"id":                "c1",
			"content":           "Substantivstil — bitte umformulieren.",
			"author":            map[string]any{"displayName": "Ada"},
			"quotedFileContent": map[string]any{"value": "Die Inanspruchnahme"},
			"replies": []any{
				map[string]any{"id": "r1", "content": "übernehme ich", "author": map[string]any{"displayName": "Ben"}},
			},
		},
		{"id": "c2", "content": "erledigt", "resolved": true},
		{"id": "c3", "content": "gelöscht", "deleted": true},
	}

	t.Run("open threads only, by default", func(t *testing.T) {
		c, _ := open(t, &server{comments: all}, Options{Comments: true})
		got, err := c.Comments(context.Background(), "doc-1", CommentOptions{})
		if err != nil {
			t.Fatalf("Comments: %v", err)
		}
		if len(got) != 1 || got[0].ID != "c1" {
			t.Fatalf("comments = %+v, want only the open thread", got)
		}
		// The quoted passage is the field that makes a comment actionable: it
		// is a literal substring of the document, so it is what `replace
		// --find` is given.
		if got[0].Quoted != "Die Inanspruchnahme" {
			t.Errorf("quoted = %q", got[0].Quoted)
		}
		if got[0].Author != "Ada" || got[0].ReplyCount != 1 || got[0].Replies[0].Author != "Ben" {
			t.Errorf("thread = %+v, want the author and the reply mapped", got[0])
		}
	})

	t.Run("resolved threads on request", func(t *testing.T) {
		c, _ := open(t, &server{comments: all}, Options{Comments: true})
		got, err := c.Comments(context.Background(), "doc-1", CommentOptions{IncludeResolved: true})
		if err != nil {
			t.Fatalf("Comments: %v", err)
		}
		// Still without the deleted one: a deleted comment has no content, so
		// it would render as an empty row.
		if len(got) != 2 {
			t.Fatalf("comments = %d, want the open and the resolved thread", len(got))
		}
	})

	t.Run("a limit caps the list", func(t *testing.T) {
		c, _ := open(t, &server{comments: all}, Options{Comments: true})
		got, err := c.Comments(context.Background(), "doc-1", CommentOptions{IncludeResolved: true, Limit: 1})
		if err != nil {
			t.Fatalf("Comments: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("comments = %d, want 1", len(got))
		}
	})

	t.Run("a client without comment access says so", func(t *testing.T) {
		c, _ := open(t, &server{}, Options{})
		_, err := c.Comments(context.Background(), "doc-1", CommentOptions{})
		assertCode(t, err, errs.CodeProviderUnavailable)
	})
}

func TestReplyResolvesInTheSameCall(t *testing.T) {
	s := &server{}
	c, _ := open(t, s, Options{Write: true, Comments: true})

	got, err := c.Reply(context.Background(), "doc-1", "c1", ReplyOptions{Content: "done", Action: "resolve"})
	if err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if got.ID != "reply-1" {
		t.Errorf("id = %q", got.ID)
	}
	if s.created["action"] != "resolve" || s.created["content"] != "done" {
		t.Fatalf("sent %+v, want the text and the state change in one request", s.created)
	}
}

func TestReplyNeedsSomethingToSay(t *testing.T) {
	c, _ := open(t, &server{}, Options{Write: true, Comments: true})
	_, err := c.Reply(context.Background(), "doc-1", "c1", ReplyOptions{})
	assertCode(t, err, errs.CodeInvalidArgs)
}

func TestAddCommentRejectsEmptyText(t *testing.T) {
	c, _ := open(t, &server{}, Options{Write: true, Comments: true})
	_, err := c.AddComment(context.Background(), "doc-1", "   ")
	assertCode(t, err, errs.CodeInvalidArgs)
}

func TestReplaceGuards(t *testing.T) {
	repeated := &docs.Document{
		DocumentId: "doc-1",
		RevisionId: "rev-1",
		Tabs: []*docs.Tab{tab("t.0", "",
			para("NORMAL_TEXT", run("Der Antrag ist zu stellen.\n")),
			para("NORMAL_TEXT", run("Der Antrag wird geprüft.\n")),
		)},
	}

	tests := []struct {
		name    string
		doc     *docs.Document
		req     ReplaceRequest
		wantErr errs.Code
		// wantWrite is whether the API should have been called at all.
		wantWrite bool
	}{
		{
			name:      "a unique match is replaced",
			doc:       sampleDoc(),
			req:       ReplaceRequest{Find: "Die Inanspruchnahme", Replace: "Wer die Leistung nutzt"},
			wantWrite: true,
		},
		{
			// The failure worth spending a read to prevent: applying one review
			// comment must not silently rewrite three other places.
			name:    "an ambiguous match is refused",
			doc:     repeated,
			req:     ReplaceRequest{Find: "Der Antrag", Replace: "Das Gesuch"},
			wantErr: errs.CodeInvalidArgs,
		},
		{
			name:      "--all accepts the ambiguity",
			doc:       repeated,
			req:       ReplaceRequest{Find: "Der Antrag", Replace: "Das Gesuch", All: true},
			wantWrite: true,
		},
		{
			// A call that "succeeded" and changed nothing is the worst possible
			// answer: an agent would move on believing the edit landed.
			name:    "no match is not found",
			doc:     sampleDoc(),
			req:     ReplaceRequest{Find: "Widerspruch", Replace: "x"},
			wantErr: errs.CodeNotFound,
		},
		{
			name:    "an empty --find is refused",
			doc:     sampleDoc(),
			req:     ReplaceRequest{Replace: "x"},
			wantErr: errs.CodeInvalidArgs,
		},
		{
			// Docs matches within one paragraph, so a multi-line --find can
			// never match. Saying so beats a mystifying "no match".
			name:    "a --find spanning a line break is refused",
			doc:     sampleDoc(),
			req:     ReplaceRequest{Find: "eins\nzwei", Replace: "x"},
			wantErr: errs.CodeInvalidArgs,
		},
		{
			name:      "a dry run counts without writing",
			doc:       repeated,
			req:       ReplaceRequest{Find: "Der Antrag", Replace: "x", All: true, DryRun: true},
			wantWrite: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{doc: tc.doc}
			c, _ := open(t, s, Options{Write: true})

			got, err := c.Replace(context.Background(), "doc-1", tc.req)
			if tc.wantErr != "" {
				assertCode(t, err, tc.wantErr)
				if s.calls["batchUpdate"] != 0 {
					t.Fatal("the document was written despite the guard")
				}
				return
			}
			if err != nil {
				t.Fatalf("Replace: %v", err)
			}
			if got.Applied != tc.wantWrite {
				t.Fatalf("applied = %v, want %v", got.Applied, tc.wantWrite)
			}
			if (s.calls["batchUpdate"] > 0) != tc.wantWrite {
				t.Fatalf("batchUpdate calls = %d, want written = %v", s.calls["batchUpdate"], tc.wantWrite)
			}
		})
	}
}

func TestReplacePinsTheRevisionItRead(t *testing.T) {
	s := &server{doc: sampleDoc()}
	c, _ := open(t, s, Options{Write: true})

	if _, err := c.Replace(context.Background(), "doc-1", ReplaceRequest{
		Find: "Die Inanspruchnahme", Replace: "Wer nutzt", MatchCase: true,
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if s.batch == nil || s.batch.WriteControl == nil {
		t.Fatal("the write carried no write control; a concurrent edit would be overwritten instead of refused")
	}
	if s.batch.WriteControl.RequiredRevisionId != "rev-1" {
		t.Errorf("requiredRevisionId = %q, want the revision the read returned", s.batch.WriteControl.RequiredRevisionId)
	}
	req := s.batch.Requests[0].ReplaceAllText
	if req == nil || req.ContainsText.Text != "Die Inanspruchnahme" || !req.ContainsText.MatchCase {
		t.Errorf("request = %+v, want the literal search sent through", req)
	}
}

func TestInsert(t *testing.T) {
	tests := []struct {
		name     string
		req      InsertRequest
		wantText string
		// wantEnd is whether the insertion targets the end of the segment
		// rather than an explicit index.
		wantEnd   bool
		wantIndex int64
	}{
		{
			// Appending without the newline lands inside the last paragraph,
			// which is almost never what "append this note" meant.
			name:     "appending starts a new paragraph",
			req:      InsertRequest{Text: "Nachtrag."},
			wantText: "\nNachtrag.",
			wantEnd:  true,
		},
		{
			name:     "--inline joins the last paragraph",
			req:      InsertRequest{Text: " Nachtrag.", Inline: true},
			wantText: " Nachtrag.",
			wantEnd:  true,
		},
		{
			name:      "--at start goes to the first position",
			req:       InsertRequest{Text: "ENTWURF", At: AtStart},
			wantText:  "ENTWURF\n",
			wantIndex: 1,
		},
		{
			name:      "--at <index> is honoured",
			req:       InsertRequest{Text: "x", At: "12"},
			wantText:  "x",
			wantIndex: 12,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{doc: sampleDoc()}
			c, _ := open(t, s, Options{Write: true})

			if _, err := c.Insert(context.Background(), "doc-1", tc.req); err != nil {
				t.Fatalf("Insert: %v", err)
			}
			got := s.batch.Requests[0].InsertText
			if got.Text != tc.wantText {
				t.Errorf("text = %q, want %q", got.Text, tc.wantText)
			}
			if tc.wantEnd {
				if got.EndOfSegmentLocation == nil {
					t.Fatalf("location = %+v, want the end of the segment", got.Location)
				}
				return
			}
			if got.Location == nil || got.Location.Index != tc.wantIndex {
				t.Fatalf("location = %+v, want index %d", got.Location, tc.wantIndex)
			}
		})
	}
}

func TestInsertRefusesToGuessATab(t *testing.T) {
	twoTabs := &docs.Document{
		DocumentId: "doc-1",
		RevisionId: "rev-1",
		Tabs: []*docs.Tab{
			tab("t.0", "Erster", para("NORMAL_TEXT", run("eins\n"))),
			tab("t.1", "Zweiter", para("NORMAL_TEXT", run("zwei\n"))),
		},
	}
	s := &server{doc: twoTabs}
	c, _ := open(t, s, Options{Write: true})

	// Writing to tab one because it was first is the kind of default that
	// quietly edits the wrong half of a document.
	_, err := c.Insert(context.Background(), "doc-1", InsertRequest{Text: "x"})
	assertCode(t, err, errs.CodeInvalidArgs)
	if !strings.Contains(err.(*errs.E).Hint, "t.1") {
		t.Errorf("hint = %q, want it to list the tabs to choose from", err.(*errs.E).Hint)
	}
	if s.calls["batchUpdate"] != 0 {
		t.Fatal("a tab was guessed and written to")
	}
}

func TestErrorTranslation(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantCode errs.Code
		// wantHint is a fragment the hint must contain.
		wantHint string
	}{
		{
			// The mapping this whole feature turns on. Google answers a document
			// the caller cannot see with 404, so a literal "not found" would
			// send the user hunting for a typo in a correct id.
			name:     "404 asks for the document to be shared",
			status:   http.StatusNotFound,
			body:     `{"error":{"code":404,"message":"Requested entity was not found."}}`,
			wantCode: errs.CodeNotFound,
			wantHint: testEmail,
		},
		{
			name:     "403 on permissions asks for the document to be shared",
			status:   http.StatusForbidden,
			body:     `{"error":{"code":403,"message":"The caller does not have permission"}}`,
			wantCode: errs.CodeAuthDenied,
			wantHint: testEmail,
		},
		{
			// Fixed once, in the console, by whoever owns the key — not per
			// document by whoever owns the document. Sending someone to the
			// wrong one of those costs an afternoon.
			name:     "403 on a disabled API points at the console",
			status:   http.StatusForbidden,
			body:     `{"error":{"code":403,"message":"Google Docs API has not been used in project 123 before or it is disabled."}}`,
			wantCode: errs.CodeAuthDenied,
			wantHint: "console.cloud.google.com",
		},
		{
			name:     "401 says the credentials were rejected",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"code":401,"message":"Invalid Credentials"}}`,
			wantCode: errs.CodeAuthExpired,
		},
		{
			name:     "429 is retriable",
			status:   http.StatusTooManyRequests,
			body:     `{"error":{"code":429,"message":"Quota exceeded"}}`,
			wantCode: errs.CodeRateLimited,
		},
		{
			name:     "a stale revision is reported as a conflict to re-run",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":400,"message":"Invalid requiredRevisionId: the revision is not the head revision."}}`,
			wantCode: errs.CodeInvalidArgs,
			wantHint: "Re-run",
		},
		{
			name:     "500 is retriable",
			status:   http.StatusInternalServerError,
			body:     `{"error":{"code":500,"message":"Internal error"}}`,
			wantCode: errs.CodeAPI5xx,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := open(t, &server{status: tc.status, body: tc.body}, Options{})

			_, err := c.Read(context.Background(), "doc-1", ReadOptions{})
			assertCode(t, err, tc.wantCode)
			e := err.(*errs.E)
			if tc.wantHint != "" && !strings.Contains(e.Hint, tc.wantHint) {
				t.Fatalf("hint = %q, want it to contain %q", e.Hint, tc.wantHint)
			}
			if e.Hint == "" {
				t.Error("error carries no hint; the user has nothing to act on")
			}
		})
	}
}

func TestErrorRetriability(t *testing.T) {
	// An agent branches on this: a rate limit is worth waiting out, a denied
	// credential never is.
	tests := []struct {
		name      string
		status    int
		body      string
		retriable bool
	}{
		{name: "a rate limit", status: 429, body: `{"error":{"code":429,"message":"slow down"}}`, retriable: true},
		{name: "a server error", status: 500, body: `{"error":{"code":500,"message":"oops"}}`, retriable: true},
		{name: "a missing share", status: 404, body: `{"error":{"code":404,"message":"nope"}}`, retriable: false},
		{name: "a denied credential", status: 403, body: `{"error":{"code":403,"message":"denied"}}`, retriable: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := open(t, &server{status: tc.status, body: tc.body}, Options{})
			_, err := c.Read(context.Background(), "doc-1", ReadOptions{})
			if got := err.(*errs.E).Retriable; got != tc.retriable {
				t.Fatalf("retriable = %v, want %v", got, tc.retriable)
			}
		})
	}
}

func TestOpenRejectsAMissingKeyFile(t *testing.T) {
	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	// A configured path that does not exist is an error rather than a silent
	// fallback to Application Default Credentials: falling back would turn a
	// typo into a confusing permission error much later.
	_, err := Open(context.Background(), Options{ServiceAccountPath: filepath.Join(t.TempDir(), "nope.json")})
	assertCode(t, err, errs.CodeAuthMissing)
}

func TestOpenRejectsTheWrongKindOfCredential(t *testing.T) {
	t.Setenv("TEXT_SERVICE_ACCOUNT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	key := filepath.Join(t.TempDir(), "key.json")
	// Downloading the OAuth client secret instead of a service account key is a
	// common mistake that otherwise surfaces as an unreadable token error.
	if err := os.WriteFile(key, []byte(`{"type":"authorized_user","client_id":"x"}`), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	_, err := Open(context.Background(), Options{ServiceAccountPath: key})
	assertCode(t, err, errs.CodeAuthMissing)
	if !strings.Contains(err.Error(), "authorized_user") {
		t.Errorf("error = %v, want it to name the credential type that was found", err)
	}
}

func TestAccountReportsWhereTheKeyCameFrom(t *testing.T) {
	c, _ := open(t, &server{doc: sampleDoc()}, Options{Comments: true})
	acct := c.Account()
	if acct.Email != testEmail {
		t.Errorf("email = %q, want the address to share documents with", acct.Email)
	}
	if acct.ProjectID != "test-project" {
		t.Errorf("project = %q, want the project the APIs must be enabled on", acct.ProjectID)
	}
	if acct.Source == "" || acct.KeyPath == "" {
		t.Errorf("account = %+v, want it to say which key is loaded and where it came from", acct)
	}
	if len(acct.Scopes) != 2 {
		t.Errorf("scopes = %v, want the scopes this invocation asked for", acct.Scopes)
	}
}

func assertCode(t *testing.T, err error, want errs.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want code %s", want)
	}
	e, ok := err.(*errs.E)
	if !ok {
		t.Fatalf("error = %v (%T), want an *errs.E — the exit code is derived from it", err, err)
	}
	if e.Code != want {
		t.Fatalf("code = %s, want %s (%v)", e.Code, want, err)
	}
}
