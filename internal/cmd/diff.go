package cmd

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/analyze/readability"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// diffDocSummary is one side of a diff: enough identity and statistics to
// explain the metric deltas without re-reading the source file.
type diffDocSummary struct {
	ID               string         `json:"id"`
	Language         string         `json:"language"`
	LanguageDetected bool           `json:"language_detected"`
	Stats            textproc.Stats `json:"stats"`
}

// diffMetricRow is one metric's before/after verdict. Improved is derived from
// Direction, never from the raw sign of Delta — see dfImproved.
type diffMetricRow struct {
	Metric      string  `json:"metric"`
	Title       string  `json:"title,omitempty"`
	Before      float64 `json:"before"`
	After       float64 `json:"after"`
	Delta       float64 `json:"delta"`
	Improved    bool    `json:"improved"`
	Direction   string  `json:"direction"`
	LevelBefore string  `json:"level_before,omitempty"`
	LevelAfter  string  `json:"level_after,omitempty"`
}

// diffData is the JSON payload of `text diff`.
type diffData struct {
	Before     diffDocSummary  `json:"before"`
	After      diffDocSummary  `json:"after"`
	Metrics    []diffMetricRow `json:"metrics"`
	StatsDelta map[string]any  `json:"stats_delta"`
}

func newDiffCmd() *cobra.Command {
	var metricsFlag string
	c := &cobra.Command{
		Use:   "diff <before> <after>",
		Short: "Score two documents and report the delta for every metric",
		Long: `diff scores <before> and <after> and reports, for every applicable
metric, the before/after values and the delta — so a rewrite's effect is a
number, not a guess.

Direction is derived from each metric's declared scale, never from the raw
sign of the delta: a falling grade-level score (LIX, Flesch-Kincaid, SMOG, ...)
is an improvement, a falling reading-ease score (Flesch, Amstad) is not.
"improved" always reflects that derived direction.

Both arguments are file paths; "-" reads stdin for at most one of them, so a
generated draft can be diffed against a saved original without a temp file.

Examples:
  text diff draft1.md draft2.md
  text diff original.md - --output text < rewrite.md
  text diff a.md b.md --metrics amstad,lix --output csv`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(cmd, args, metricsFlag)
		},
	}
	c.Flags().StringVar(&metricsFlag, "metrics", "", `metrics to compute: comma-separated names, "auto", or "all" (default: config defaults.metrics, else auto)`)
	return c
}

func runDiff(cmd *cobra.Command, args []string, metricsFlag string) error {
	s := getState(cmd)

	if len(args) != 2 {
		return errs.Newf(errs.CodeInvalidArgs, "diff requires exactly two file paths, got %d", len(args)).
			WithHint(`Usage: text diff <before> <after>. Use "-" for stdin on at most one side.`)
	}
	beforePath, afterPath := args[0], args[1]
	if beforePath == "-" && afterPath == "-" {
		return errs.New(errs.CodeInvalidArgs, `only one of <before> and <after> may be "-" (stdin)`).
			WithHint("Save one side to a file: stdin can only be read once.")
	}

	beforeItem, err := dfLoadOne(s, beforePath)
	if err != nil {
		return err
	}
	afterItem, err := dfLoadOne(s, afterPath)
	if err != nil {
		return err
	}

	lang := s.Language()
	beforeDoc, err := textproc.Analyze(beforeItem.Text, lang)
	if err != nil {
		return err
	}
	afterDoc, err := textproc.Analyze(afterItem.Text, lang)
	if err != nil {
		return err
	}

	// Mixed languages are surfaced, not silently papered over: both documents'
	// languages land in the output, and (unless --quiet) a warning explains
	// that the metrics below were resolved for "after"'s language.
	if beforeDoc.Language != afterDoc.Language && !s.Quiet {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: before is %q but after is %q; metrics were resolved for %q and applied to both\n",
			beforeDoc.Language, afterDoc.Language, afterDoc.Language)
	}

	sel := metricsSelection(cmd, s, metricsFlag)
	selected, err := resolveMetrics(sel, afterDoc.Language)
	if err != nil {
		return err
	}

	rows := make([]diffMetricRow, 0, len(selected))
	for _, m := range selected {
		beforeResult, err := m.Compute(beforeDoc)
		if err != nil {
			return err
		}
		afterResult, err := m.Compute(afterDoc)
		if err != nil {
			return err
		}
		delta := dfRound(afterResult.Score-beforeResult.Score, 1)
		direction := dfDirection(m.Name, afterResult.Scale)
		rows = append(rows, diffMetricRow{
			Metric:      m.Name,
			Title:       afterResult.Title,
			Before:      beforeResult.Score,
			After:       afterResult.Score,
			Delta:       delta,
			Improved:    dfImproved(direction, delta),
			Direction:   direction,
			LevelBefore: beforeResult.Level,
			LevelAfter:  afterResult.Level,
		})
	}

	data := diffData{
		Before: diffDocSummary{
			ID:               beforeItem.ID,
			Language:         string(beforeDoc.Language),
			LanguageDetected: beforeDoc.Detected,
			Stats:            beforeDoc.Stats,
		},
		After: diffDocSummary{
			ID:               afterItem.ID,
			Language:         string(afterDoc.Language),
			LanguageDetected: afterDoc.Detected,
			Stats:            afterDoc.Stats,
		},
		Metrics:    rows,
		StatsDelta: dfStatsDelta(beforeDoc.Stats, afterDoc.Stats),
	}

	outRows := make([]output.Row, 0, len(rows))
	for _, r := range rows {
		outRows = append(outRows, dfRow(r))
	}

	return emitResult(cmd, emitOpts{
		Data:    data,
		Meta:    dfMeta(data, beforeItem.Truncated || afterItem.Truncated),
		Columns: []string{"metric", "before", "after", "delta", "improved", "level_before", "level_after"},
		Rows:    outRows,
		Text:    func(w io.Writer) error { return dfWriteText(w, data) },
	})
}

