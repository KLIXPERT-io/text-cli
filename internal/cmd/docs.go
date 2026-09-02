package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/gdocs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// docsProvider is the name reported in meta.provider. It matches the fetcher's
// registry name so `text docs read` and `text readability --url <doc>` name the
// same backend.
const docsProvider = "gdocs"

// docsCellWidth is where a comment is shortened for the flat output formats. It
// is display truncation only — the JSON carries the full text, the same
// division of labour as lint.Shorten.
const docsCellWidth = 60

func newDocsCmd() *cobra.Command {
	var (
		serviceAccount string
		timeout        time.Duration
	)

	c := &cobra.Command{
		Use:     "docs",
		Aliases: []string{"gdocs", "doc"},
		Short:   "Read, comment on, and edit Google Docs",
		Long: `docs works with Google Docs: read one as prose, read the comments on it,
reply to them, and edit the text.

It is the only part of this CLI that writes anywhere but stdout, so the write
path is deliberately narrow. ` + "`docs replace`" + ` changes one literal string and
refuses an ambiguous match; ` + "`docs insert`" + ` adds a paragraph. Both pin the
revision they read, so an edit is rejected rather than applied if someone
changed the document in between.

Every analysis command reads a document directly, because a Google Docs URL
routes to this backend automatically:

  text readability --url https://docs.google.com/document/d/<id>/edit
  text lint --url https://docs.google.com/document/d/<id>/edit
  text diff --url <doc-a> --url <doc-b>

Access is per document. text authenticates as a service account, and a service
account can only open a document that has been shared with its address —
exactly like a colleague:

  text config set docs.service_account_path ~/keys/text-cli.json
  text docs whoami        # prints the address to share the document with`,
	}

	read := &cobra.Command{
		Use:   "read <doc>",
		Short: "Read a document as markdown",
		Long: `read fetches a document and prints it as markdown: headings stay headings,
lists stay lists, tables become pipe tables.

--output text prints the document and nothing else, which is what makes it
safe at the head of a pipe. Analysing a document does not need the pipe,
though — --url does the same thing in one process:

  text docs read <doc> --output text | text lint    # works
  text lint --url <doc-url>                         # better

Pending suggestions are left out by default: the score of "the text plus
everything anyone has proposed" is a score of a document that does not exist.
--suggestions accepted reads it the other way.

Examples:
  text docs read https://docs.google.com/document/d/<id>/edit
  text docs read <id> --output text > draft.md
  text docs read <id> --suggestions accepted --output text | text readability`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, err := docsID(args)
			if err != nil {
				return err
			}
			tab, _ := cmd.Flags().GetString("tab")
			suggestions, _ := cmd.Flags().GetString("suggestions")
			if err := gdocs.ValidateSuggestions(suggestions); err != nil {
				return err
			}

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{})
			if err != nil {
				return err
			}
			doc, err := client.Read(cmd.Context(), id, gdocs.ReadOptions{TabID: tab, Suggestions: suggestions})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    doc,
				Meta:    docsMeta(1, 1),
				Columns: []string{"document_id", "title", "revision_id", "tabs", "chars"},
				Rows: []output.Row{{
					"document_id": doc.DocumentID,
					"title":       doc.Title,
					"revision_id": doc.RevisionID,
					"tabs":        len(doc.Tabs),
					"chars":       len([]rune(doc.Content)),
				}},
				Records: []any{doc},
				Text: func(w io.Writer) error {
					_, err := fmt.Fprintln(w, doc.Content)
					return err
				},
			})
		},
	}
	read.Flags().String("tab", "", "read one tab by id (default: every tab)")
	read.Flags().String("suggestions", gdocs.SuggestionsClean,
		"how to treat pending suggestions: "+strings.Join(gdocs.SuggestionModes(), "|"))

	comments := &cobra.Command{
		Use:     "comments <doc>",
		Aliases: []string{"comment-list"},
		Short:   "List the comment threads on a document",
		Long: `comments lists the review threads on a document, newest reply last.

The field that matters is ` + "`quoted`" + `: the passage the comment is anchored to,
copied verbatim out of the document. It is a literal substring, so it is
exactly what ` + "`docs replace --find`" + ` wants — which makes the whole loop one
pipeline:

  text docs comments <doc>                                  # what to fix, and where
  text docs replace <doc> --find "<quoted>" --replace "..."  # fix it
  text docs reply <doc> <comment-id> "done" --resolve        # close the thread

Resolved threads are left out unless you ask for them: an edited document
accumulates them, and the question a comment list is being asked is usually
what is still open.

Examples:
  text docs comments <id>
  text docs comments <id> --include-resolved --output table
  text docs comments <id> --output ndjson | jq -r .quoted`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, err := docsID(args)
			if err != nil {
				return err
			}
			includeResolved, _ := cmd.Flags().GetBool("include-resolved")
			limit, _ := cmd.Flags().GetInt("limit")

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{Comments: true})
			if err != nil {
				return err
			}
			threads, err := client.Comments(cmd.Context(), id, gdocs.CommentOptions{
				IncludeResolved: includeResolved,
				Limit:           limit,
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"document_id": id, "url": gdocs.DocURL(id), "comments": threads},
				Meta:    docsMeta(len(threads), 1),
				Columns: []string{"id", "author", "resolved", "replies", "quoted", "content"},
				Rows:    commentRows(threads),
				Records: commentRecords(threads),
				Text:    func(w io.Writer) error { return writeCommentsText(w, threads) },
			})
		},
	}
	comments.Flags().Bool("include-resolved", false, "include threads that have been resolved")
	comments.Flags().Int("limit", 0, "maximum number of threads to return")

	comment := &cobra.Command{
		Use:   "comment <doc> [text]",
		Short: "Post a comment on a document",
		Long: `comment posts a new comment on the document.

It is a document-level comment, not one anchored to a passage: the anchor the
Google Docs editor writes is an opaque region descriptor that no API can mint.
--quote is the way to point at a passage — it prepends the quoted text to the
comment, which is what a reviewer would have done by hand.

Examples:
  text docs comment <id> "The second section is two reading levels harder than the rest."
  text docs comment <id> --quote "Die Inanspruchnahme" --text "Substantivstil — bitte umformulieren."
  text lint --url <doc-url> --output text | text docs comment <id> --text -`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, rest, err := docsIDAndRest(args)
			if err != nil {
				return err
			}
			flagText, _ := cmd.Flags().GetString("text")
			quote, _ := cmd.Flags().GetString("quote")

			body, err := s.docsBody(rest, flagText)
			if err != nil {
				return err
			}
			if quote != "" {
				body = "> " + strings.ReplaceAll(strings.TrimSpace(quote), "\n", "\n> ") + "\n\n" + body
			}

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{Write: true, Comments: true})
			if err != nil {
				return err
			}
			created, err := client.AddComment(cmd.Context(), id, body)
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    created,
				Meta:    docsMeta(1, 1),
				Columns: []string{"id", "author", "resolved", "replies", "quoted", "content"},
				Rows:    commentRows([]gdocs.Comment{*created}),
				Records: []any{created},
				Text: func(w io.Writer) error {
					_, err := fmt.Fprintf(w, "posted comment %s\n", created.ID)
					return err
				},
			})
		},
	}
	comment.Flags().String("text", "", `the comment text ("-" reads stdin)`)
	comment.Flags().String("quote", "", "passage the comment is about, quoted into it")

	reply := &cobra.Command{
		Use:   "reply <doc> <comment-id> [text]",
		Short: "Reply to a comment thread, optionally resolving it",
		Long: `reply posts into an existing thread. --resolve closes the thread with the
same call, because "fixed in the second paragraph" and the state change are
one event.

Comment ids come from ` + "`text docs comments <doc>`" + `.

Examples:
  text docs reply <id> AAABBBCCC "Rewritten — Flesch went from 31 to 58."
  text docs reply <id> AAABBBCCC "done" --resolve
  text docs reply <id> AAABBBCCC --reopen --text "This came back in the last edit."`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, rest, err := docsIDAndRest(args)
			if err != nil {
				return err
			}
			if len(rest) == 0 {
				return errs.New(errs.CodeInvalidArgs, "no comment id").
					WithHint("List the threads first: text docs comments <doc>.")
			}
			commentID, rest := rest[0], rest[1:]

			flagText, _ := cmd.Flags().GetString("text")
			resolve, _ := cmd.Flags().GetBool("resolve")
			reopen, _ := cmd.Flags().GetBool("reopen")
			if resolve && reopen {
				return errs.New(errs.CodeInvalidArgs, "--resolve and --reopen contradict each other").
					WithHint("Pass one of them.")
			}

			// A resolving reply is commonly just the state change, so empty
			// text is allowed here and rejected by the client only when there
			// is no action either.
			body := strings.Join(rest, " ")
			if flagText != "" {
				if body, err = s.docsBody(rest, flagText); err != nil {
					return err
				}
			}

			action := ""
			switch {
			case resolve:
				action = "resolve"
			case reopen:
				action = "reopen"
			}

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{Write: true, Comments: true})
			if err != nil {
				return err
			}
			posted, err := client.Reply(cmd.Context(), id, commentID, gdocs.ReplyOptions{Content: body, Action: action})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    posted,
				Meta:    docsMeta(1, 1),
				Columns: []string{"id", "author", "action", "content"},
				Rows: []output.Row{{
					"id":      posted.ID,
					"author":  posted.Author,
					"action":  posted.Action,
					"content": oneLine(posted.Content, docsCellWidth),
				}},
				Records: []any{posted},
				Text: func(w io.Writer) error {
					verb := "replied to"
					if posted.Action != "" {
						verb = posted.Action + "d"
					}
					_, err := fmt.Fprintf(w, "%s comment %s\n", verb, commentID)
					return err
				},
			})
		},
	}
	reply.Flags().String("text", "", `the reply text ("-" reads stdin)`)
	reply.Flags().Bool("resolve", false, "resolve the thread with this reply")
	reply.Flags().Bool("reopen", false, "reopen a resolved thread")

	replace := &cobra.Command{
		Use:   "replace <doc>",
		Short: "Replace one exact string in a document",
		Long: `replace changes a literal string in the document.

It is built to be driven by a lint finding. ` + "`text lint`" + ` reports an excerpt
that is exactly the source text at the offsets it names, so the excerpt is the
--find string, and no index arithmetic is involved:

  text lint --url <doc-url> --output ndjson | jq -r .excerpt

Two guards make it safe to hand to an agent:

  * A --find that matches more than once is refused unless --all is passed.
    Applying one review comment must not silently rewrite four other places.
  * The write pins the revision the document had when it was read, so an edit
    is rejected — not applied — if the document changed in between.

The match is literal, case-insensitive by default, and cannot span a paragraph
break. Watch for Docs autocorrect: the document may hold a curly quote or an
em dash where the source had a straight one.

Examples:
  text docs replace <id> --find "Die Inanspruchnahme" --replace "Wer ... nutzt" --dry-run
  text docs replace <id> --find "Die Inanspruchnahme" --replace "Wer ... nutzt"
  text docs replace <id> --find "colour" --replace "color" --all --match-case`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, err := docsID(args)
			if err != nil {
				return err
			}
			find, _ := cmd.Flags().GetString("find")
			replaceWith, _ := cmd.Flags().GetString("replace")
			all, _ := cmd.Flags().GetBool("all")
			matchCase, _ := cmd.Flags().GetBool("match-case")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			tab, _ := cmd.Flags().GetString("tab")

			if !cmd.Flags().Changed("replace") {
				return errs.New(errs.CodeInvalidArgs, "--replace is required").
					WithHint(`Pass --replace "" to delete the matched text.`)
			}

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{Write: true})
			if err != nil {
				return err
			}
			result, err := client.Replace(cmd.Context(), id, gdocs.ReplaceRequest{
				Find:      find,
				Replace:   replaceWith,
				MatchCase: matchCase,
				All:       all,
				DryRun:    dryRun,
				TabID:     tab,
			})
			if err != nil {
				return err
			}
			return emitWriteResult(cmd, result, func(w io.Writer) error {
				if !result.Applied {
					_, err := fmt.Fprintf(w, "dry run: %d occurrence(s) would be replaced\n", result.Occurrences)
					return err
				}
				_, err := fmt.Fprintf(w, "replaced %d occurrence(s)\n", result.Occurrences)
				return err
			})
		},
	}
	replace.Flags().String("find", "", "the exact text to replace")
	replace.Flags().String("replace", "", "what to put in its place")
	replace.Flags().Bool("all", false, "replace every occurrence instead of refusing an ambiguous match")
	replace.Flags().Bool("match-case", false, "make the match case-sensitive")
	replace.Flags().Bool("dry-run", false, "count the matches without writing")
	replace.Flags().String("tab", "", "confine the replacement to one tab")

	insert := &cobra.Command{
		Use:   "insert <doc> [text]",
		Short: "Insert text into a document",
		Long: `insert adds text to the document. It appends a new paragraph by default;
--at start puts it first, --at <index> puts it at a document index, and
--inline joins the neighbouring paragraph instead of starting a new one.

Text comes from the arguments, --text, or stdin with --text -. It is inserted
verbatim: markdown is not converted to Docs formatting, so a "## " arrives as
two hashes and a space.

Examples:
  text docs insert <id> "One more thing: the deadline moved to Friday."
  text docs insert <id> --at start --text "DRAFT — do not circulate."
  text readability --url <doc-url> --output text | text docs insert <id> --text -`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			id, rest, err := docsIDAndRest(args)
			if err != nil {
				return err
			}
			flagText, _ := cmd.Flags().GetString("text")
			at, _ := cmd.Flags().GetString("at")
			inline, _ := cmd.Flags().GetBool("inline")
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			tab, _ := cmd.Flags().GetString("tab")

			body, err := s.docsBody(rest, flagText)
			if err != nil {
				return err
			}

			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{Write: true})
			if err != nil {
				return err
			}
			result, err := client.Insert(cmd.Context(), id, gdocs.InsertRequest{
				Text:   body,
				At:     at,
				Inline: inline,
				DryRun: dryRun,
				TabID:  tab,
			})
			if err != nil {
				return err
			}
			return emitWriteResult(cmd, result, func(w io.Writer) error {
				if !result.Applied {
					_, err := fmt.Fprintf(w, "dry run: %d character(s) would be inserted at %s\n", len([]rune(body)), firstNonEmpty(at, gdocs.AtEnd))
					return err
				}
				_, err := fmt.Fprintf(w, "inserted %d character(s)\n", len([]rune(body)))
				return err
			})
		},
	}
	insert.Flags().String("text", "", `the text to insert ("-" reads stdin)`)
	insert.Flags().String("at", gdocs.AtEnd, "where to insert: end|start|<index>")
	insert.Flags().Bool("inline", false, "join the neighbouring paragraph instead of starting a new one")
	insert.Flags().Bool("dry-run", false, "validate without writing")
	insert.Flags().String("tab", "", "the tab to write to (required when a document has several)")

	whoami := &cobra.Command{
		Use:   "whoami",
		Short: "Print the address to share documents with",
		Long: `whoami prints the service account this CLI authenticates as.

A service account cannot ask for access and there is no consent screen: the
only thing that grants it is somebody sharing the document with this address,
the same way they would share it with a colleague. That is what makes this
command the first one to run — and the one to run again after a not_found.

Examples:
  text docs whoami
  text docs whoami --output text`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			client, err := s.openDocs(cmd.Context(), serviceAccount, timeout, gdocs.Options{})
			if err != nil {
				return err
			}
			acct := client.Account()

			return emitResult(cmd, emitOpts{
				// whoami calls nothing: the address comes out of the key
				// file on disk.
				Data:    acct,
				Meta:    docsMeta(0, 0),
				Columns: []string{"email", "project_id", "key_path", "source"},
				Rows: []output.Row{{
					"email":      acct.Email,
					"project_id": acct.ProjectID,
					"key_path":   acct.KeyPath,
					"source":     acct.Source,
				}},
				Records: []any{acct},
				Text: func(w io.Writer) error {
					if acct.Email == "" {
						_, err := fmt.Fprintln(w, "application default credentials — no service account address to share with")
						return err
					}
					_, err := fmt.Fprintln(w, acct.Email)
					return err
				},
			})
		},
	}

	pf := c.PersistentFlags()
	pf.StringVar(&serviceAccount, "service-account", "", "path to a Google service account key (overrides config and env)")
	pf.DurationVar(&timeout, "timeout", gdocs.DefaultTimeout, "per-request timeout")

	c.AddCommand(read, comments, comment, reply, replace, insert, whoami)
	return c
}

