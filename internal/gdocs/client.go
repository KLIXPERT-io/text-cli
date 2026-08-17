package gdocs

import (
	"context"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	docs "google.golang.org/api/docs/v1"
	drive "google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// commentFields is the field mask for a comment read.
//
// Drive requires an explicit mask on this method — it refuses the request
// without one — so the mask is a contract, not an optimisation. quotedFileContent
// is the reason to care: it carries the passage the comment is attached to, and
// without it a comment is an opinion about an unknown part of the document.
const commentFields = "nextPageToken,comments(id,content,anchor,resolved,deleted," +
	"createdTime,modifiedTime,assigneeEmailAddress,author(displayName,emailAddress)," +
	"quotedFileContent(value)," +
	"replies(id,content,action,createdTime,deleted,author(displayName,emailAddress)))"

// oneCommentFields is the same mask for a single created comment.
const oneCommentFields = "id,content,anchor,resolved,createdTime,modifiedTime,author(displayName),quotedFileContent(value)"

// Suggestion view modes, as this CLI names them on the command line.
const (
	// SuggestionsClean is the document as it is written today, with pending
	// suggestions left out. It is the default because a readability score of
	// "the text plus everything anyone has proposed" is a score of a document
	// that does not exist.
	SuggestionsClean = "clean"
	// SuggestionsAccepted is the document as it would read if every pending
	// suggestion were accepted.
	SuggestionsAccepted = "accepted"
	// SuggestionsInline is whatever the API's default for this credential is,
	// suggestion markers included.
	SuggestionsInline = "inline"
)

var suggestionModes = map[string]string{
	SuggestionsClean:    "PREVIEW_WITHOUT_SUGGESTIONS",
	SuggestionsAccepted: "PREVIEW_SUGGESTIONS_ACCEPTED",
	SuggestionsInline:   "DEFAULT_FOR_CURRENT_ACCESS",
}

// SuggestionModes lists the accepted --suggestions values, for an error hint.
func SuggestionModes() []string {
	return []string{SuggestionsClean, SuggestionsAccepted, SuggestionsInline}
}

// ValidateSuggestions checks a mode name.
//
// It is exported so a command can reject a typo before opening a client. A
// misspelled flag that first reports "no Google credentials" sends the user to
// fix the wrong thing.
func ValidateSuggestions(mode string) error {
	if mode == "" {
		return nil
	}
	if _, ok := suggestionModes[mode]; !ok {
		return errs.Newf(errs.CodeInvalidArgs, "unknown suggestions mode: %q", mode).
			WithHint("Use --suggestions " + strings.Join(SuggestionModes(), ", ") + ".")
	}
	return nil
}

// Client talks to the Docs and Drive APIs as one identity.
type Client struct {
	docs  *docs.Service
	drive *drive.Service
	acct  Account
	opts  Options
}

// Open resolves credentials and constructs the API clients.
//
// The Drive client is built only when the caller asked for comments: a token
// that can read a user's Drive is not something to hold while rendering a
// document body.
func Open(ctx context.Context, opts Options) (*Client, error) {
	path, source, err := resolveKeyPath(opts.ServiceAccountPath)
	if err != nil {
		return nil, err
	}
	acct, err := loadAccount(path, source)
	if err != nil {
		return nil, err
	}
	acct.Scopes = opts.Scopes()

	clientOpts := []option.ClientOption{option.WithScopes(opts.Scopes()...)}
	if path != "" {
		clientOpts = append(clientOpts, option.WithCredentialsFile(path))
	}
	if opts.Endpoint != "" {
		clientOpts = []option.ClientOption{
			option.WithEndpoint(strings.TrimRight(opts.Endpoint, "/") + "/"),
			option.WithoutAuthentication(),
		}
	}

	c := &Client{acct: acct, opts: opts}
	if c.docs, err = docs.NewService(ctx, clientOpts...); err != nil {
		return nil, translate(err, acct, opts.Write)
	}
	if opts.Comments {
		if c.drive, err = drive.NewService(ctx, clientOpts...); err != nil {
			return nil, translate(err, acct, opts.Write)
		}
	}
	return c, nil
}

// Account returns the identity the client authenticates as.
func (c *Client) Account() Account { return c.acct }

// ReadOptions selects what to read.
type ReadOptions struct {
	// TabID reads one tab. Empty reads every tab, in document order.
	TabID string
	// Suggestions is one of the Suggestions* constants. Empty means clean.
	Suggestions string
}

// Read fetches a document and renders it as markdown.
func (c *Client) Read(ctx context.Context, id string, o ReadOptions) (*Document, error) {
	doc, err := c.get(ctx, id, o.Suggestions)
	if err != nil {
		return nil, err
	}
	tabs := flattenTabs(doc)

	selected := tabs
	if o.TabID != "" {
		selected = nil
		for _, t := range tabs {
			if t.meta.TabID == o.TabID {
				selected = append(selected, t)
			}
		}
		if len(selected) == 0 {
			return nil, errs.Newf(errs.CodeNotFound, "no tab %q in this document", o.TabID).
				WithHint("`text docs read <doc>` lists the document's tabs in meta and in the tabs field.")
		}
	}

	parts := make([]string, 0, len(selected))
	meta := make([]Tab, 0, len(selected))
	for _, t := range selected {
		body := renderMarkdown(t.content, t.lists)
		// A multi-tab document read as one document needs its tab boundaries
		// marked, or two tabs silently become one flow of prose. A single-tab
		// document — nearly all of them — gets no synthetic heading, because
		// that heading would be text the author never wrote.
		if len(selected) > 1 && t.meta.Title != "" {
			body = strings.TrimRight("# "+t.meta.Title+"\n\n"+body, "\n")
		}
		if body != "" {
			parts = append(parts, body)
		}
		meta = append(meta, t.meta)
	}

	mode := o.Suggestions
	if mode == "" {
		mode = SuggestionsClean
	}
	return &Document{
		DocumentID:  doc.DocumentId,
		Title:       doc.Title,
		RevisionID:  doc.RevisionId,
		URL:         DocURL(doc.DocumentId),
		Content:     strings.Join(parts, "\n\n"),
		Tabs:        meta,
		Suggestions: mode,
	}, nil
}

// get performs the documents.get call.
func (c *Client) get(ctx context.Context, id, suggestions string) (*docs.Document, error) {
	if suggestions == "" {
		suggestions = SuggestionsClean
	}
	if err := ValidateSuggestions(suggestions); err != nil {
		return nil, err
	}
	mode := suggestionModes[suggestions]

	ctx, cancel := context.WithTimeout(ctx, c.opts.EffectiveTimeout())
	defer cancel()

	doc, err := c.docs.Documents.Get(id).
		// Without this the response carries only the first tab, and a
		// multi-tab document loses everything after tab one — silently, with
		// a perfectly valid-looking result.
		IncludeTabsContent(true).
		SuggestionsViewMode(mode).
		Context(ctx).
		Do()
	if err != nil {
		return nil, translate(err, c.acct, c.opts.Write)
	}
	return doc, nil
}

// tabContent is one tab's renderable body plus the list definitions its bullets
// refer to. The lists map is per tab, not per document.
type tabContent struct {
	meta    Tab
	content []*docs.StructuralElement
	lists   map[string]docs.List
}

// flattenTabs returns every tab in the order the Docs UI shows them, parents
// before their children.
//
// It falls back to the top-level Body for a document that reports no tabs at
// all, which is what the API returns for documents created before tabs existed.
func flattenTabs(doc *docs.Document) []tabContent {
	var out []tabContent
	var walk func(t *docs.Tab, nesting int)
	walk = func(t *docs.Tab, nesting int) {
		if t == nil {
			return
		}
		if dt := t.DocumentTab; dt != nil && dt.Body != nil {
			meta := Tab{Index: len(out), Nesting: nesting}
			if p := t.TabProperties; p != nil {
				meta.TabID, meta.Title = p.TabId, p.Title
			}
			out = append(out, tabContent{meta: meta, content: dt.Body.Content, lists: dt.Lists})
		}
		for _, child := range t.ChildTabs {
			walk(child, nesting+1)
		}
	}
	for _, t := range doc.Tabs {
		walk(t, 0)
	}
	if len(out) == 0 && doc.Body != nil {
		out = append(out, tabContent{content: doc.Body.Content, lists: doc.Lists})
	}
	return out
}

// CommentOptions filters a comment read.
type CommentOptions struct {
	// IncludeResolved keeps closed threads. They are off by default because a
	// document under review accumulates them, and "what still needs doing" is
	// the question a comment list is usually asked.
	IncludeResolved bool
	// Limit caps the number of threads returned, newest-modified last. Zero
	// means no cap.
	Limit int
}

// Comments lists the document's comment threads.
func (c *Client) Comments(ctx context.Context, id string, o CommentOptions) ([]Comment, error) {
	if c.drive == nil {
		return nil, errs.New(errs.CodeProviderUnavailable, "this client was opened without comment access").
			WithHint("This is a build problem, not a configuration one.")
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.EffectiveTimeout())
	defer cancel()

	out := []Comment{}
	err := c.drive.Comments.List(id).
		Fields(googleapi.Field(commentFields)).
		PageSize(100).
		Context(ctx).
		Pages(ctx, func(page *drive.CommentList) error {
			for _, cm := range page.Comments {
				if cm == nil || cm.Deleted {
					continue
				}
				if cm.Resolved && !o.IncludeResolved {
					continue
				}
				out = append(out, convertComment(cm))
			}
			return nil
		})
	if err != nil {
		return nil, translate(err, c.acct, c.opts.Write)
	}
	if o.Limit > 0 && len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out, nil
}

func convertComment(cm *drive.Comment) Comment {
	out := Comment{
		ID:          cm.Id,
		Content:     cm.Content,
		Resolved:    cm.Resolved,
		Assignee:    cm.AssigneeEmailAddress,
		CreatedTime: cm.CreatedTime,
		ModifiedAt:  cm.ModifiedTime,
		Replies:     []Reply{},
	}
	if cm.Author != nil {
		out.Author = cm.Author.DisplayName
	}
	if cm.QuotedFileContent != nil {
		out.Quoted = cm.QuotedFileContent.Value
	}
	for _, r := range cm.Replies {
		if r == nil || r.Deleted {
			continue
		}
		reply := Reply{ID: r.Id, Content: r.Content, Action: r.Action, CreatedTime: r.CreatedTime}
		if r.Author != nil {
			reply.Author = r.Author.DisplayName
		}
		out.Replies = append(out.Replies, reply)
	}
	out.ReplyCount = len(out.Replies)
	return out
}

// AddComment posts a new comment on the document.
//
// The comment is not anchored to a passage. The Drive API's anchor is an opaque
// region descriptor that only the Docs editor can mint, so a comment created
// through the API attaches to the document as a whole. Quoting the passage in
// the comment text is the way to point at one — which is what `text docs
// comment` does when it is given a --quote.
func (c *Client) AddComment(ctx context.Context, id, content string) (*Comment, error) {
	if strings.TrimSpace(content) == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "a comment needs text").
			WithHint(`Pass it as an argument or with --text: text docs comment <doc> "this paragraph is unclear".`)
	}
	if c.drive == nil {
		return nil, errs.New(errs.CodeProviderUnavailable, "this client was opened without comment access").
			WithHint("This is a build problem, not a configuration one.")
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.EffectiveTimeout())
	defer cancel()

	cm, err := c.drive.Comments.Create(id, &drive.Comment{Content: content}).
		Fields(googleapi.Field(oneCommentFields)).
		Context(ctx).
		Do()
	if err != nil {
		return nil, translate(err, c.acct, true)
	}
	out := convertComment(cm)
	return &out, nil
}

