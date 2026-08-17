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
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// sentimentDoc is one document's worth of sentiment. Like entityDoc it is the
// contractual JSON shape: a single document renders it directly as `data`, a
// batch renders a list of them under `data.documents`, so a consumer parses one
// struct either way.
type sentimentDoc struct {
	ID        string                     `json:"id"`
	Provider  string                     `json:"provider"`
	Language  string                     `json:"language,omitempty"`
	Score     float64                    `json:"score"`
	Magnitude float64                    `json:"magnitude"`
	Label     string                     `json:"label"`
	Sentences []entity.SentenceSentiment `json:"sentences,omitempty"`
}

func newSentimentCmd() *cobra.Command {
	var (
		provider       string
		serviceAccount string
		sentences      bool
		timeout        time.Duration
	)

	c := &cobra.Command{
		Use:     "sentiment [text...]",
		Aliases: []string{"sent"},
		Short:   "Score how a text feels: polarity, strength, and a label",
		Long: `sentiment sends each document to a provider that supports sentiment analysis
and reports how the text feels.

Two numbers, and they answer different questions:

  score      -1.0 (negative) to +1.0 (positive) — which way the text leans.
             It is an average, so opposing parts cancel out.
  magnitude  0 to +inf — how much feeling there is in total, in either
             direction. It is not normalized and grows with length, so it is
             only comparable between documents of similar size.

label is derived from both, and the pair is why: a document that is half
furious and half delighted averages to a score near zero, exactly like a
document with no feeling at all. Magnitude tells them apart — near-zero score
with high magnitude is "mixed", near-zero score with low magnitude is
"neutral".

--sentences (on by default) adds the per-sentence breakdown to JSON. Passing it
explicitly also switches CSV, table, and text output to one row per sentence;
by default those stay one row per document. The provider is always asked for
sentences and the full answer is cached, so toggling the flag never costs
another call.

Results are cached: provider calls are billed per request, so re-running the
same text costs nothing. Use --refresh to force a fresh call.

Examples:
  text sentiment "The service was fast, but the room was filthy."
  cat review.md | text sentiment --output text
  text sentiment --file review.md --sentences --output csv
  jq -c '{id, text}' reviews.jsonl | text sentiment --input-format jsonl --output ndjson`,
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
			if closer, ok := p.(io.Closer); ok {
				defer closer.Close()
			}
			// Sentiment is a capability, not something every backend has. A
			// provider that cannot do it is refused here, by name, with a hint
			// listing the ones that can.
			analyzer, err := entity.RequireSentiment(p)
			if err != nil {
				return err
			}

			lang := resolveEntityLanguage(cmd, s)
			opts := cnlOptions(s, serviceAccount, lang, timeout)

			docs := make([]sentimentDoc, 0, len(items))
			var (
				apiCalls  int
				cacheHits int
				truncated bool
				oldest    *cache.Entry
			)

			for _, item := range items {
				res, entry, err := s.analyzeSentiment(cmd, analyzer, item.Text, name, lang, opts)
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
				if !sentences {
					res = res.WithoutSentences()
				}
				docs = append(docs, sentimentDoc{
					ID:        item.ID,
					Provider:  name,
					Language:  res.Language,
					Score:     res.Score,
					Magnitude: res.Magnitude,
					Label:     res.Label,
					Sentences: res.Sentences,
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

			// The rectangular formats stay one row per document unless the user
			// asked for sentences explicitly. --sentences defaults to true, so
			// keying off the value alone would mean a plain `--output csv` never
			// shows a document-level score at all.
			perSentence := sentences && cmd.Flags().Changed("sentences")
			columns := []string{"doc_id", "score", "magnitude", "label", "language"}
			rows := sentimentRows(docs)
			if perSentence {
				columns = []string{"doc_id", "sentence", "score", "magnitude", "text"}
				rows = sentenceRows(docs)
			}

			return emitResult(cmd, emitOpts{
				Data:    data,
				Meta:    meta,
				Columns: columns,
				Rows:    rows,
				Records: sentimentRecords(docs, items),
				Text:    func(w io.Writer) error { return writeSentimentText(w, docs, perSentence) },
			})
		},
	}

	f := c.Flags()
	f.StringVar(&provider, "provider", "", "sentiment provider: "+strings.Join(entity.SentimentProviders(), "|")+" (default: config entities.provider, else google)")
	f.StringVar(&serviceAccount, "service-account", "", "path to a Google service account key (overrides config and env)")
	f.BoolVar(&sentences, "sentences", true, "include the per-sentence breakdown; passing it explicitly switches csv/table/text to one row per sentence")
	f.DurationVar(&timeout, "timeout", entity.DefaultTimeout, "per-request timeout")
	return c
}

// cnlOptions builds the provider Options every Cloud Natural Language command
// sends: the same credential layering `text entities` uses — flag, then env,
// then config — so one credential set works for the whole CLI. Config comes
// last on purpose: exporting TEXT_SERVICE_ACCOUNT in CI must override a
// developer's stored config without editing the file. An empty path falls
// through to Application Default Credentials.
func cnlOptions(s *State, serviceAccount, lang string, timeout time.Duration) entity.Options {
	configured := ""
	if s.Cfg != nil {
		configured = config.ExpandHome(s.Cfg.Entities.ServiceAccountPath)
	}
	return entity.Options{
		Language: lang,
		ServiceAccountPath: firstNonEmpty(
			serviceAccount,
			os.Getenv("TEXT_SERVICE_ACCOUNT"),
			os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"),
			configured,
		),
		Timeout: timeout,
	}
}

// analyzeSentiment returns one document's sentiment, from cache when possible.
//
// Sentiment calls are BILLED PER REQUEST, exactly like entity calls, so the
// caching rule is the same: the key covers only what the provider saw
// (provider, language, text), never the rendering flags, so toggling
// --sentences re-renders a cached payload instead of paying again.
func (s *State) analyzeSentiment(cmd *cobra.Command, a entity.SentimentAnalyzer, text, providerName, lang string, opts entity.Options) (*entity.SentimentResult, *cache.Entry, error) {
	key := cache.Key("sentiment", []string{providerName, lang, text}, "", "")
	ttl := s.TTLFor(s.Cfg.EntitiesTTL())

	if s.Cache != nil && !s.NoCache && !s.Refresh {
		if entry, err := s.Cache.Get(key); err == nil && entry != nil {
			var res entity.SentimentResult
			if err := json.Unmarshal(entry.Payload, &res); err == nil {
				return &res, entry, nil
			}
			// A corrupt payload is not worth failing over: fall through and pay
			// for a fresh call.
		}
	}

	res, err := a.AnalyzeSentiment(cmd.Context(), text, opts)
	if err != nil {
		return nil, nil, err
	}
	if res == nil {
		res = &entity.SentimentResult{Provider: providerName, Label: entity.Label(0, 0)}
	}
	if res.Provider == "" {
		res.Provider = providerName
	}
	// A provider that filled the numbers but not the label gets the derived one,
	// so the label rule lives in exactly one place for every backend.
	if res.Label == "" {
		res.Label = entity.Label(res.Score, res.Magnitude)
	}

	if s.Cache != nil && !s.NoCache {
		if payload, err := json.Marshal(res); err == nil {
			_ = s.Cache.Put(key, payload, ttl)
		}
	}
	return res, nil, nil
}

// sentimentRows is the default CSV/table shape: one row per document.
func sentimentRows(docs []sentimentDoc) []output.Row {
	rows := make([]output.Row, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, output.Row{
			"doc_id":    d.ID,
			"score":     d.Score,
			"magnitude": d.Magnitude,
			"label":     d.Label,
			"language":  d.Language,
		})
	}
	return rows
}

// sentenceRows is the CSV/table shape under an explicit --sentences: one row per
// sentence. "sentence" is the sentence's index within its document, which is
// what makes the rows joinable back to the document order; "text" is the
// sentence itself.
func sentenceRows(docs []sentimentDoc) []output.Row {
	rows := []output.Row{}
	for _, d := range docs {
		for i, s := range d.Sentences {
			rows = append(rows, output.Row{
				"doc_id":    d.ID,
				"sentence":  i,
				"score":     s.Score,
				"magnitude": s.Magnitude,
				"text":      s.Text,
			})
		}
	}
	return rows
}

// sentimentRecords builds the NDJSON stream: one document per line — sentiment
// is a per-document verdict, so splitting it further would lose the answer —
// enriched with the input's passthrough fields so a JSONL pipeline can be
// joined back to its source. Computed keys win over passthrough keys.
func sentimentRecords(docs []sentimentDoc, items []input.Item) []any {
	fields := passthroughFields(items)

	records := make([]any, 0, len(docs))
	for _, d := range docs {
		rec := map[string]any{}
		for k, v := range fields[d.ID] {
			rec[k] = v
		}
		if !mergeJSON(rec, d) {
			continue
		}
		rec["doc_id"] = d.ID
		records = append(records, rec)
	}
	return records
}

// passthroughFields indexes the input's extra JSONL fields by document id.
func passthroughFields(items []input.Item) map[string]map[string]any {
	fields := make(map[string]map[string]any, len(items))
	for _, it := range items {
		if len(it.Fields) > 0 {
			fields[it.ID] = it.Fields
		}
	}
	return fields
}

// mergeJSON marshals v and copies its keys into dst, overwriting whatever was
// there: a passthrough input field named "score" must not shadow the computed
// one. It reports false when v cannot be marshalled, which cannot happen for
// the structs here but keeps the caller total.
func mergeJSON(dst map[string]any, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var computed map[string]any
	if err := json.Unmarshal(b, &computed); err != nil {
		return false
	}
	for k, val := range computed {
		dst[k] = val
	}
	return true
}

// writeSentimentText renders one aligned line per document, and — under an
// explicit --sentences — the sentence breakdown indented beneath each one.
func writeSentimentText(w io.Writer, docs []sentimentDoc, perSentence bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	multi := len(docs) > 1
	for _, d := range docs {
		prefix := ""
		if multi {
			prefix = d.ID + "\t"
		}
		fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\n",
			prefix,
			d.Label,
			"score "+formatScore(d.Score),
			"magnitude "+formatScore(d.Magnitude),
			d.Language,
		)
		if !perSentence {
			continue
		}
		for _, s := range d.Sentences {
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				entity.Label(s.Score, s.Magnitude),
				"score "+formatScore(s.Score),
				"magnitude "+formatScore(s.Magnitude),
				collapseWhitespace(s.Text),
			)
		}
	}
	return tw.Flush()
}

// formatScore renders a score at the same four decimals the JSON carries, so
// the human output and the machine output never disagree.
func formatScore(v float64) string { return strconv.FormatFloat(v, 'f', 4, 64) }

// collapseWhitespace flattens a sentence onto one line: a newline inside a
// sentence would break the column alignment of every row after it.
func collapseWhitespace(s string) string { return strings.Join(strings.Fields(s), " ") }
