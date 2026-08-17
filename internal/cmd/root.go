// Package cmd wires the cobra command tree and shared state.
//
// Every subcommand reads its text through State.LoadInput and writes its result
// through emitResult, so piping, output formats, and the JSON envelope behave
// identically no matter which analysis was asked for. Adding a command means
// adding a file with a newXCmd() constructor and one line in Execute.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/fetch"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/logging"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/strip"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/KLIXPERT-io/text-cli/internal/update"
	"github.com/spf13/cobra"
)

// Shared flag/state carried via context.
type ctxKey string

const stateKey ctxKey = "text.state"

type State struct {
	Cfg          *config.Config
	Cache        *cache.Store
	OutputFormat string
	Lang         string
	NoCache      bool
	Refresh      bool
	CacheTTL     time.Duration

	// Input flags, shared by every analysis command.
	File        string
	InputFormat string
	TextField   string
	IDField     string
	MaxBytes    int64
	Strip       string

	// URL input. --url is a third input source alongside --file and stdin,
	// not a per-command flag: it is resolved inside LoadInput with the other
	// two, so every command reads a web page through one path and none of
	// them can disagree about what the document is.
	URLs        []string
	Fetcher     string
	MainContent bool

	Verbose   bool
	Quiet     bool
	LogFormat string
}

func getState(cmd *cobra.Command) *State {
	v := cmd.Context().Value(stateKey)
	s, _ := v.(*State)
	if s == nil {
		// Defensive: a command run without PersistentPreRunE (tests) still gets
		// usable defaults rather than a nil dereference.
		return &State{Cfg: config.Default()}
	}
	return s
}

// LoadInput resolves the documents to analyse from flags, args, stdin, and
// --url, and reduces markup to prose before anything measures it.
//
// Stripping happens here, once, rather than in each command: scoring a fenced
// code block or a URL as if it were a sentence is simply wrong, and a command
// that forgot to strip would report a confidently incorrect number. Defaulting
// to auto-detection means `cat post.md | text readability` is right without a
// flag; --strip none opts out.
//
// A fetched page goes through the same strip pass as a piped file, which is
// the reason the fetcher returns markdown rather than plain text: the page
// arrives as markup and is reduced by the code that already knows how, instead
// of by a scraper's text extraction that would flatten headings into the
// following sentence.
func (s *State) LoadInput(ctx context.Context, args []string) ([]input.Item, error) {
	items, err := s.loadRaw(ctx, args)
	if err != nil {
		return nil, err
	}
	mode := strip.Mode(firstNonEmpty(s.Strip, string(strip.ModeAuto)))
	if mode == strip.ModeNone {
		return items, nil
	}
	for i := range items {
		stripped := strip.Apply(items[i].Text, mode)
		// Markup that reduces to nothing measurable is left alone: an
		// over-eager strip must never turn a real document into empty input.
		if strings.TrimSpace(stripped) != "" {
			items[i].Text = stripped
		}
	}
	return items, nil
}

// loadRaw resolves the input source before stripping. --url wins over the
// other sources when it is set, so `text entities --url X < some.txt` analyses
// the page rather than silently preferring the redirect.
func (s *State) loadRaw(ctx context.Context, args []string) ([]input.Item, error) {
	if len(s.URLs) == 0 {
		return input.Load(input.Options{
			Args:      args,
			File:      s.File,
			Format:    input.Format(s.InputFormat),
			TextField: s.TextField,
			IDField:   s.IDField,
			MaxBytes:  s.MaxBytes,
		})
	}

	urls, err := s.loadURLs(nil)
	if err != nil {
		return nil, err
	}
	pages, stats, err := s.fetchPages(ctx, urls, s.fetchOptions())
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		if stats.firstErr != nil {
			return nil, stats.firstErr
		}
		return nil, errs.New(errs.CodeEmptyInput, "no page could be fetched").
			WithHint("Check the URL loads in a browser, or try --no-main-content.")
	}

	items := make([]input.Item, 0, len(pages))
	for _, p := range pages {
		if p == nil {
			continue
		}
		text, truncated := capText(p.Content, s.MaxBytes)
		// The URL is the document id, not an index: a batch of three pages is
		// far more useful in NDJSON when each row says which page it came
		// from. Title and URL ride along as passthrough fields for the same
		// reason JSONL input's do — so output can be joined back to its source.
		item := input.Item{ID: p.URL, Text: text, Truncated: truncated}
		item.Fields = map[string]any{"url": p.URL}
		if p.Title != "" {
			item.Fields["title"] = p.Title
		}
		items = append(items, item)
	}
	return items, nil
}

