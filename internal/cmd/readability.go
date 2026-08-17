package cmd

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/analyze"

	// Metrics register themselves at init time. Importing the package for that
	// side effect here is the whole wiring: a future metric package is one more
	// blank import, not a new command or a switch statement.
	_ "github.com/KLIXPERT-io/text-cli/internal/analyze/readability"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// readabilityDoc is one document's result. It is the unit of the JSON output:
// a single document is emitted as this object, a batch as a list of them, so a
// consumer parses the same shape either way.
type readabilityDoc struct {
	ID               string           `json:"id"`
	Language         string           `json:"language"`
	LanguageDetected bool             `json:"language_detected"`
	Stats            *textproc.Stats  `json:"stats,omitempty"`
	Metrics          []analyze.Result `json:"metrics"`
}

func newReadabilityCmd() *cobra.Command {
	var (
		metricsFlag string
		withStats   bool
	)
	c := &cobra.Command{
		Use:     "readability [text...]",
		Aliases: []string{"read", "rd", "flesch", "amstad"},
		Short:   "Score text for reading ease (Flesch, Amstad)",
		Long: `readability scores prose with the formula calibrated for its language:
Flesch Reading Ease for English, Amstad for German.

With --metrics auto (the default) the language is resolved per document, so a
mixed-language JSONL batch is scored correctly row by row.

Examples:
  cat post.md | text readability
  text readability --file post.de.md --lang de --output text
  text readability "Short words help." --metrics flesch
  jq -c '{id, text}' posts.jsonl | text readability --input-format jsonl --output ndjson

Invoked as ` + "`text flesch`" + ` or ` + "`text amstad`" + ` it defaults to that one metric.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReadability(cmd, args, metricsFlag, withStats)
		},
	}
	c.Flags().StringVar(&metricsFlag, "metrics", "", `metrics to compute: comma-separated names, "auto", or "all" (default: config defaults.metrics, else auto)`)
	c.Flags().BoolVar(&withStats, "stats", true, "include token statistics (words, sentences, syllables, averages)")
	return c
}

func runReadability(cmd *cobra.Command, args []string, metricsFlag string, withStats bool) error {
	s := getState(cmd)

	items, err := s.LoadInput(args)
	if err != nil {
		return err
	}
	sel := metricsSelection(cmd, s, metricsFlag)
	lang := s.Language()

	docs := make([]readabilityDoc, 0, len(items))
	rows := make([]output.Row, 0, len(items))
	records := make([]any, 0, len(items))
	columns := readabilityColumns(withStats)
	seenMetric := map[string]bool{}
	truncated := false

	for _, it := range items {
		d, err := textproc.Analyze(it.Text, lang)
		if err != nil {
			return rdDocumentErr(err, it.ID, len(items))
		}
		// Resolved per document, not once per run: a JSONL batch may mix
		// languages, and each row deserves the formula built for it.
		selected, err := resolveMetrics(sel, d.Language)
		if err != nil {
			return rdDocumentErr(err, it.ID, len(items))
		}

		results := make([]analyze.Result, 0, len(selected))
		row := output.Row{"id": it.ID, "language": string(d.Language)}
		if withStats {
			row["words"] = d.Stats.Words
			row["sentences"] = d.Stats.Sentences
			row["syllables"] = d.Stats.Syllables
			row["avg_sentence_length"] = rdRound(d.Stats.AvgSentenceLength, 3)
			row["avg_syllables_per_word"] = rdRound(d.Stats.AvgSyllablesPerWord, 3)
		}
		for _, m := range selected {
			r, err := m.Compute(d)
			if err != nil {
				return rdDocumentErr(err, it.ID, len(items))
			}
			results = append(results, r)
			row[r.Metric] = r.Score
			row[r.Metric+"_level"] = r.Level
			if !seenMetric[r.Metric] {
				seenMetric[r.Metric] = true
				columns = append(columns, r.Metric, r.Metric+"_level")
			}
		}

		doc := readabilityDoc{
			ID:               it.ID,
			Language:         string(d.Language),
			LanguageDetected: d.Detected,
			Metrics:          results,
		}
		if withStats {
			stats := d.Stats
			doc.Stats = &stats
		}
		docs = append(docs, doc)
		rows = append(rows, row)
		records = append(records, rdWithFields(row, it.Fields))
		truncated = truncated || it.Truncated
	}

	// A single document is the document object itself; a batch is a list under
	// "documents". LoadInput never returns zero items, but the empty case falls
	// into the batch shape rather than panicking.
	var data any = map[string]any{"documents": docs}
	if len(docs) == 1 {
		data = docs[0]
	}

	return emitResult(cmd, emitOpts{
		Data:    data,
		Meta:    readabilityMeta(docs, truncated),
		Columns: columns,
		Rows:    rows,
		Records: records,
		Text:    func(w io.Writer) error { return writeReadabilityText(w, docs) },
	})
}

// metricsSelection resolves what to compute: the flag if given, then the
// command it was invoked as (so `text flesch` means --metrics flesch), then the
// configured default, then auto.
func metricsSelection(cmd *cobra.Command, s *State, flag string) string {
	if cmd.Flags().Changed("metrics") {
		return flag
	}
	if called := cmd.CalledAs(); called != "" {
		if m, ok := analyze.Get(called); ok {
			return m.Name
		}
	}
	def := ""
	if s.Cfg != nil {
		def = s.Cfg.Defaults.Metrics
	}
	return firstNonEmpty(flag, def, "auto")
}

// resolveMetrics turns a --metrics selection into the metrics to run for one
// document's language.
func resolveMetrics(sel string, lang textproc.Language) ([]analyze.Metric, error) {
	switch strings.ToLower(strings.TrimSpace(sel)) {
	case "", "auto", "all":
		// "auto" and "all" agree today because every metric declares its
		// languages; both mean "everything valid for this document".
		ms := analyze.ForLanguage(lang)
		if len(ms) == 0 {
			return nil, errs.Newf(errs.CodeUnsupportedLanguage, "no metric supports language %q", lang).
				WithHint("Supported languages: " + strings.Join(metricLanguages(), ", ") + ". Force one with --lang.")
		}
		return ms, nil
	}

	var (
		out  []analyze.Metric
		seen = map[string]bool{}
	)
	for _, name := range strings.Split(sel, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		m, ok := analyze.Get(name)
		if !ok {
			return nil, errs.Newf(errs.CodeUnknownMetric, "unknown metric: %q", name).
				WithHint("Known metrics: " + strings.Join(analyze.Names(), ", ") + ". Run `text metrics list` for details.")
		}
		if !m.Supports(lang) {
			return nil, errs.Newf(errs.CodeUnsupportedLanguage, "metric %q does not support language %q", m.Name, lang).
				WithHint(metricLanguageHint(lang))
		}
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errs.New(errs.CodeInvalidArgs, "--metrics selected nothing").
			WithHint("Pass metric names, or use --metrics auto.")
	}
	return out, nil
}

// metricLanguageHint names the metrics that would have worked, so the caller's
// next command is obvious.
func metricLanguageHint(lang textproc.Language) string {
	var names []string
	for _, m := range analyze.ForLanguage(lang) {
		names = append(names, m.Name)
	}
	if len(names) == 0 {
		return fmt.Sprintf("No registered metric supports %q. Supported languages: %s.",
			lang, strings.Join(metricLanguages(), ", "))
	}
	return fmt.Sprintf("Metrics for %q: %s. Use --metrics auto to pick by language, or --lang to force the analysis language.",
		lang, strings.Join(names, ", "))
}

// metricLanguages is the union of every registered metric's languages.
func metricLanguages() []string {
	set := map[string]bool{}
	for _, m := range analyze.All() {
		for _, l := range m.Languages {
			set[l] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// readabilityColumns are the fixed leading columns of the CSV/table form; metric
// columns are appended in the order the metrics first appear.
func readabilityColumns(withStats bool) []string {
	cols := []string{"id", "language"}
	if withStats {
		cols = append(cols, "words", "sentences", "syllables", "avg_sentence_length", "avg_syllables_per_word")
	}
	return cols
}

// rdWithFields merges the source document's sidecar JSONL fields into a flat
// record so a pipeline keeps its ids and joins. Computed keys win: a field
// called "words" must never overwrite the word count.
func rdWithFields(row output.Row, fields map[string]any) any {
	if len(fields) == 0 {
		return row
	}
	rec := make(map[string]any, len(row)+len(fields))
	for k, v := range fields {
		rec[k] = v
	}
	for k, v := range row {
		rec[k] = v
	}
	return rec
}

// readabilityMeta summarises the run. Language is reported only when every
// document agreed on one; a mixed batch carries its language per document.
func readabilityMeta(docs []readabilityDoc, truncated bool) output.Meta {
	m := output.Meta{Documents: len(docs), Truncated: truncated}
	if len(docs) == 0 {
		return m
	}
	lang, detected := docs[0].Language, docs[0].LanguageDetected
	for _, d := range docs[1:] {
		if d.Language != lang {
			return m
		}
		detected = detected && d.LanguageDetected
	}
	m.Language = lang
	m.LanguageDetected = detected
	return m
}

func writeReadabilityText(w io.Writer, docs []readabilityDoc) error {
	for i, d := range docs {
		if i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if len(docs) > 1 {
			if _, err := fmt.Fprintf(w, "# %s\n", d.ID); err != nil {
				return err
			}
		}
		detected := ""
		if d.LanguageDetected {
			detected = " (detected)"
		}
		fmt.Fprintf(w, "language: %s%s\n", d.Language, detected)
		if d.Stats != nil {
			fmt.Fprintf(w, "words %d  sentences %d  syllables %d  asl %s  asw %s\n",
				d.Stats.Words, d.Stats.Sentences, d.Stats.Syllables,
				rdNum(rdRound(d.Stats.AvgSentenceLength, 2)), rdNum(rdRound(d.Stats.AvgSyllablesPerWord, 2)))
		}
		width := 0
		for _, r := range d.Metrics {
			if n := len([]rune(r.Metric)); n > width {
				width = n
			}
		}
		for _, r := range d.Metrics {
			line := fmt.Sprintf("%-*s  %s  %s", width, r.Metric, rdNum(r.Score), r.Level)
			if r.Grade != "" {
				line += " (" + r.Grade + ")"
			}
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// rdDocumentErr adds the document id to a per-document failure, but only in a
// batch: on a single document the id "0" is noise.
func rdDocumentErr(err error, id string, total int) error {
	var e *errs.E
	if total <= 1 || !errors.As(err, &e) {
		return err
	}
	return &errs.E{
		Code:          e.Code,
		Message:       fmt.Sprintf("document %q: %s", id, e.Message),
		Hint:          e.Hint,
		Retriable:     e.Retriable,
		RetryAfterSec: e.RetryAfterSec,
	}
}

func rdRound(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// rdNum formats a float without a trailing ".0", so a table reads 14 rather than
// 14.000000.
func rdNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