// ReplyOptions is one reply to a comment thread.
type ReplyOptions struct {
	Content string
	// Action is "resolve", "reopen", or empty for a plain reply.
	Action string
}

// Reply posts a reply into a comment thread, optionally resolving it.
//
// Resolving is a reply with an action rather than a separate operation, which
// is Drive's model and a good one: "fixed in the second paragraph" and the
// state change belong to the same event.
func (c *Client) Reply(ctx context.Context, id, commentID string, o ReplyOptions) (*Reply, error) {
	if c.drive == nil {
		return nil, errs.New(errs.CodeProviderUnavailable, "this client was opened without comment access").
			WithHint("This is a build problem, not a configuration one.")
	}
	if strings.TrimSpace(commentID) == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "no comment id").
			WithHint("List the threads first: text docs comments <doc>.")
	}
	if strings.TrimSpace(o.Content) == "" && o.Action == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "a reply needs text or an action").
			WithHint(`Pass text, or use --resolve: text docs reply <doc> <comment-id> "done" --resolve.`)
	}

	ctx, cancel := context.WithTimeout(ctx, c.opts.EffectiveTimeout())
	defer cancel()

	r, err := c.drive.Replies.Create(id, commentID, &drive.Reply{Content: o.Content, Action: o.Action}).
		Fields("id,content,action,createdTime,author(displayName)").
		Context(ctx).
		Do()
	if err != nil {
		return nil, translate(err, c.acct, true)
	}
	out := &Reply{ID: r.Id, Content: r.Content, Action: r.Action, CreatedTime: r.CreatedTime}
	if r.Author != nil {
		out.Author = r.Author.DisplayName
	}
	return out, nil
}