// docsID resolves the single document argument.
func docsID(args []string) (string, error) {
	id, rest, err := docsIDAndRest(args)
	if err != nil {
		return "", err
	}
	if len(rest) > 0 {
		return "", errs.Newf(errs.CodeInvalidArgs, "expected one document, got %d arguments", len(args)).
			WithHint("Each docs command works on one document. Analysing several at once is what --url is for: text readability --url <a> --url <b>.")
	}
	return id, nil
}

// docsIDAndRest splits the document argument off the front.
func docsIDAndRest(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errs.New(errs.CodeInvalidArgs, "no document given").
			WithHint("Pass a document URL or id: text docs read https://docs.google.com/document/d/<id>/edit.")
	}
	id, err := gdocs.ParseDocID(args[0])
	if err != nil {
		return "", nil, err
	}
	return id, args[1:], nil
}

// docsBody resolves the text a write command writes.
//
// It deliberately does not go through State.LoadInput. That path reduces
// markup to prose before anything sees it, which is right for every command
// that measures a document and wrong for every command that writes one: a
// heading typed as "## Rollout" must reach the document as "## Rollout", not as
// "Rollout". So the text is read raw, and "-" is the explicit opt-in to stdin.
func (s *State) docsBody(args []string, flagText string) (string, error) {
	switch {
	case flagText == "-":
		items, err := input.Load(input.Options{
			Files: []string{"-"},
			// Never decode here: this is the write path, and the bytes the
			// user piped in are what lands in the document.
			From:     input.FromText,
			Format:   input.FormatText,
			MaxBytes: s.MaxBytes,
		})
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "", errs.New(errs.CodeEmptyInput, "nothing on stdin").
				WithHint("Pipe the text in, or pass it as an argument.")
		}
		return items[0].Text, nil
	case flagText != "":
		return flagText, nil
	case len(args) > 0:
		return strings.Join(args, " "), nil
	}
	return "", errs.New(errs.CodeInvalidArgs, "no text given").
		WithHint(`Pass it as an argument, with --text, or on stdin with --text -.`)
}

