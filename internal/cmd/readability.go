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

	// Metrics register themselves at init time. Importing the package is the
	// whole wiring: a future metric package is one more import, not a new
	// command or a switch statement. It is imported by name rather than blank
	// only for readability.DirectionOf, which the --fail-under/--fail-over gate
	// needs to know which way a score runs.
	"github.com/KLIXPERT-io/text-cli/internal/analyze/readability"

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
		failUnder   float64
		failOver    float64
	)
	c := &cobra.Command{
		Use:     "readability [text...]",
		Aliases: []string{"read", "rd", "flesch", "amstad"},
		Short:   "Score text for reading ease and grade level",
		Long: `readability scores prose with the formulas calibrated for its language:
Flesch Reading Ease and the English grade-level pack (Flesch-Kincaid, Gunning
Fog, SMOG, Coleman-Liau, ARI) for English, Amstad and the four Wiener
Sachtextformeln for German, and LIX for any language.

With --metrics auto (the default) the language is resolved per document, so a
mixed-language JSONL batch is scored correctly row by row.

Reading-ease scores run 0–100 with higher meaning easier; grade-level scores are
school grades, so LOWER means EASIER. --fail-under gates the first family and
--fail-over the second; each refuses the metrics it cannot mean anything for.

Examples:
  cat post.md | text readability
  text readability --file post.de.md --lang de --output text
  text readability "Short words help." --metrics flesch
  text readability --file post.md --metrics flesch --fail-under 60   # CI gate
  text readability --file post.de.md --metrics wstf --fail-over 10   # CI gate
  jq -c '{id, text}' posts.jsonl | text readability --input-format jsonl --output ndjson

Invoked as ` + "`text flesch`" + ` or ` + "`text amstad`" + ` it defaults to that one metric.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReadability(cmd, args, metricsFlag, withStats, readGates(cmd, failUnder, failOver))
		},
	}
	c.Flags().StringVar(&metricsFlag, "metrics", "", `metrics to compute: comma-separated names, "auto", or "all" (default: config defaults.metrics, else auto)`)
	c.Flags().BoolVar(&withStats, "stats", true, "include token statistics (words, sentences, syllables, averages)")
	c.Flags().Float64Var(&failUnder, "fail-under", 0, "exit non-zero if a reading-ease score (higher is easier: flesch, amstad) is below this threshold")
	c.Flags().Float64Var(&failOver, "fail-over", 0, "exit non-zero if a grade-level score (lower is easier: wstf*, lix, flesch-kincaid, gunning-fog, smog, coleman-liau, ari) is above this threshold")
	return c
}

func runReadability(cmd *cobra.Command, args []string, metricsFlag string, withStats bool, gates []gate) error {
	s := getState(cmd)

	items, err := s.LoadInput(args)
	if err != nil {
		return err
	}
	sel := metricsSelection(cmd, s, metricsFlag)
	lang := s.Language()
	// An explicit --metrics list is the user's own choice of metric, so a
	// threshold pointed the wrong way at one of them is an argument error. Under
	// auto/all the selection is the registry's choice, so a gate simply applies
	// to the metrics it fits and ignores the rest.
	explicitMetrics := !isAutoSelection(sel)

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
		if explicitMetrics {
			if err := checkGateDirections(gates, selected); err != nil {
				return err
			}
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
			applyGates(gates, it.ID, r)
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

	// The output goes out first even when a gate has failed: a CI job that gates
	// a docs build still needs the numbers that failed it on stdout. The gate
	// verdict travels as the returned error, which the root command renders as
	// one JSON line on stderr and turns into the exit code.
	if err := emitResult(cmd, emitOpts{
		Data:    data,
		Meta:    readabilityMeta(docs, truncated),
		Columns: columns,
		Rows:    rows,
		Records: records,
		Text:    func(w io.Writer) error { return writeReadabilityText(w, docs) },
	}); err != nil {
		return err
	}
	return gateVerdict(gates, len(docs))
}

// ---------------------------------------------------------------------------
// --fail-under / --fail-over
//
// Readability numbers run in two directions. Flesch and Amstad are reading-ease
// scores: 0–100, higher is easier, and "must score at least 60" is the sensible
// gate. Every grade-level formula — the Wiener Sachtextformeln, LIX,
// Flesch-Kincaid, Gunning Fog, SMOG, Coleman-Liau, ARI — is a school grade:
// lower is easier, and the sensible gate is "must score at most 10". A single
// --fail-under applied to both families would silently invert its own verdict on
// half of them, passing exactly the documents it should have failed.
//
// So there are two flags, and each one knows which metrics it can mean anything
// for. The direction comes from readability.DirectionOf: analyze.Metric has no
// Direction field and internal/analyze/registry.go is not this change's to
// widen, so the readability package publishes an exported lookup keyed by metric
// name instead. Inferring the direction from the Scale string was the
// alternative and was rejected: those strings are prose, written in German for
// the German metrics, and a CI gate must not hinge on their wording.
// ---------------------------------------------------------------------------

// gate is one resolved threshold flag.
type gate struct {
	flag      string                // "fail-under" or "fail-over"
	threshold float64               // the value scores are compared against
	dir       readability.Direction // the direction of the metrics this gate applies to
	// applied counts the metric results this gate actually judged, so a gate
	// that matched nothing can be reported as a mistake instead of passing
	// vacuously.
	applied  int
	failures []gateFailure
}

// gateFailure is one metric on one document falling the wrong side of a gate.
type gateFailure struct {
	doc    string
	metric string
	score  float64
}

// readGates turns the two flags into gates. Both may be given at once: on a
// mixed selection that gates the reading-ease scores and the grade levels in
// one run, each in its own direction.
func readGates(cmd *cobra.Command, failUnder, failOver float64) []gate {
	var gates []gate
	if cmd.Flags().Changed("fail-under") {
		gates = append(gates, gate{flag: "fail-under", threshold: failUnder, dir: readability.HigherIsEasier})
	}
	if cmd.Flags().Changed("fail-over") {
		gates = append(gates, gate{flag: "fail-over", threshold: failOver, dir: readability.LowerIsEasier})
	}
	return gates
}

// metricDirection reports which way a metric's score runs. A metric registered
// by some future package without declaring a direction is left ungated rather
// than guessed at: a threshold that might be inverted is worse than no
// threshold, because CI would be green for the wrong reason.
func metricDirection(name string) (readability.Direction, bool) {
	return readability.DirectionOf(name)
}

// checkGateDirections rejects a threshold that cannot mean anything for any
// metric the caller asked for, naming those metrics and the flag that would have
// worked. This is what stops `--metrics wstf --fail-under 8` from quietly
// passing every document: on a grade-level metric "not under 8" is satisfied by
// every text that is not a children's book, so the run would look green for
// exactly the wrong reason.
//
// A metric of the other direction inside a mixed selection is not an error — it
// is simply left ungated, and `--metrics flesch,ari --fail-under 60 --fail-over
// 12` gates both families in one run. What is refused is a gate that judges
// nothing, because that is the case where CI is green without measuring.
func checkGateDirections(gates []gate, selected []analyze.Metric) error {
	for _, g := range gates {
		var mismatched []string
		matched := false
		for _, m := range selected {
			dir, known := metricDirection(m.Name)
			switch {
			case !known:
				// Undeclared direction: never gated, never complained about.
			case dir == g.dir:
				matched = true
			default:
				mismatched = append(mismatched, m.Name)
			}
		}
		if matched {
			continue
		}
		if len(mismatched) == 0 {
			// Only reachable with a metric that declares no direction at all.
			return errs.Newf(errs.CodeInvalidArgs,
				"--%s applies to metrics where %s, and --metrics selected none that declares a direction", g.flag, directionPhrase(g.dir)).
				WithHint(fmt.Sprintf("Select a readability metric, or drop --%s.", g.flag))
		}
		return errs.Newf(errs.CodeInvalidArgs,
			"--%s applies to metrics where %s, but --metrics selected only %s, where %s",
			g.flag, directionPhrase(g.dir), strings.Join(quoteAll(mismatched), ", "), directionPhrase(flipDirection(g.dir))).
			WithHint(fmt.Sprintf("Gate those with --%s instead, or select a metric where %s.",
				oppositeFlag(g.flag), directionPhrase(g.dir)))
	}
	return nil
}

func flipDirection(d readability.Direction) readability.Direction {
	if d == readability.LowerIsEasier {
		return readability.HigherIsEasier
	}
	return readability.LowerIsEasier
}

func quoteAll(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strconv.Quote(n))
	}
	return out
}