// ReplaceRequest is a literal find-and-replace across the document.
type ReplaceRequest struct {
	Find    string
	Replace string
	// MatchCase makes the search case-sensitive.
	MatchCase bool
	// All permits replacing more than one occurrence. Without it, a Find that
	// matches twice is refused rather than applied twice — see Replace.
	All bool
	// DryRun counts the matches and returns without writing.
	DryRun bool
	// TabID confines the replacement to one tab. Empty covers every tab.
	TabID string
}

// Replace performs a literal find-and-replace.
//
// Two guards make this safe enough to hand to an agent:
//
//   - The document is read first and the matches are counted locally. A Find
//     that matches nothing is a not_found error rather than a successful call
//     that changed nothing, and a Find that matches more than once is refused
//     unless --all was passed. An agent applying a lint finding means one
//     specific passage; silently rewriting four of them is the failure mode
//     worth spending a read to prevent.
//   - The write carries the revision id from that read. If anyone edited the
//     document in between — a human in the browser, another agent — Google
//     rejects the batch instead of applying an edit computed against text that
//     no longer exists.
//
// The match is literal and cannot span a paragraph break: Docs stores each
// paragraph separately and replaceAllText does not cross that boundary.
func (c *Client) Replace(ctx context.Context, id string, r ReplaceRequest) (*WriteResult, error) {
	if r.Find == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "--find is empty").
			WithHint("Pass the exact text to replace. A lint finding's excerpt is exactly this string.")
	}
	if strings.ContainsAny(r.Find, "\n\v") {
		return nil, errs.New(errs.CodeInvalidArgs, "--find spans a line break").
			WithHint("Google Docs matches within one paragraph. Replace one paragraph's text at a time.")
	}

	doc, err := c.get(ctx, id, SuggestionsClean)
	if err != nil {
		return nil, err
	}
	text, err := documentText(doc, r.TabID)
	if err != nil {
		return nil, err
	}
	count := countOccurrences(text, r.Find, r.MatchCase)

	result := &WriteResult{
		DocumentID:  id,
		Occurrences: count,
		RevisionID:  doc.RevisionId,
		URL:         DocURL(id),
	}
	if count == 0 {
		return nil, errs.Newf(errs.CodeNotFound, "no match for %q in %q", short(r.Find), doc.Title).
			WithHint("The text must match the document exactly, including punctuation and the kind of quote and dash Docs autocorrected it to. Read the document first: text docs read " + id + " --output text.")
	}
	if count > 1 && !r.All {
		return nil, errs.Newf(errs.CodeInvalidArgs, "%q matches %d times in %q", short(r.Find), count, doc.Title).
			WithHint("Pass --all to replace every occurrence, or extend --find with surrounding words until it is unique.")
	}
	if r.DryRun {
		return result, nil
	}

	req := &docs.ReplaceAllTextRequest{
		ContainsText: &docs.SubstringMatchCriteria{Text: r.Find, MatchCase: r.MatchCase},
		ReplaceText:  r.Replace,
	}
	if r.TabID != "" {
		req.TabsCriteria = &docs.TabsCriteria{TabIds: []string{r.TabID}}
	}
	resp, err := c.batch(ctx, id, doc.RevisionId, &docs.Request{ReplaceAllText: req})
	if err != nil {
		return nil, err
	}

	result.Applied = true
	// The API's count wins over the local one. They agree in every ordinary
	// case, but the document has parts this CLI does not render — headers,
	// footers, footnotes — and the API replaced text there too.
	for _, reply := range resp.Replies {
		if reply != nil && reply.ReplaceAllText != nil {
			result.Occurrences = int(reply.ReplaceAllText.OccurrencesChanged)
		}
	}
	if resp.WriteControl != nil {
		result.RevisionID = resp.WriteControl.RequiredRevisionId
	}
	return result, nil
}