// serviceAccountPath resolves the Google credential for the docs feature.
//
// Flag, then env, then config, the same layering as `text entities` — exporting
// TEXT_SERVICE_ACCOUNT in CI must override a stored config without editing the
// file. The entities key is the last fallback on purpose: one machine has one
// Google credential, and a user who already configured entity extraction should
// not have to configure a second thing to read a document.
func (s *State) serviceAccountPath(explicit string) string {
	var docsPath, entitiesPath string
	if s.Cfg != nil {
		docsPath = config.ExpandHome(s.Cfg.Docs.ServiceAccountPath)
		entitiesPath = config.ExpandHome(s.Cfg.Entities.ServiceAccountPath)
	}
	return firstNonEmpty(
		explicit,
		os.Getenv("TEXT_SERVICE_ACCOUNT"),
		os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
		docsPath,
		entitiesPath,
	)
}

// openDocsFn is the constructor the commands call. It is a var so tests can
// substitute a client without a credential or a network.
var openDocsFn = gdocs.Open

// openDocs fills in the credential and the timeout, leaving the caller to
// declare only what it needs: write access, comment access, or neither.
func (s *State) openDocs(ctx context.Context, serviceAccount string, timeout time.Duration, opts gdocs.Options) (*gdocs.Client, error) {
	opts.ServiceAccountPath = s.serviceAccountPath(serviceAccount)
	opts.Timeout = timeout
	return openDocsFn(ctx, opts)
}