// dfLoadOne loads a single document from a file path (or "-" for stdin),
// reusing input.Load so --input-format, --from and --max-bytes behave
// identically to every other command — which is also what lets `text diff
// old.docx new.docx` work without a line of code here. The item's id becomes
// the file's base name (or "stdin"), which is more useful in a diff than the
// "0" index input.Load hands out for a lone document.
//
// It cannot call State.LoadInput, which resolves one input source and this
// command needs two named ones — so it applies the strip pass itself, through
// the same State.stripItems LoadInput uses. Skipping it would make `text diff`
// the one command that scores markup as prose, and every decoded document
// arrives here as markdown.
func dfLoadOne(s *State, path string) (*input.Item, error) {
	items, err := input.Load(input.Options{
		Files:     []string{path},
		From:      s.From,
		Format:    input.Format(s.InputFormat),
		TextField: s.TextField,
		IDField:   s.IDField,
		MaxBytes:  s.MaxBytes,
	})
	if err != nil {
		return nil, err
	}
	s.stripItems(items)
	it := items[0]
	if path == "-" {
		it.ID = "stdin"
	} else {
		it.ID = filepath.Base(path)
	}
	return &it, nil
}

// dfDirection reports a metric's improvement direction.
//
// This is the whole point of `text diff`: some metrics are higher-is-easier
// (Flesch, Amstad, 0-100 reading ease) and others are lower-is-easier grade
// levels (LIX, Wiener Sachtextformel, Flesch-Kincaid, Gunning Fog, SMOG,
// Coleman-Liau, ARI, ...).
//
// The declared table in the readability package is authoritative: it is the
// same source `--fail-under`/`--fail-over` gate on, and a test there fails the
// build if a registered metric is missing from it. Reading the direction from
// two different places would let `text diff` and the CI gate disagree about
// whether the same rewrite was an improvement.
//
// The Scale-string fallback covers a metric registered from outside the
// readability package, which the table cannot know about. If neither says,
// this falls back to higher-is-easier rather than guessing lower — see
// dfImproved for why getting this backwards would be actively misleading.
func dfDirection(name, scale string) string {
	if d, ok := readability.DirectionOf(name); ok {
		if d == readability.LowerIsEasier {
			return "lower-is-easier"
		}
		return "higher-is-easier"
	}
	s := strings.ToLower(scale)
	switch {
	case strings.Contains(s, "lower is easier"), strings.Contains(s, "niedriger ist leichter"):
		return "lower-is-easier"
	case strings.Contains(s, "higher is easier"), strings.Contains(s, "höher ist leichter"):
		return "higher-is-easier"
	default:
		return "higher-is-easier"
	}
}