// Insert positions for InsertRequest.At.
const (
	AtEnd   = "end"
	AtStart = "start"
)

// InsertRequest adds text to a document.
type InsertRequest struct {
	Text string
	// At is "end", "start", or a decimal index into the document.
	At string
	// Inline suppresses the paragraph break that end and start insertions
	// otherwise add, so text joins the neighbouring paragraph instead of
	// becoming its own.
	Inline bool
	DryRun bool
	TabID  string
}

// Insert adds text to a document.
//
// "end" and "start" become a new paragraph by default. Appending to the end of
// a document without a leading newline lands inside the last paragraph, which
// is almost never what "append this note" meant, so the newline is added and
// --inline takes it away.
func (c *Client) Insert(ctx context.Context, id string, r InsertRequest) (*WriteResult, error) {
	if strings.TrimSpace(r.Text) == "" {
		return nil, errs.New(errs.CodeInvalidArgs, "nothing to insert").
			WithHint(`Pass the text as an argument or with --text: text docs insert <doc> "One more thing."`)
	}
	at := r.At
	if at == "" {
		at = AtEnd
	}
	// The position is parsed before the document is read: a typo'd --at is the
	// user's mistake, and reporting it should not cost a request.
	var index int64
	if at != AtEnd && at != AtStart {
		var err error
		if index, err = parseIndex(at); err != nil {
			return nil, err
		}
	}

	doc, err := c.get(ctx, id, SuggestionsClean)
	if err != nil {
		return nil, err
	}
	tabID, err := c.resolveWriteTab(doc, r.TabID)
	if err != nil {
		return nil, err
	}

	text := r.Text
	insert := &docs.InsertTextRequest{}
	switch at {
	case AtEnd:
		if !r.Inline {
			text = "\n" + text
		}
		insert.EndOfSegmentLocation = &docs.EndOfSegmentLocation{TabId: tabID}
	case AtStart:
		if !r.Inline {
			text += "\n"
		}
		insert.Location = &docs.Location{Index: 1, TabId: tabID}
	default:
		insert.Location = &docs.Location{Index: index, TabId: tabID}
	}
	insert.Text = text

	result := &WriteResult{
		DocumentID:  id,
		Occurrences: 1,
		RevisionID:  doc.RevisionId,
		URL:         DocURL(id),
	}
	if r.DryRun {
		return result, nil
	}

	resp, err := c.batch(ctx, id, doc.RevisionId, &docs.Request{InsertText: insert})
	if err != nil {
		return nil, err
	}
	result.Applied = true
	if resp.WriteControl != nil {
		result.RevisionID = resp.WriteControl.RequiredRevisionId
	}
	return result, nil
}

