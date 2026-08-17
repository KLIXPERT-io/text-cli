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

func newEntitiesCmd() *cobra.Command {
	var (
		provider       string
		serviceAccount string
		types          string
		minProbability float64
		top            int
		timeout        time.Duration
	)

	c := &cobra.Command{
		Use:     "entities [text...]",
		Aliases: []string{"ents"},
		Short:   "Extract named entities (people, places, organizations) from text",
		Long: `entities sends each document to an entity provider and reports the named
things it found, with the knowledge-base identifiers needed to look them up.

Results are cached: provider calls are billed per request, so re-running the
same text costs nothing. Use --refresh to force a fresh call.

Examples:
  text entities "Ada Lovelace worked with Charles Babbage in London."
  cat post.md | text entities --types PERSON,ORGANIZATION --top 10
  text entities --file post.md --min-probability 0.8 --output csv
  jq -c '{id, text}' posts.jsonl | text entities --input-format jsonl --output ndjson`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			items, err := s.LoadInput(args)
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
			filter := entity.FilterOptions{
				Types:          entity.ParseTypes(types),
				MinProbability: minProbability,
				Top:            top,
			}
			if minProbability < 0 || minProbability > 1 {
				return errs.Newf(errs.CodeInvalidArgs, "--min-probability must be between 0 and 1, got %v", minProbability).
					WithHint("Probability is the confidence of the strongest mention, in (0, 1].")
			}

			docs := make([]entityDoc, 0, len(items))
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

			var data any = map[string]any{"documents": docs}
			if len(docs) == 1 {
				data = docs[0]
			}

			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    meta,
				Columns: []string{"doc_id", "name", "type", "probability", "mentions", "wikipedia_url"},
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
	f.Float64Var(&minProbability, "min-probability", 0, "drop entities below this confidence (0-1)")
	f.IntVar(&top, "top", 0, "keep only the N highest-confidence entities (0 = all)")
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
				"probability":   e.Probability,
				"mentions":      e.MentionCount,
				"wikipedia_url": e.WikipediaURL,
			})
		}
	}
	return rows
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
			fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\n",
				prefix,
				e.Name,
				e.Type,
				strconv.FormatFloat(e.Probability, 'f', 2, 64),
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
