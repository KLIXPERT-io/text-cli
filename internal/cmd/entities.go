package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/config"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// entityDoc is one document's worth of entities. It is the contractual JSON
// shape: a single document renders it directly as `data`, a batch renders a
// list of them under `data.documents`, so a consumer parses one struct either
// way.
type entityDoc struct {
	ID                string          `json:"id"`
	Provider          string          `json:"provider"`
	Language          string          `json:"language,omitempty"`
	LanguageSupported bool            `json:"language_supported"`
	Entities          []entity.Entity `json:"entities"`
}

// aggregateData is the `data` payload of `--aggregate`: one corpus-level list
// plus the number of documents it was merged from, which is what makes the
// combined salience numbers readable.
type aggregateData struct {
	Entities  []entity.AggregatedEntity `json:"entities"`
	Documents int                       `json:"documents"`
}

func newEntitiesCmd() *cobra.Command {
	var (
		provider       string
		serviceAccount string
		types          string
		minProbability float64
		minSalience    float64
		sortBy         string
		aggregate      bool
		enrich         bool
		top            int
		timeout        time.Duration
	)

	c := &cobra.Command{
		Use:     "entities [text...]",
		Aliases: []string{"ents"},
		Short:   "Extract named entities (people, places, organizations) from text",
		Long: `entities sends each document to an entity provider and reports the named
things it found, with a salience score and the knowledge-base identifiers
needed to look them up.

Salience is how central an entity is to its document, in [0, 1]. Within one
document the scores sum to about 1.0, so useful thresholds are small: 0.05 is
a strong entity, 0.8 almost never happens.

--aggregate merges the same entity across every input document into one
corpus-level list with a combined salience (the sum of its per-document
salience, bounded by the number of documents, not by 1.0).

Results are cached: provider calls are billed per request, so re-running the
same text costs nothing. Use --refresh to force a fresh call.

Examples:
  text entities "Ada Lovelace worked with Charles Babbage in London."
  cat post.md | text entities --types PERSON,ORGANIZATION --top 10
  text entities --file post.md --min-salience 0.02 --output csv
  jq -c '{id, text}' posts.jsonl | text entities --input-format jsonl --aggregate --top 20`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			items, err := s.LoadInput(cmd.Context(), args)
			if err != nil {
				return err
			}

			name := firstNonEmpty(provider, s.Cfg.Entities.Provider, entity.ProviderGoogle)
			p, err := entity.Open(name)
			if err != nil {
				return err
			}
			// A provider holding a connection releases it here; one that does
			// not simply does not implement io.Closer.
			if closer, ok := p.(io.Closer); ok {
				defer closer.Close()
			}

			lang := resolveEntityLanguage(cmd, s)
			opts := entity.Options{
				Language: lang,
				// Flag, then env, then config — the same layering as the
				// author's other CLIs. Config last matters in CI: exporting
				// TEXT_SERVICE_ACCOUNT must override a developer's stored
				// config without editing the file. google.go falls back to
				// Application Default Credentials when this stays empty.
				ServiceAccountPath: firstNonEmpty(
					serviceAccount,
					os.Getenv("TEXT_SERVICE_ACCOUNT"),
					os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
					config.ExpandHome(s.Cfg.Entities.ServiceAccountPath),
				),
				Timeout: timeout,
			}
			// --min-salience is the name that matches what the Google backend
			// actually reports; --min-probability is the older spelling of the
			// same threshold and stays for scripts that already pass it. An
			// explicit --min-salience wins when both are given.
			minScore := minProbability
			if cmd.Flags().Changed("min-salience") {
				minScore = minSalience
			}
			for _, f := range []struct {
				name string
				val  float64
			}{{"min-salience", minSalience}, {"min-probability", minProbability}} {
				if f.val < 0 || f.val > 1 {
					return errs.Newf(errs.CodeInvalidArgs, "--%s must be between 0 and 1, got %v", f.name, f.val).
						WithHint("The threshold is compared against each entity's score — salience for the Google backend, in (0, 1]. Salience sums to ~1.0 per document, so try 0.01-0.1.")
				}
			}
			order, err := entity.ParseSortBy(sortBy)
			if err != nil {
				return err
			}

			// Filters that decide *which* entities exist run per document, before
			// any merge: --types is part of the merge key, and the salience
			// threshold is a statement about one document's attention. --top is
			// a statement about the final list, so with --aggregate it is applied
			// after merging instead.
			filter := entity.FilterOptions{
				Types:    entity.ParseTypes(types),
				MinScore: minScore,
				Sort:     order,
			}
			if !aggregate {
				filter.Top = top
			}

			docs := make([]entityDoc, 0, len(items))
			filteredResults := make([]*entity.Result, 0, len(items))
			var (
				apiCalls  int
				cacheHits int
				truncated bool
				oldest    *cache.Entry
			)

			for _, item := range items {
				res, entry, err := s.analyzeEntities(cmd, p, item.Text, name, lang, opts)
				if err != nil {
					return err
				}
				if entry != nil {
					cacheHits++
					if oldest == nil || entry.CachedAt.Before(oldest.CachedAt) {
						oldest = entry
					}
				} else {
					apiCalls++
				}
				if item.Truncated {
					truncated = true
				}
				filtered := res.Apply(filter)
				filteredResults = append(filteredResults, filtered)
				docs = append(docs, entityDoc{
					ID:                item.ID,
					Provider:          name,
					Language:          filtered.Language,
					LanguageSupported: filtered.LanguageSupported,
					Entities:          filtered.Entities,
				})
			}

			meta := output.Meta{
				Provider:  name,
				Documents: len(docs),
				APICalls:  apiCalls,
				Truncated: truncated,
			}
			// "cached" means the whole answer came from cache; a partially
			// served batch still cost money, and reporting it as cached would
			// hide that.
			if cacheHits > 0 && apiCalls == 0 && oldest != nil {
				meta.Cached = true
				meta.CachedAt = oldest.CachedAt.Format(time.RFC3339)
				sec := int(oldest.Remaining().Seconds())
				meta.TTLRemainingSec = &sec
			}
			meta.Language = lang
			if lang == "" {
				meta.LanguageDetected = true
				if len(docs) > 0 {
					meta.Language = docs[0].Language
				}
			}

			// Silently doing nothing would be worse than the extra line: the two
			// flags are individually reasonable and only look compatible.
			if enrich && aggregate && !s.Quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: --enrich is ignored with --aggregate (a merged entity has no single article)")
			}

			if aggregate {
				aggs := entity.Aggregate(filteredResults)
				entity.SortAggregated(aggs, order)
				aggs = entity.TopAggregated(aggs, top)
				if aggs == nil {
					aggs = []entity.AggregatedEntity{}
				}
				return emitResult(cmd, emitOpts{
					Data:    aggregateData{Entities: aggs, Documents: len(docs)},
					Meta:    meta,
					Columns: []string{"name", "type", "combined_salience", "avg_salience", "mentions", "documents", "wikipedia_url"},
					Rows:    aggregateRows(aggs),
					Records: aggregateRecords(aggs),
					Text:    func(w io.Writer) error { return writeAggregateText(w, aggs) },
				})
			}

			// --enrich runs here, after filtering and after the --aggregate
			// branch has returned: a merged row belongs to the corpus and has
			// no single entity to hang an article on, and filtering first means
			// no lookup is paid for an entity --top or --types just discarded.
			if enrich {
				return s.emitEnrichedEntities(cmd, docs, items, meta)
			}

			var data any = map[string]any{"documents": docs}
			if len(docs) == 1 {
				data = docs[0]
			}

			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    meta,
				Columns: []string{"doc_id", "name", "type", "salience", "probability", "mentions", "wikipedia_url"},
				Rows:    entityRows(docs),
				Records: entityRecords(docs, items),
				Text:    func(w io.Writer) error { return writeEntitiesText(w, docs) },
			})
		},
	}

	f := c.Flags()
	f.StringVar(&provider, "provider", "", "entity provider: "+strings.Join(entity.Names(), "|")+" (default: config entities.provider, else google)")
	f.StringVar(&serviceAccount, "service-account", "", "path to a Google service account key (overrides config and env)")
	f.StringVar(&types, "types", "", "comma-separated entity types to keep, e.g. PERSON,ORGANIZATION (default: all)")
	f.Float64Var(&minSalience, "min-salience", 0, "drop entities below this per-document salience (0-1; salience sums to ~1.0 per document, so try 0.01-0.1)")
	f.Float64Var(&minProbability, "min-probability", 0, "older name for --min-salience: same threshold, compared against the entity score (salience for the Google backend)")
	f.StringVar(&sortBy, "sort", string(entity.SortSalience), "sort entities by: salience|mentions|name")
	f.BoolVar(&aggregate, "aggregate", false, "merge the same entity across all documents into one list with a combined salience")
	f.BoolVar(&enrich, "enrich", false, "attach the Wikipedia description and lead paragraph to every entity with a wikipedia_url (ignored with --aggregate)")
	f.IntVar(&top, "top", 0, "keep only the N highest-ranked entities, applied after --aggregate merges (0 = all)")
	f.DurationVar(&timeout, "timeout", entity.DefaultTimeout, "per-request timeout")
	return c
}

