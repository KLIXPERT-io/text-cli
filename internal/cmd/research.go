package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/firecrawl"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/research"
	"github.com/spf13/cobra"
)

func newResearchCmd() *cobra.Command {
	var (
		source  string
		timeout time.Duration
		limit   int
	)

	c := &cobra.Command{
		Use:     "research",
		Aliases: []string{"papers"},
		Short:   "Search scientific literature (arXiv, PubMed, DOI)",
		Long: `research searches a paper index and returns records with their abstracts.

It is the citation counterpart to ` + "`text kb`" + `: kb answers "what is this
thing?" from an encyclopedia, research answers "what has been published about
it?" from the literature.

The abstracts are prose, so the rest of this CLI composes onto them directly:

  text research papers "readability formulas" --output ndjson |
    jq -r .abstract | text readability

  text research paper arxiv:1706.03762 --query "what is attention?"
  text research similar arxiv:1706.03762 --intent "cheaper attention variants"

Results are cached for 24h. The index answers unauthenticated requests; a
Firecrawl API key raises the rate limit but is not required.`,
	}

	papers := &cobra.Command{
		Use:   "papers <query...>",
		Short: "Find papers matching a question",
		Long: `papers searches the index. The query is embedded rather than keyword-matched,
so a question works better than a keyword list.

Examples:
  text research papers "how is text readability measured?"
  text research papers "diffusion image synthesis" --limit 20 --categories cs.LG
  text research papers "attention" --from 2017-01-01 --to 2018-01-01
  text research papers "sepsis biomarkers" --output csv`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			flags := cmd.Flags()

			query, err := s.loadQuery(cmd.Context(), args, "papers")
			if err != nil {
				return err
			}
			authors, _ := flags.GetString("authors")
			categories, _ := flags.GetString("categories")
			from, _ := flags.GetString("from")
			to, _ := flags.GetString("to")

			opts := research.SearchOptions{
				Query:      query,
				Limit:      limit,
				Authors:    authors,
				Categories: categories,
				From:       from,
				To:         to,
				Timeout:    timeout,
			}

			src, name, err := s.openResearchSource(source)
			if err != nil {
				return err
			}

			key := cache.Key("research", []string{
				name, "papers", query, strconv.Itoa(opts.EffectiveLimit()),
				authors, categories, from, to,
			}, "", "")

			var found []research.Paper
			entry, err := s.researchCached(key, &found, func() (any, error) {
				return src.SearchPapers(cmd.Context(), opts)
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"query": query, "papers": found},
				Meta:    researchMeta(name, len(found), entry),
				Columns: paperColumns,
				Rows:    paperRows(found),
				Records: paperRecords(found),
				Text:    func(w io.Writer) error { return writePapersText(w, found) },
			})
		},
	}
	papers.Flags().String("authors", "", "filter by a substring of the byline")
	papers.Flags().String("categories", "", "filter by subject category (e.g. cs.LG)")
	papers.Flags().String("from", "", "earliest publication date (YYYY-MM-DD)")
	papers.Flags().String("to", "", "latest publication date (YYYY-MM-DD)")

	paper := &cobra.Command{
		Use:   "paper <id>",
		Short: "Read one paper by id, with passages answering a question",
		Long: `paper fetches a single record. With --query it also retrieves the passages of
the paper's full text most relevant to that question, which is what makes it
usable as a source rather than just a citation.

Ids are namespaced. A bare number is ambiguous — it is as likely a PMID as an
arXiv id — so the namespace is required:

  arxiv:1706.03762   doi:10.1145/3442188   pmid:18027780   pmcid:PMC1234567

Examples:
  text research paper arxiv:1706.03762
  text research paper arxiv:1706.03762 --query "what is the attention mechanism?"
  text research paper doi:10.1111/j.1742-1241.2010.02408.x --output text`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			query, _ := cmd.Flags().GetString("query")

			src, name, err := s.openResearchSource(source)
			if err != nil {
				return err
			}
			inspector, err := research.RequireInspector(src)
			if err != nil {
				return err
			}
			id, err := research.NormalizeID(args[0])
			if err != nil {
				return err
			}

			opts := research.InspectOptions{Query: query, Limit: limit, Timeout: timeout}
			key := cache.Key("research", []string{
				name, "paper", id, query, strconv.Itoa(limit),
			}, "", "")

			var detail research.PaperDetail
			entry, err := s.researchCached(key, &detail, func() (any, error) {
				return inspector.InspectPaper(cmd.Context(), id, opts)
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data:    detail,
				Meta:    researchMeta(name, 1, entry),
				Columns: paperColumns,
				Rows:    paperRows([]research.Paper{detail.Paper}),
				Records: paperRecords([]research.Paper{detail.Paper}),
				Text:    func(w io.Writer) error { return writePaperDetailText(w, detail) },
			})
		},
	}
	paper.Flags().String("query", "", "retrieve the passages most relevant to this question")

	similar := &cobra.Command{
		Use:     "similar <id>",
		Aliases: []string{"related"},
		Short:   "Find work related to a paper",
		Long: `similar walks out from one paper. --relation picks the direction: work on the
same subject, work that cites it, or its own bibliography.

--intent is required, and it is the point of the command. "Papers like this
one" is ambiguous until you say what makes them alike — the method, the
application, or the dataset — and the ranking is built from that sentence.

Examples:
  text research similar arxiv:1706.03762 --intent "cheaper attention variants"
  text research similar arxiv:1706.03762 --intent "applications to vision" --relation citers
  text research similar arxiv:1706.03762 --intent "sequence models" --relation references`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			intent, _ := cmd.Flags().GetString("intent")
			relation, _ := cmd.Flags().GetString("relation")

			if !research.ValidRelation(relation) {
				return errs.Newf(errs.CodeInvalidArgs, "unknown relation: %q", relation).
					WithHint("Use --relation " + strings.Join(research.Relations(), ", ") + ".")
			}

			src, name, err := s.openResearchSource(source)
			if err != nil {
				return err
			}
			finder, err := research.RequireSimilarFinder(src)
			if err != nil {
				return err
			}
			id, err := research.NormalizeID(args[0])
			if err != nil {
				return err
			}

			opts := research.SimilarOptions{
				Intent:   intent,
				Relation: research.Relation(strings.ToLower(strings.TrimSpace(relation))),
				Limit:    limit,
				Timeout:  timeout,
			}
			key := cache.Key("research", []string{
				name, "similar", id, intent, string(opts.EffectiveRelation()),
				strconv.Itoa(opts.EffectiveLimit()),
			}, "", "")

			var found []research.Paper
			entry, err := s.researchCached(key, &found, func() (any, error) {
				return finder.SimilarPapers(cmd.Context(), id, opts)
			})
			if err != nil {
				return err
			}

			return emitResult(cmd, emitOpts{
				Data: map[string]any{
					"seed":     id,
					"relation": string(opts.EffectiveRelation()),
					"intent":   intent,
					"papers":   found,
				},
				Meta:    researchMeta(name, len(found), entry),
				Columns: paperColumns,
				Rows:    paperRows(found),
				Records: paperRecords(found),
				Text:    func(w io.Writer) error { return writePapersText(w, found) },
			})
		},
	}
	similar.Flags().String("intent", "", "what makes a neighbour interesting (required)")
	similar.Flags().String("relation", string(research.RelationSimilar),
		"direction to walk: "+strings.Join(research.Relations(), "|"))

	pf := c.PersistentFlags()
	pf.StringVar(&source, "source", "", "research source: "+strings.Join(research.Names(), "|"))
	pf.DurationVar(&timeout, "timeout", research.DefaultTimeout, "per-request timeout")
	pf.IntVar(&limit, "limit", research.DefaultLimit, "maximum number of results")

	c.AddCommand(papers, paper, similar)
	return c
}