// applyGates judges one metric result against every gate that fits its
// direction.
func applyGates(gates []gate, docID string, r analyze.Result) {
	for i := range gates {
		g := &gates[i]
		dir, known := metricDirection(r.Metric)
		if !known || dir != g.dir {
			continue
		}
		g.applied++
		failed := r.Score < g.threshold // --fail-under: higher is easier
		if g.dir == readability.LowerIsEasier {
			failed = r.Score > g.threshold // --fail-over: lower is easier
		}
		if failed {
			g.failures = append(g.failures, gateFailure{doc: docID, metric: r.Metric, score: r.Score})
		}
	}
}

// gateVerdict is the run's exit verdict. In a batch any failing document fails
// the run, and the message says how many did, because that count is what a CI
// log is read for.
func gateVerdict(gates []gate, docs int) error {
	var (
		details []string
		failed  = map[string]bool{}
	)
	for _, g := range gates {
		if g.applied == 0 {
			// Only reachable under --metrics auto/all: an explicit selection is
			// rejected up front by checkGateDirections. A gate that judged
			// nothing must not report success.
			return errs.Newf(errs.CodeInvalidArgs,
				"--%s applies to metrics where %s, and none was computed", g.flag, directionPhrase(g.dir)).
				WithHint(fmt.Sprintf("Name one with --metrics, or use --%s.", oppositeFlag(g.flag)))
		}
		for _, f := range g.failures {
			failed[f.doc] = true
			details = append(details, fmt.Sprintf("%s=%s (--%s %s)",
				f.metric, rdNum(f.score), g.flag, rdNum(g.threshold)))
		}
	}
	if len(details) == 0 {
		return nil
	}
	sort.Strings(details)
	msg := fmt.Sprintf("readability gate failed: %d of %d documents missed the threshold: %s",
		len(failed), docs, strings.Join(details, ", "))
	if docs == 1 {
		msg = "readability gate failed: " + strings.Join(details, ", ")
	}
	return errs.New(errs.CodeGeneric, msg).
		WithHint("The scores were printed to stdout. Simplify the text, or relax the threshold.")
}

// directionPhrase describes a direction in the words a hint needs.
func directionPhrase(d readability.Direction) string {
	if d == readability.LowerIsEasier {
		return "a lower score is easier"
	}
	return "a higher score is easier"
}

func oppositeFlag(flag string) string {
	if flag == "fail-under" {
		return "fail-over"
	}
	return "fail-under"
}

// isAutoSelection reports whether --metrics left the choice to the registry.
func isAutoSelection(sel string) bool {
	switch strings.ToLower(strings.TrimSpace(sel)) {
	case "", "auto", "all":
		return true
	}
	return false
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
	if isAutoSelection(sel) {
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