// resolveWriteTab picks the tab a write targets.
//
// A multi-tab document with no --tab is an error rather than a guess: writing
// to tab one because it was first is the kind of default that quietly edits the
// wrong half of a document.
func (c *Client) resolveWriteTab(doc *docs.Document, requested string) (string, error) {
	tabs := flattenTabs(doc)
	if requested != "" {
		for _, t := range tabs {
			if t.meta.TabID == requested {
				return requested, nil
			}
		}
		return "", errs.Newf(errs.CodeNotFound, "no tab %q in this document", requested).
			WithHint("`text docs read <doc>` lists the tabs and their ids.")
	}
	if len(tabs) > 1 {
		names := make([]string, 0, len(tabs))
		for _, t := range tabs {
			label := t.meta.TabID
			if t.meta.Title != "" {
				label = t.meta.Title + " (" + t.meta.TabID + ")"
			}
			names = append(names, label)
		}
		return "", errs.Newf(errs.CodeInvalidArgs, "the document has %d tabs; name the one to write to", len(tabs)).
			WithHint("Pass --tab <id>. Tabs: " + strings.Join(names, ", ") + ".")
	}
	if len(tabs) == 1 {
		return tabs[0].meta.TabID, nil
	}
	return "", nil
}

// batch sends one batchUpdate, pinned to the revision the caller read.
func (c *Client) batch(ctx context.Context, id, revision string, reqs ...*docs.Request) (*docs.BatchUpdateDocumentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.EffectiveTimeout())
	defer cancel()

	resp, err := c.docs.Documents.BatchUpdate(id, &docs.BatchUpdateDocumentRequest{
		Requests:     reqs,
		WriteControl: &docs.WriteControl{RequiredRevisionId: revision},
	}).Context(ctx).Do()
	if err != nil {
		return nil, translate(err, c.acct, true)
	}
	return resp, nil
}