// openResearchSource resolves --source, then the config, then the registry's
// default, and injects the credential and endpoint.
//
// It returns the resolved name alongside the source because that name is both
// the cache-key component and what meta.provider reports; re-deriving it at
// either call site is how the two drift apart.
func (s *State) openResearchSource(flag string) (research.Source, string, error) {
	name := strings.TrimSpace(flag)
	if name == "" && s.Cfg != nil {
		name = strings.TrimSpace(s.Cfg.Research.Source)
	}
	if name == "" {
		name = research.Default()
	}
	if name == "" {
		return nil, "", errs.New(errs.CodeProviderUnavailable, "no research source is registered").
			WithHint("This is a build problem, not a configuration one.")
	}
	src, err := research.Open(name)
	if err != nil {
		return nil, "", err
	}
	if api, ok := src.(research.APIConfigurer); ok {
		var configured, base string
		if s.Cfg != nil {
			configured = s.Cfg.Firecrawl.APIKey
			base = s.Cfg.Firecrawl.BaseURL
		}
		api.SetAPIKey(firecrawl.ResolveKey(configured))
		api.SetBaseURL(base)
	}
	return src, name, nil
}

// loadQuery resolves a single free-text query from the arguments or stdin, so
// `echo "..." | text research papers` works.
func (s *State) loadQuery(ctx context.Context, args []string, sub string) (string, error) {
	if len(args) > 0 {
		q := strings.TrimSpace(strings.Join(args, " "))
		if q != "" {
			return q, nil
		}
	}
	// A fetched page is not a query, and letting --url through here would send
	// a whole article to a search endpoint.
	st := *s
	st.URLs = nil
	items, err := st.LoadInput(ctx, nil)
	if err != nil {
		if isCode(err, errs.CodeEmptyInput) {
			return "", errs.Newf(errs.CodeEmptyInput, "no query given").
				WithHint(`Pass one: text research ` + sub + ` "your question".`)
		}
		return "", err
	}
	if len(items) > 1 {
		return "", errs.Newf(errs.CodeInvalidArgs, "%s takes one query, got %d", sub, len(items)).
			WithHint("Drop --input-format, or loop in the shell.")
	}
	q := strings.TrimSpace(items[0].Text)
	if q == "" {
		return "", errs.New(errs.CodeEmptyInput, "input contained no query").
			WithHint("Check the upstream command in your pipeline.")
	}
	return q, nil
}

