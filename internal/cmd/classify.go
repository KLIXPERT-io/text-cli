package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/KLIXPERT-io/text-cli/internal/cache"
	"github.com/KLIXPERT-io/text-cli/internal/entity"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// classifyDoc is one document's worth of categories. Same contract as
// entityDoc and sentimentDoc: a single document renders directly as `data`, a
// batch renders a list under `data.documents`.
type classifyDoc struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Language   string            `json:"language,omitempty"`
	Categories []entity.Category `json:"categories"`
}

func newClassifyCmd() *cobra.Command {
	var (
		provider       string
		serviceAccount string
		top            int
		minConfidence  float64
		timeout        time.Duration
	)

	c := &cobra.Command{
		Use:     "classify [text...]",
		Aliases: []string{"cls"},
		Short:   "Sort text into content categories",
		Long: `classify sends each document to a provider that supports text classification
and reports the content categories it belongs to, as taxonomy paths:

  /Computers & Electronics/Software    0.6200
  /Science/Computer Science            0.5100

Confidence is per category and independent: unlike entity salience these do not
sum to 1.0, because a document really can belong to several categories at once.
Split a name on "/" to get the hierarchy.

Classification needs a document with enough context — roughly 20 words or more.
Shorter input is rejected before the call, with invalid_args, rather than
spending a request to be told the same thing less clearly.

Results are cached: provider calls are billed per request, so re-running the
same text costs nothing. Use --refresh to force a fresh call.

Examples:
  text classify --file post.md
  cat post.md | text classify --top 3 --min-confidence 0.3
  text classify --file post.md --output csv
  jq -c '{id, text}' posts.jsonl | text classify --input-format jsonl --output ndjson`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			items, err := s.LoadInput(args)
			if err != nil {
				return err
			}
			if minConfidence < 0 || minConfidence > 1 {
				return errs.Newf(errs.CodeInvalidArgs, "--min-confidence must be between 0 and 1, got %v", minConfidence).
					WithHint("Confidence is per category in (0, 1], and categories do not share a budget the way salience does, so 0.3-0.5 is a reasonable cut-off.")
			}
			// The length guard runs before the provider is even opened: it costs
			// nothing, it is the same answer the API would give, and failing
			// early means a batch with one short document does not bill for the
			// documents ahead of it.
			for _, item := range items {
				if err := entity.CheckClassifiable(item.Text); err != nil {
					return err
				}
			}

			name := firstNonEmpty(provider, s.Cfg.Entities.Provider, entity.ProviderGoogle)
			p, err := entity.Open(name)
			if err != nil {
				return err
			}
			if closer, ok := p.(io.Closer); ok {
				defer closer.Close()
			}
			// Classification is a capability, not something every backend has.
			classifier, err := entity.RequireClassifier(p)
			if err != nil {
				return err
			}

			lang := resolveEntityLanguage(cmd, s)
			opts := cnlOptions(s, serviceAccount, lang, timeout)
			filter := entity.ClassifyFilterOptions{MinConfidence: minConfidence, Top: top}

			docs := make([]classifyDoc, 0, len(items))
			var (
				apiCalls  int
				cacheHits int
				truncated bool
				oldest    *cache.Entry
			)

			for _, item := range items {
				res, entry, err := s.classifyText(cmd, classifier, item.Text, name, lang, opts)
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
				docs = append(docs, classifyDoc{
					ID:         item.ID,
					Provider:   name,
					Language:   filtered.Language,
					Categories: filtered.Categories,
				})
			}

			meta := output.Meta{
				Provider:  name,
				Documents: len(docs),
				APICalls:  apiCalls,
				Truncated: truncated,
			}
			// "cached" means the whole answer came from cache; a partially
			// served batch still cost money.
			if cacheHits > 0 && apiCalls == 0 && oldest != nil {
				meta.Cached = true
				meta.CachedAt = oldest.CachedAt.Format(time.RFC3339)
				sec := int(oldest.Remaining().Seconds())
				meta.TTLRemainingSec = &sec
			}
			meta.Language = lang
			if lang == "" {
				// Unlike entities and sentiment, the v1 ClassifyText response
				// reports no language, so there is nothing to fill Language with
				// here — only the honest flag that detection was left to the
				// provider.
				meta.LanguageDetected = true
			}

			var data any = map[string]any{"documents": docs}
			if len(docs) == 1 {
				data = docs[0]
			}

			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    meta,
				Columns: []string{"doc_id", "category", "confidence"},
				Rows:    classifyRows(docs),
				Records: classifyRecords(docs, items),
				Text:    func(w io.Writer) error { return writeClassifyText(w, docs) },
			})
		},
	}

	f := c.Flags()
	f.StringVar(&provider, "provider", "", "classification provider: "+strings.Join(entity.ClassifierProviders(), "|")+" (default: config entities.provider, else google)")
	f.StringVar(&serviceAccount, "service-account", "", "path to a Google service account key (overrides config and env)")
	f.IntVar(&top, "top", 0, "keep only the N most confident categories (0 = all)")
	f.Float64Var(&minConfidence, "min-confidence", 0, "drop categories below this confidence (0-1)")
	f.DurationVar(&timeout, "timeout", entity.DefaultTimeout, "per-request timeout")
	return c
}