// docsMeta is the envelope metadata for a docs call.
//
// Nothing here is cached: reading a document costs nothing and the document may
// be being typed into right now, so a stored copy would only ever be a way to
// be wrong. api_calls is counted honestly — a write is two calls, because it
// reads the document first to count the matches and to learn the revision it
// pins itself to.
func docsMeta(documents, apiCalls int) output.Meta {
	return output.Meta{
		Provider:  docsProvider,
		Documents: documents,
		APICalls:  apiCalls,
	}
}

// emitWriteResult renders what a mutation did, in every format.
func emitWriteResult(cmd *cobra.Command, r *gdocs.WriteResult, text func(io.Writer) error) error {
	// A write is a read plus a batch update, unless the dry run stopped after
	// the read.
	calls := 2
	if !r.Applied {
		calls = 1
	}
	return emitResult(cmd, emitOpts{
		Data:    r,
		Meta:    docsMeta(1, calls),
		Columns: []string{"document_id", "applied", "occurrences", "revision_id"},
		Rows: []output.Row{{
			"document_id": r.DocumentID,
			"applied":     r.Applied,
			"occurrences": r.Occurrences,
			"revision_id": r.RevisionID,
		}},
		Records: []any{r},
		Text:    text,
	})
}

func commentRows(threads []gdocs.Comment) []output.Row {
	rows := []output.Row{}
	for _, t := range threads {
		rows = append(rows, output.Row{
			"id":       t.ID,
			"author":   t.Author,
			"resolved": t.Resolved,
			"replies":  t.ReplyCount,
			"quoted":   oneLine(t.Quoted, docsCellWidth),
			"content":  oneLine(t.Content, docsCellWidth),
		})
	}
	return rows
}