// capText applies --max-bytes to fetched text, which otherwise bypasses the
// limit that every other input source honours.
//
// It cuts on a rune boundary: a page truncated mid-UTF-8-sequence would hand
// the tokenizer an invalid byte, and the resulting word count would be wrong in
// a way nothing downstream could detect.
func capText(s string, max int64) (string, bool) {
	if max <= 0 || int64(len(s)) <= max {
		return s, false
	}
	cut := int(max)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// isCode reports whether err is an *errs.E carrying the given code.
func isCode(err error, code errs.Code) bool {
	var e *errs.E
	return errors.As(err, &e) && e.Code == code
}

// Language returns the requested analysis language, honoring --lang then
// config, and defaulting to auto-detection.
func (s *State) Language() textproc.Language {
	v := firstNonEmpty(s.Lang, os.Getenv("TEXT_LANG"))
	if v == "" && s.Cfg != nil {
		v = s.Cfg.Defaults.Lang
	}
	if v == "" {
		return textproc.LangAuto
	}
	return textproc.Language(strings.ToLower(v))
}

// TTLFor returns the effective cache TTL: --cache-ttl wins, then the caller's
// per-command default.
func (s *State) TTLFor(def time.Duration) time.Duration {
	if s.CacheTTL > 0 {
		return s.CacheTTL
	}
	return def
}

// Execute builds and runs the root command.
func Execute(version string) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &State{}
	root := &cobra.Command{
		Use:   "text",
		Short: "Text analysis CLI — readability, entities, LLM-friendly output",
		Long: `text analyses prose from stdin, a file, or an argument and prints
structured JSON (default), TOON, NDJSON, CSV, or a table.

--output toon emits the same {data, meta} envelope as JSON in TOON
(Token-Oriented Object Notation), a compact encoding that costs 16-40% fewer
tokens on this CLI's own payloads — most on uniform arrays, least when one
long string dominates. Use it when the output is going into an LLM prompt
rather than into jq, which cannot parse it.

Markdown and HTML are reduced to prose before anything is measured, so a
fenced code block or a URL never counts as a sentence. --strip none opts out.

--url fetches a web page and analyses that, so any command that reads a file
also reads a URL. Pages are scraped to clean prose and cached for 24h.

It reads text in and writes data out, so it composes:

  cat post.md | text readability --lang de
  text entities --file post.md --min-salience 0.02 --output csv
  text entities --url https://example.com/post
  text readability --file post.md --output toon
  text lint --url https://example.com/post --output table
  text diff draft1.md draft2.md
  text fetch https://example.com/post --output text
  text lint --url https://docs.google.com/document/d/<id>/edit
  text docs comments <id>
  text research papers "how is readability measured?"
  jq -c '{id, text}' posts.jsonl | text readability --input-format jsonl --output ndjson`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			maybePrintUpdateNotice(cmd, version)
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			st.Cfg = cfg
			if st.OutputFormat == "" && cfg.Defaults.Output != "" {
				st.OutputFormat = cfg.Defaults.Output
			}
			if st.OutputFormat != "" && !output.Valid(st.OutputFormat) {
				return errs.Newf(errs.CodeInvalidArgs, "unknown output format: %q", st.OutputFormat).
					WithHint("Use --output json, toon, ndjson, csv, table, or text.")
			}
			if st.Strip != "" && !strip.Valid(st.Strip) {
				return errs.Newf(errs.CodeInvalidArgs, "unknown strip mode: %q", st.Strip).
					WithHint("Use --strip " + strings.Join(strip.Modes(), ", ") + ".")
			}
			if st.Fetcher != "" && !fetch.Registered(st.Fetcher) {
				return errs.Newf(errs.CodeInvalidArgs, "unknown fetcher: %q", st.Fetcher).
					WithHint("Use --fetcher " + strings.Join(fetch.Names(), ", ") + ".")
			}
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			cacheDir := cfg.Cache.Dir
			if cacheDir == "" {
				cacheDir = filepath.Join(dataDir, "cache")
			}
			st.Cache = cache.New(config.ExpandHome(cacheDir), cfg.TTL())
			st.Cache.HintWriter = func(msg string) {
				if !st.Quiet {
					fmt.Fprintln(os.Stderr, "hint: "+msg)
				}
			}
			logging.Setup(logging.Options{
				Verbose: st.Verbose || cfg.Logging.Verbose,
				Quiet:   st.Quiet,
				Format:  firstNonEmpty(st.LogFormat, cfg.Logging.Format, "text"),
			})
			cmd.SetContext(context.WithValue(cmd.Context(), stateKey, st))
			return nil
		},
	}

	root.Version = version
	pf := root.PersistentFlags()
	pf.StringVar(&st.OutputFormat, "output", "", "output format: json|toon|ndjson|csv|table|text (default: json, or table on TTY)")
	pf.StringVar(&st.Lang, "lang", "", "analysis language: auto|en|de (default: auto)")
	pf.StringVarP(&st.File, "file", "f", "", `read text from a file ("-" for stdin)`)
	pf.StringVar(&st.InputFormat, "input-format", "text", "input format: text|lines|jsonl")
	pf.StringVar(&st.TextField, "text-field", "text", "JSONL field holding the text")
	pf.StringVar(&st.IDField, "id-field", "id", "JSONL field holding the document id")
	pf.Int64Var(&st.MaxBytes, "max-bytes", input.DefaultMaxBytes, "maximum input size in bytes")
	pf.StringVar(&st.Strip, "strip", string(strip.ModeAuto), "reduce markup to prose before analysis: auto|markdown|html|none")
	pf.StringArrayVar(&st.URLs, "url", nil, "fetch a web page and analyse it (repeatable)")
	// No backticks in this usage string: cobra reads a backquoted word as the
	// flag's argument placeholder, so "`text fetch`" renders as "--fetcher text
	// fetch" instead of "--fetcher string".
	pf.StringVar(&st.Fetcher, "fetcher", "", "backend for --url and the fetch command: "+strings.Join(fetch.Names(), "|"))
	pf.BoolVar(&st.MainContent, "main-content", true, "drop nav, headers, and footers from a fetched page")
	pf.BoolVar(&st.NoCache, "no-cache", false, "bypass cache read and write")
	pf.BoolVar(&st.Refresh, "refresh", false, "bypass cache read, write fresh result")
	pf.DurationVar(&st.CacheTTL, "cache-ttl", 0, "override cache TTL for this call (e.g. 30m)")
	pf.BoolVarP(&st.Verbose, "verbose", "v", false, "verbose logs to stderr")
	pf.BoolVarP(&st.Quiet, "quiet", "q", false, "suppress warnings on stderr")
	pf.StringVar(&st.LogFormat, "log-format", "", "log format: text|json (default text)")

	root.AddCommand(
		newReadabilityCmd(),
		newMetricsCmd(),
		newEntitiesCmd(),
		newSentimentCmd(),
		newClassifyCmd(),
		newDiffCmd(),
		newLintCmd(),
		newKBCmd(),
		newFetchCmd(),
		newDocsCmd(),
		newResearchCmd(),
		newConfigCmd(),
		newUpdateCmd(version),
	)

	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		errs.Write(os.Stderr, err)
		return errs.ExitCode(err)
	}
	return 0
}

