// Package gdocs reads and edits Google Docs through a service account.
//
// It is the first capability in this repo that writes to something other than
// stdout, and that shapes every decision here. A wrong readability score is a
// number to argue with; a wrong edit is somebody's document. So the write path
// is deliberately narrow — replace a literal string, insert a paragraph — and
// every write carries the revision the document had when it was read, so an
// edit computed against a stale copy is refused by Google rather than applied
// to a document that moved underneath it.
//
// Two APIs are involved and neither is optional. Text lives in the Docs API
// (docs.googleapis.com); comments do not exist there at all and come from the
// Drive API (drive.googleapis.com, the `comments` and `replies` resources).
// That split is Google's, not this package's: a caller sees one document with
// its comments attached.
//
// Access is granted per document, by sharing it with the service account's
// email address the way you would share it with a colleague. There is no
// consent screen and no way for the CLI to grant itself access, which is why
// almost every error path in this package ends in the same place: print the
// address, ask the user to share the document with it.
package gdocs

import (
	"regexp"
	"strings"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultTimeout bounds one API call. Docs and Drive are fast when healthy;
// this is generous enough for a large document and short enough that a hung
// call does not look like a hung CLI.
const DefaultTimeout = 30 * time.Second

// OAuth scopes, split by what the command actually does.
//
// A read command asks for read-only scopes so that a `text docs read` cannot
// modify a document even if this package had a bug: the token it holds is not
// permitted to. Comments live in Drive, so a command that touches them needs a
// Drive scope on top of the Docs one.
//
// drive.file is deliberately not used. It grants access only to files the app
// itself created or that were opened through the Google Picker, and a document
// shared directly with a service account is neither — the call would fail with
// a 404 that looks exactly like "you forgot to share it".
const (
	ScopeDocumentsRead = "https://www.googleapis.com/auth/documents.readonly"
	ScopeDocuments     = "https://www.googleapis.com/auth/documents"
	ScopeDriveRead     = "https://www.googleapis.com/auth/drive.readonly"
	ScopeDrive         = "https://www.googleapis.com/auth/drive"
)

// Document is one Google Doc, reduced to what this CLI needs.
//
// Content is markdown for the same reason fetch.Page.Content is: every command
// in this repo reduces markup to prose in one place, and a heading flattened
// into the sentence after it would move a readability score. Handing the strip
// pass markdown is what makes `text docs read` and `text readability --url`
// agree to the byte.
type Document struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	// RevisionID identifies the version that was read. It is what a follow-up
	// write passes back as writeControl.requiredRevisionId, so an edit computed
	// against this text cannot land on a document that has changed since.
	RevisionID string `json:"revision_id"`
	URL        string `json:"url"`
	// Content is the document body as markdown.
	Content string `json:"content"`
	// Tabs names the tabs the content came from, in document order. A
	// single-tab document — nearly all of them — reports one entry.
	Tabs []Tab `json:"tabs,omitempty"`
	// Suggestions reports which suggestion view the text represents, because
	// "the document" is ambiguous while edits are pending.
	Suggestions string `json:"suggestions,omitempty"`
}

// Tab is one tab of a document.
type Tab struct {
	TabID string `json:"tab_id"`
	Title string `json:"title,omitempty"`
	// Index is the tab's position in the flattened, top-down tab order.
	Index int `json:"index"`
	// Nesting is 0 for a top-level tab and 1+ for a child tab.
	Nesting int `json:"nesting,omitempty"`
}

// Comment is one comment thread on a document.
//
// It is a thread, not a single message: the Drive API models a comment as a
// head post with replies, and a reviewer's "still not clear" three replies down
// is the part worth acting on.
type Comment struct {
	ID     string `json:"id"`
	Author string `json:"author,omitempty"`
	// Content is the comment text as the author typed it.
	Content string `json:"content"`
	// Quoted is the passage of the document the comment is anchored to, when
	// it is anchored to one. This is the field that makes a comment actionable:
	// it is a literal substring of the document, so it can be handed straight
	// to `text docs replace --find`.
	Quoted string `json:"quoted,omitempty"`
	// Resolved reports whether the thread has been closed.
	Resolved bool `json:"resolved"`
	// Assignee is the email a comment was assigned to, when it was assigned.
	Assignee    string  `json:"assignee,omitempty"`
	CreatedTime string  `json:"created_time,omitempty"`
	ModifiedAt  string  `json:"modified_time,omitempty"`
	Replies     []Reply `json:"replies,omitempty"`
	// ReplyCount is len(Replies), carried explicitly so the flat output formats
	// (CSV, table) can show it without the nested list.
	ReplyCount int `json:"reply_count"`
}

// Reply is one message inside a comment thread.
type Reply struct {
	ID      string `json:"id"`
	Author  string `json:"author,omitempty"`
	Content string `json:"content,omitempty"`
	// Action is "resolve" or "reopen" when the reply changed the thread's
	// state, and empty when it was just a message. A resolving reply commonly
	// has no content at all, which is why this field cannot be inferred.
	Action      string `json:"action,omitempty"`
	CreatedTime string `json:"created_time,omitempty"`
}