// documentText is the document's literal text, which is what an occurrence
// count has to be computed against. See the comment at the top of markdown.go.
func documentText(doc *docs.Document, tabID string) (string, error) {
	tabs := flattenTabs(doc)
	var sb strings.Builder
	found := tabID == ""
	for _, t := range tabs {
		if tabID != "" && t.meta.TabID != tabID {
			continue
		}
		found = true
		sb.WriteString(renderPlain(t.content, t.lists))
	}
	if !found {
		return "", errs.Newf(errs.CodeNotFound, "no tab %q in this document", tabID).
			WithHint("`text docs read <doc>` lists the tabs and their ids.")
	}
	return sb.String(), nil
}

// countOccurrences counts non-overlapping matches, the way replaceAllText does.
func countOccurrences(text, find string, matchCase bool) int {
	if find == "" {
		return 0
	}
	if !matchCase {
		text, find = strings.ToLower(text), strings.ToLower(find)
	}
	return strings.Count(text, find)
}

// short truncates a search string for an error message. The whole paragraph a
// user pasted into --find does not belong in a one-line error.
func short(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// parseIndex reads a numeric --at.
func parseIndex(v string) (int64, error) {
	var n int64
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, errs.Newf(errs.CodeInvalidArgs, "unknown --at value: %q", v).
				WithHint("Use --at end, --at start, or --at <index>.")
		}
		n = n*10 + int64(r-'0')
	}
	// Index 0 is the segment itself rather than a position inside it, and the
	// API rejects it. Saying so here is a better answer than relaying a 400.
	if n < 1 {
		return 0, errs.New(errs.CodeInvalidArgs, "--at 0 is not a position in the document").
			WithHint("The first character is index 1. Use --at start for the beginning.")
	}
	return n, nil
}