// dfImproved reports whether a delta represents an improvement, given the
// metric's derived direction. This is the only place "improved" is decided,
// and it is deliberately never `delta > 0`: for a lower-is-easier grade-level
// metric a *falling* score is the improvement, and computing improvement from
// the raw sign of delta would report every successful simplification as a
// regression.
func dfImproved(direction string, delta float64) bool {
	if direction == "lower-is-easier" {
		return delta < 0
	}
	return delta > 0
}

// dfStatsDelta is after-minus-before for every Stats field. Integer counts
// stay exact; the derived averages are rounded to 2 decimals, one more digit
// than the metric scores get, since they are inputs to those scores rather
// than the headline numbers.
func dfStatsDelta(before, after textproc.Stats) map[string]any {
	return map[string]any{
		"words":                  after.Words - before.Words,
		"sentences":              after.Sentences - before.Sentences,
		"syllables":              after.Syllables - before.Syllables,
		"characters":             after.Characters - before.Characters,
		"polysyllabic_words":     after.PolysyllabicWords - before.PolysyllabicWords,
		"monosyllabic_words":     after.MonosyllabicWords - before.MonosyllabicWords,
		"long_words":             after.LongWords - before.LongWords,
		"avg_sentence_length":    dfRound(after.AvgSentenceLength-before.AvgSentenceLength, 2),
		"avg_syllables_per_word": dfRound(after.AvgSyllablesPerWord-before.AvgSyllablesPerWord, 2),
		"avg_word_length":        dfRound(after.AvgWordLength-before.AvgWordLength, 2),
	}
}

// dfMeta summarises the run. Language/LanguageDetected are only set when both
// documents agree, mirroring readabilityMeta: a mixed pair carries its
// languages on Data.Before/Data.After instead.
func dfMeta(data diffData, truncated bool) output.Meta {
	m := output.Meta{Documents: 2, Truncated: truncated}
	if data.Before.Language == data.After.Language {
		m.Language = data.Before.Language
		m.LanguageDetected = data.Before.LanguageDetected && data.After.LanguageDetected
	}
	return m
}

func dfRow(r diffMetricRow) output.Row {
	return output.Row{
		"metric":       r.Metric,
		"title":        r.Title,
		"before":       r.Before,
		"after":        r.After,
		"delta":        r.Delta,
		"improved":     r.Improved,
		"direction":    r.Direction,
		"level_before": r.LevelBefore,
		"level_after":  r.LevelAfter,
	}
}

func dfWriteText(w io.Writer, data diffData) error {
	beforeLang, afterLang := data.Before.Language, data.After.Language
	if _, err := fmt.Fprintf(w, "before: %s (%s)\n", data.Before.ID, beforeLang); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "after:  %s (%s)\n", data.After.ID, afterLang); err != nil {
		return err
	}
	if beforeLang != afterLang {
		if _, err := fmt.Fprintln(w, "warning: before/after detected as different languages"); err != nil {
			return err
		}
	}

	width := 0
	for _, r := range data.Metrics {
		if n := len([]rune(r.Metric)); n > width {
			width = n
		}
	}
	for _, r := range data.Metrics {
		// The arrow encodes improvement, derived from direction — never the
		// raw sign of delta. A lower-is-easier metric whose score fell still
		// prints "↑" here, because that fall is the improvement.
		arrow := "↓"
		if r.Improved {
			arrow = "↑"
		}
		sign := ""
		if r.Delta >= 0 {
			sign = "+"
		}
		line := fmt.Sprintf("%-*s  %s -> %s  %s%s %s", width, r.Metric,
			dfNum(r.Before), dfNum(r.After), sign, dfNum(r.Delta), arrow)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}

	sd := data.StatsDelta
	_, err := fmt.Fprintf(w, "words %s  sentences %s  avg_sentence_length %s  avg_syllables_per_word %s\n",
		dfSignedInt(sd["words"]), dfSignedInt(sd["sentences"]),
		dfSignedFloat(sd["avg_sentence_length"]), dfSignedFloat(sd["avg_syllables_per_word"]))
	return err
}

func dfSignedInt(v any) string {
	n, _ := v.(int)
	if n >= 0 {
		return "+" + fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%d", n)
}

func dfSignedFloat(v any) string {
	f, _ := v.(float64)
	if f >= 0 {
		return "+" + dfNum(f)
	}
	return dfNum(f)
}

func dfRound(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// dfNum formats a float without a trailing ".0", so a table reads 14 rather
// than 14.000000.
func dfNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