// WriteResult is what a mutation did. Every write returns one, and every field
// is something the caller could not have known before the call.
type WriteResult struct {
	DocumentID string `json:"document_id"`
	// Applied is false for a dry run: the request was validated and the match
	// counted, but nothing was sent.
	Applied bool `json:"applied"`
	// Occurrences is how many matches a replace changed (or would change).
	Occurrences int `json:"occurrences"`
	// RevisionID is the document's revision after the write, when the API
	// reports one, and the revision the dry run was computed against otherwise.
	RevisionID string `json:"revision_id,omitempty"`
	URL        string `json:"url,omitempty"`
}

// Account describes the identity the CLI is calling as. It exists for one
// reason: a user cannot share a document with an address they cannot see.
type Account struct {
	// Email is the service account's address, read out of the key file. It is
	// empty when Application Default Credentials are in use, because those name
	// no address this CLI can read locally.
	Email string `json:"email,omitempty"`
	// ProjectID is the key's project, which is where the Docs and Drive APIs
	// have to be enabled.
	ProjectID string `json:"project_id,omitempty"`
	// KeyPath is the file the credentials were read from, and Source names the
	// layer that supplied it, so `text docs whoami` can answer "which key is
	// this?" without a guess.
	KeyPath string `json:"key_path,omitempty"`
	Source  string `json:"source,omitempty"`
	// Scopes are the OAuth scopes the current invocation asked for.
	Scopes []string `json:"scopes,omitempty"`
}

// docIDPattern is what a Drive file id looks like. The length floor rejects a
// word a user typed by mistake ("draft") without rejecting a real id.
var docIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,}$`)

// docURLPattern pulls the id out of every Google Docs URL shape in the wild:
// /document/d/<id>/edit, /document/u/0/d/<id>, and the mobile /document/d/<id>
// with no trailing segment.
var docURLPattern = regexp.MustCompile(`/document/(?:u/\d+/)?d/([a-zA-Z0-9_-]+)`)

// otherWorkspaceURL matches the sibling Workspace apps, so a Sheet gets an
// answer about Sheets rather than "not a valid document id".
var otherWorkspaceURL = regexp.MustCompile(`/(spreadsheets|presentation|forms|drawings)/`)

// ParseDocID resolves a document id from a URL or a bare id.
//
// It accepts what a user actually has in their clipboard — the full edit URL,
// with or without a heading fragment — and rejects the neighbouring Workspace
// file types by name. `text docs` is Google Docs only, and "unsupported id"
// would be a confusing way to say "that is a spreadsheet".
func ParseDocID(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", errs.New(errs.CodeInvalidArgs, "no document given").
			WithHint("Pass a document URL or id: text docs read https://docs.google.com/document/d/<id>/edit.")
	}

	if m := otherWorkspaceURL.FindStringSubmatch(v); m != nil {
		kind := map[string]string{
			"spreadsheets": "Google Sheet",
			"presentation": "Google Slides deck",
			"forms":        "Google Form",
			"drawings":     "Google Drawing",
		}[m[1]]
		return "", errs.Newf(errs.CodeInvalidArgs, "that is a %s, not a Google Doc", kind).
			WithHint("text docs only works with Google Docs (docs.google.com/document/...).")
	}

	if m := docURLPattern.FindStringSubmatch(v); m != nil {
		return m[1], nil
	}
	// The legacy ?id= form, still produced by some share dialogs and by Drive
	// search results.
	if i := strings.Index(v, "id="); i >= 0 && strings.Contains(v, "docs.google.com") {
		id := v[i+3:]
		if j := strings.IndexAny(id, "&#"); j >= 0 {
			id = id[:j]
		}
		if docIDPattern.MatchString(id) {
			return id, nil
		}
	}
	if strings.Contains(v, "://") || strings.Contains(v, "/") {
		return "", errs.Newf(errs.CodeInvalidArgs, "not a Google Docs URL: %q", raw).
			WithHint("A document URL looks like https://docs.google.com/document/d/<id>/edit.")
	}
	if !docIDPattern.MatchString(v) {
		return "", errs.Newf(errs.CodeInvalidArgs, "not a document id: %q", raw).
			WithHint("Pass the id from the document URL (the part after /d/), or the whole URL.")
	}
	return v, nil
}

// IsDocURL reports whether a URL is a Google Doc.
//
// It is what the fetch registry's URL routing asks, so it must be cheap and
// must never claim a URL this package cannot actually read: a false positive
// here turns a working Firecrawl scrape into an authentication error.
func IsDocURL(raw string) bool {
	v := strings.TrimSpace(raw)
	if !strings.Contains(v, "docs.google.com") {
		return false
	}
	return docURLPattern.MatchString(v)
}

// DocURL is the canonical address of a document, used as the id of the
// documents this CLI emits and as the URL a fetched page reports.
func DocURL(id string) string {
	return "https://docs.google.com/document/d/" + id + "/edit"
}