// researchCached is the read-through cache shared by all three subcommands.
//
// The call returns whatever the source returns and it is round-tripped through
// JSON into out, so the cached path and the fresh path decode the same bytes
// into the same shape. A caller cannot accidentally serve a richer object on a
// miss than it serves on a hit.
func (s *State) researchCached(key string, out any, call func() (any, error)) (*cache.Entry, error) {
	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			if err := json.Unmarshal(entry.Payload, out); err == nil {
				return entry, nil
			}
			// A corrupt payload is a miss, not a failure: pay for a fresh call.
		}
	}

	result, err := call()
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, errs.Newf(errs.CodeGeneric, "encode result: %s", err.Error())
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return nil, errs.Newf(errs.CodeGeneric, "decode result: %s", err.Error())
	}
	if s.Cache != nil && !s.NoCache {
		ttl := 24 * time.Hour
		if s.Cfg != nil {
			ttl = s.Cfg.ResearchTTL()
		}
		_ = s.Cache.Put(key, payload, s.TTLFor(ttl))
	}
	return nil, nil
}

func researchMeta(name string, documents int, entry *cache.Entry) output.Meta {
	meta := output.Meta{Provider: name, Documents: documents}
	if entry != nil {
		meta.Cached = true
		meta.CachedAt = entry.CachedAt.Format(time.RFC3339)
		sec := int(entry.Remaining().Seconds())
		meta.TTLRemainingSec = &sec
		return meta
	}
	meta.APICalls = 1
	return meta
}

// paperColumns is the flat shape. The abstract is deliberately absent — a
// paragraph in a CSV cell is unreadable, and --output json is one flag away.
var paperColumns = []string{"id", "title", "authors", "published", "score", "url"}

func paperRows(papers []research.Paper) []output.Row {
	rows := []output.Row{}
	for _, p := range papers {
		rows = append(rows, output.Row{
			"id":        p.PrimaryID,
			"title":     p.Title,
			"authors":   p.Authors,
			"published": p.Published,
			"score":     p.Score,
			"url":       p.URL,
		})
	}
	return rows
}

// paperRecords is the NDJSON stream: one complete record per line, abstract
// included, because that is the form the `| jq -r .abstract | text readability`
// pipeline in the help text consumes.
func paperRecords(papers []research.Paper) []any {
	records := make([]any, 0, len(papers))
	for _, p := range papers {
		records = append(records, p)
	}
	return records
}

func writePapersText(w io.Writer, papers []research.Paper) error {
	for i, p := range papers {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if err := writePaperText(w, p); err != nil {
			return err
		}
	}
	return nil
}

func writePaperText(w io.Writer, p research.Paper) error {
	fmt.Fprintln(w, p.Title)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if p.Authors != "" {
		fmt.Fprintf(tw, "%s\n", p.Authors)
	}
	meta := []string{}
	if p.PrimaryID != "" {
		meta = append(meta, p.PrimaryID)
	}
	if p.Published != "" {
		meta = append(meta, p.Published)
	}
	if len(p.Categories) > 0 {
		meta = append(meta, strings.Join(p.Categories, ", "))
	}
	if len(meta) > 0 {
		fmt.Fprintf(tw, "%s\n", strings.Join(meta, "  ·  "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if p.Abstract != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, wrapText(p.Abstract, textWrapWidth))
	}
	if p.URL != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.URL)
	}
	return nil
}

func writePaperDetailText(w io.Writer, d research.PaperDetail) error {
	if err := writePaperText(w, d.Paper); err != nil {
		return err
	}
	for i, p := range d.Passages {
		if i == 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Passages")
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, wrapText(p.Text, textWrapWidth))
	}
	return nil
}