// analyzeEntities returns one document's result, from cache when possible.
//
// Entity calls are BILLED PER REQUEST. Caching here is not a latency
// optimization, it is the difference between a cheap tool and an expensive one:
// an agent that re-runs the same document in a loop must not re-bill it. The
// key covers exactly the inputs the provider saw (provider, language, text) —
// never the filters, so changing --top or --types re-filters the cached payload
// instead of paying again.
func (s *State) analyzeEntities(cmd *cobra.Command, p entity.Provider, text, providerName, lang string, opts entity.Options) (*entity.Result, *cache.Entry, error) {
	key := cache.Key("entities", []string{providerName, lang, text}, "", "")
	ttl := s.TTLFor(s.Cfg.EntitiesTTL())

	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			var res entity.Result
			if err := json.Unmarshal(entry.Payload, &res); err == nil {
				return &res, entry, nil
			}
			// A corrupt payload is not worth failing over: fall through and
			// pay for a fresh call.
		}
	}

	res, err := p.AnalyzeEntities(cmd.Context(), text, opts)
	if err != nil {
		return nil, nil, err
	}
	if res == nil {
		res = &entity.Result{Provider: providerName, Entities: []entity.Entity{}}
	}
	if res.Provider == "" {
		res.Provider = providerName
	}

	if s.Cache != nil && !s.NoCache {
		if payload, err := json.Marshal(res); err == nil {
			_ = s.Cache.Put(key, payload, ttl)
		}
	}
	return res, nil, nil
}