// maybePrintUpdateNotice emits a one-line notice the first time a newly
// self-updated binary runs. Suppressed under the `update` subtree and via
// TEXT_NO_UPDATE_NOTICE.
func maybePrintUpdateNotice(cmd *cobra.Command, version string) {
	if v := os.Getenv("TEXT_NO_UPDATE_NOTICE"); v != "" && v != "0" && v != "false" {
		return
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "update" && c.HasParent() {
			return
		}
	}
	st, err := update.LoadState()
	if err != nil {
		return
	}
	installed := st.LastInstalledVersion
	if installed == "" || installed == version || installed == "v"+version {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "text: updated to %s (was v%s)\n", installed, version)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// emitOpts describes one command's result in every shape the output package can
// render. A command fills in what it can: Data is required, the rest degrade
// gracefully (CSV without Columns is an error, table without Columns falls back
// to JSON).
type emitOpts struct {
	// Data is the payload of the JSON envelope.
	Data any
	Meta output.Meta
	// Columns and Rows drive CSV and table rendering.
	Columns []string
	Rows    []output.Row
	// Records is the NDJSON stream: one object per line. Defaults to Rows.
	Records []any
	// Text renders the human-readable form for --output text.
	Text func(w io.Writer) error
}

// emitResult renders a command result in the resolved output format.
func emitResult(cmd *cobra.Command, o emitOpts) error {
	s := getState(cmd)
	w := cmd.OutOrStdout()
	f := output.ResolveFormat(s.OutputFormat, os.Stdout.Fd())

	switch f {
	case output.FormatTOON:
		return output.WriteTOON(w, o.Data, o.Meta)
	case output.FormatNDJSON:
		records := o.Records
		if records == nil {
			for _, r := range o.Rows {
				records = append(records, r)
			}
		}
		if records == nil {
			records = []any{o.Data}
		}
		return output.WriteNDJSON(w, records)
	case output.FormatCSV:
		if o.Columns == nil {
			return errs.New(errs.CodeInvalidArgs, "CSV not supported for this command").
				WithHint("Use --output json.")
		}
		return output.WriteCSV(w, o.Columns, o.Rows)
	case output.FormatTable:
		if o.Columns == nil {
			return output.WriteJSON(w, o.Data, o.Meta)
		}
		return output.WriteTable(w, o.Columns, o.Rows)
	case output.FormatText:
		if o.Text == nil {
			return output.WriteJSON(w, o.Data, o.Meta)
		}
		return o.Text(w)
	default:
		return output.WriteJSON(w, o.Data, o.Meta)
	}
}