func commentRecords(threads []gdocs.Comment) []any {
	records := make([]any, 0, len(threads))
	for i := range threads {
		records = append(records, threads[i])
	}
	return records
}

// writeCommentsText prints a thread the way a reviewer wrote it: the quoted
// passage, the comment, then the replies underneath.
func writeCommentsText(w io.Writer, threads []gdocs.Comment) error {
	if len(threads) == 0 {
		_, err := fmt.Fprintln(w, "no open comments")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, t := range threads {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		state := ""
		if t.Resolved {
			state = " (resolved)"
		}
		fmt.Fprintf(tw, "%s\t%s%s\n", t.ID, t.Author, state)
		if t.Quoted != "" {
			fmt.Fprintf(tw, "  > %s\n", oneLine(t.Quoted, textWrapWidth))
		}
		fmt.Fprintf(tw, "  %s\n", t.Content)
		for _, r := range t.Replies {
			label := r.Author
			if r.Action != "" {
				label += " [" + r.Action + "]"
			}
			fmt.Fprintf(tw, "    ↳ %s: %s\n", label, r.Content)
		}
	}
	return tw.Flush()
}

// oneLine flattens and shortens a string for the flat output formats. Display
// truncation only: the JSON keeps the full text.
func oneLine(s string, max int) string {
	flat := strings.Join(strings.Fields(s), " ")
	if len([]rune(flat)) <= max {
		return flat
	}
	return string([]rune(flat)[:max]) + "…"
}