// resolveEntityLanguage picks the BCP-47/ISO code sent to the provider.
//
// Note this is not the local analyzer language: readability's --lang selects a
// syllable counter, while here it is a hint handed to a remote API. An empty
// result means "auto": the provider detects it.
func resolveEntityLanguage(cmd *cobra.Command, s *State) string {
	// An explicit --lang always wins, including an explicit --lang auto, which
	// is how a user overrides a configured entities.language.
	if cmd.Flags().Changed("lang") {
		if l := s.Language(); l != textproc.LangAuto {
			return string(l)
		}
		return ""
	}
	if s.Cfg != nil {
		if v := strings.TrimSpace(s.Cfg.Entities.Language); v != "" {
			return strings.ToLower(v)
		}
	}
	if l := s.Language(); l != textproc.LangAuto {
		return string(l)
	}
	return ""
}

// entityRows flattens every document's entities into one row per entity, so CSV
// and table output stay rectangular across a batch.
func entityRows(docs []entityDoc) []output.Row {
	rows := []output.Row{}
	for _, d := range docs {
		for _, e := range d.Entities {
			rows = append(rows, output.Row{
				"doc_id":        d.ID,
				"name":          e.Name,
				"type":          e.Type,
				"salience":      e.Salience,
				"probability":   e.Probability,
				"mentions":      e.MentionCount,
				"wikipedia_url": e.WikipediaURL,
			})
		}
	}
	return rows
}