// classifyText returns one document's categories, from cache when possible.
//
// Classification calls are BILLED PER REQUEST like the rest of the Cloud
// Natural Language surface, so the same rule applies: the key covers only what
// the provider saw (provider, language, text) and never --top or
// --min-confidence, so re-filtering a cached answer is free.
func (s *State) classifyText(cmd *cobra.Command, c entity.TextClassifier, text, providerName, lang string, opts entity.Options) (*entity.ClassificationResult, *cache.Entry, error) {
	key := cache.Key("classify", []string{providerName, lang, text}, "", "")
	ttl := s.TTLFor(s.Cfg.EntitiesTTL())

	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			var res entity.ClassificationResult
			if err := json.Unmarshal(entry.Payload, &res); err == nil {
				return &res, entry, nil
			}
			// A corrupt payload is not worth failing over.
		}
	}

	res, err := c.ClassifyText(cmd.Context(), text, opts)
	if err != nil {
		return nil, nil, err
	}
	if res == nil {
		res = &entity.ClassificationResult{Provider: providerName, Categories: []entity.Category{}}
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

// classifyRows flattens every document's categories into one row per category,
// so CSV and table stay rectangular across a batch.
func classifyRows(docs []classifyDoc) []output.Row {
	rows := []output.Row{}
	for _, d := range docs {
		for _, c := range d.Categories {
			rows = append(rows, output.Row{
				"doc_id":     d.ID,
				"category":   c.Name,
				"confidence": c.Confidence,
			})
		}
	}
	return rows
}

// classifyRecords is the NDJSON stream: one category per line, tagged with its
// document id and enriched with the input's passthrough fields. Computed keys
// win over passthrough keys, so an input field named "confidence" cannot
// redefine the classifier's.
func classifyRecords(docs []classifyDoc, items []input.Item) []any {
	fields := passthroughFields(items)

	records := []any{}
	for _, d := range docs {
		for _, c := range d.Categories {
			rec := map[string]any{}
			for k, v := range fields[d.ID] {
				rec[k] = v
			}
			if !mergeJSON(rec, c) {
				continue
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

// writeClassifyText renders one aligned line per category.
func writeClassifyText(w io.Writer, docs []classifyDoc) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	multi := len(docs) > 1
	for _, d := range docs {
		for _, c := range d.Categories {
			prefix := ""
			if multi {
				prefix = d.ID + "\t"
			}
			fmt.Fprintf(tw, "%s%s\t%s\n", prefix, c.Name, formatScore(c.Confidence))
		}
	}
	return tw.Flush()
}