// aggregateRows is the CSV/table shape of `--aggregate`: no doc_id, because a
// merged entity belongs to the corpus rather than to one document.
func aggregateRows(aggs []entity.AggregatedEntity) []output.Row {
	rows := []output.Row{}
	for _, a := range aggs {
		rows = append(rows, output.Row{
			"name":              a.Name,
			"type":              a.Type,
			"combined_salience": a.CombinedSalience,
			"avg_salience":      a.AvgSalience,
			"mentions":          a.Mentions,
			"documents":         a.Documents,
			"wikipedia_url":     a.WikipediaURL,
		})
	}
	return rows
}

// aggregateRecords is the NDJSON stream for `--aggregate`: one merged entity
// per line. Input passthrough fields are deliberately absent — a merged entity
// has no single source row to carry them from.
func aggregateRecords(aggs []entity.AggregatedEntity) []any {
	records := make([]any, 0, len(aggs))
	for _, a := range aggs {
		records = append(records, a)
	}
	return records
}

// writeAggregateText renders one aligned line per merged entity.
func writeAggregateText(w io.Writer, aggs []entity.AggregatedEntity) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, a := range aggs {
		extra := a.WikipediaURL
		if extra == "" {
			extra = a.MID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.Name,
			a.Type,
			strconv.FormatFloat(a.CombinedSalience, 'f', 4, 64),
			"avg "+strconv.FormatFloat(a.AvgSalience, 'f', 4, 64),
			pluralMentions(a.Mentions),
			pluralDocuments(a.Documents),
			extra,
		)
	}
	return tw.Flush()
}

func pluralDocuments(n int) string {
	if n == 1 {
		return "1 document"
	}
	return strconv.Itoa(n) + " documents"
}

// entityRecords builds the NDJSON stream: one full entity object per line,
// tagged with its document id and enriched with the input's passthrough fields
// so a JSONL pipeline can be joined back to its source. Computed keys win over
// passthrough keys — an input field named "type" must not silently redefine the
// entity type.
func entityRecords(docs []entityDoc, items []input.Item) []any {
	fields := make(map[string]map[string]any, len(items))
	for _, it := range items {
		if len(it.Fields) > 0 {
			fields[it.ID] = it.Fields
		}
	}

	records := []any{}
	for _, d := range docs {
		for _, e := range d.Entities {
			rec := map[string]any{}
			for k, v := range fields[d.ID] {
				rec[k] = v
			}
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			var computed map[string]any
			if err := json.Unmarshal(b, &computed); err != nil {
				continue
			}
			for k, v := range computed {
				rec[k] = v
			}
			rec["doc_id"] = d.ID
			rec["provider"] = d.Provider
			if d.Language != "" {
				rec["language"] = d.Language
			}
			records = append(records, rec)
		}
	}
	return records
}

// writeEntitiesText renders one aligned line per entity for --output text.
func writeEntitiesText(w io.Writer, docs []entityDoc) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	multi := len(docs) > 1
	for _, d := range docs {
		for _, e := range d.Entities {
			prefix := ""
			if multi {
				prefix = d.ID + "\t"
			}
			extra := e.WikipediaURL
			if extra == "" {
				extra = e.MID
			}
			// The score column is salience for a backend that reports it and
			// probability for one that reports confidence instead; printing both
			// would leave one of them stuck at 0.0000 forever.
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\n",
				prefix,
				e.Name,
				e.Type,
				strconv.FormatFloat(e.Score(), 'f', 4, 64),
				pluralMentions(e.MentionCount),
				extra,
			)
		}
	}
	return tw.Flush()
}

func pluralMentions(n int) string {
	if n == 1 {
		return "1 mention"
	}
	return strconv.Itoa(n) + " mentions"
}
