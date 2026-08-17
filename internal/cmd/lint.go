package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/lint"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/KLIXPERT-io/text-cli/internal/textproc"
	"github.com/spf13/cobra"
)

// lintDoc is one document's work list. Like readabilityDoc it is the unit of
// the JSON output: a single document is emitted as this object, a batch as a
// list of them, so a consumer parses the same shape either way.
type lintDoc struct {
	ID               string         `json:"id"`
	Language         string         `json:"language"`
	LanguageDetected bool           `json:"language_detected,omitempty"`
	Findings         []lint.Finding `json:"findings"`
	// Summary counts findings per rule — the number a CI job thresholds on.
	Summary map[string]int `json:"summary"`
	Total   int            `json:"total"`

	// text is the source, kept for the human-readable renderer so it can turn
	// a byte offset into a line number. Unexported: it is not part of the
	// output contract, and echoing the whole document back would be absurd.
	text string
}

type lintOptions struct {
	rules            string
	severity         string
	maxSentenceWords int
	maxWordChars     int
	worst            int
	failOnFindings   bool
}

// newLintCmd builds `text lint`. Register it in Execute with root.AddCommand.
func newLintCmd() *cobra.Command {
	var o lintOptions
	def := lint.Defaults()

	c := &cobra.Command{
		Use:   "lint [text...]",
		Short: "Find what to fix: sentence- and phrase-level findings with byte offsets",
		Long: `lint turns a readability score into a work list.

Where ` + "`text readability`" + ` says a document is hard, lint says which sentence is
too long, where the Passiv hides the actor, and which Behördendeutsch phrase has
a plain-language replacement. Every finding carries byte offsets into the source,
so an editor, a script, or an LLM handed the JSON can slice the document and
apply the edit without searching for the span again.

Rules are selected per document language: --rules auto (the default) runs every
rule registered for the language the document was analysed in. Run
` + "`text lint rules`" + ` to see them all.

Examples:
  text lint --file post.de.md --output text
  text lint --file post.de.md --rules passive,bureaucratic --severity warn
  text lint --file post.de.md --output csv > findings.csv
  text lint --file post.de.md --fail-on-findings --severity warn   # CI gate
  jq -c '{id, text}' posts.jsonl | text lint --input-format jsonl --output ndjson`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLint(cmd, args, o)
		},
	}
	c.Flags().StringVar(&o.rules, "rules", "auto", `rules to run: comma-separated names, "auto" (by document language), or "all"`)
	c.Flags().StringVar(&o.severity, "severity", string(lint.SeverityInfo), "minimum severity to report: info|warn")
	c.Flags().IntVar(&o.maxSentenceWords, "max-sentence-words", def.MaxSentenceWords, "word count above which a sentence is flagged as long")
	c.Flags().IntVar(&o.maxWordChars, "max-word-chars", def.MaxWordChars, "character count above which a word is flagged as long")
	c.Flags().IntVar(&o.worst, "worst", def.Worst, "how many hard sentences to report")
	c.Flags().BoolVar(&o.failOnFindings, "fail-on-findings", false, "exit non-zero when any finding is reported (CI gating)")
	c.AddCommand(newLintRulesCmd())
	return c
}

func runLint(cmd *cobra.Command, args []string, o lintOptions) error {
	s := getState(cmd)

	minSeverity, ok := lint.ParseSeverity(o.severity)
	if !ok {
		return errs.Newf(errs.CodeInvalidArgs, "unknown severity: %q", o.severity).
			WithHint("Use --severity info (everything) or --severity warn (only the warnings).")
	}
	items, err := s.LoadInput(args)
	if err != nil {
		return err
	}
	cfg := lint.Config{
		MaxSentenceWords: o.maxSentenceWords,
		MaxWordChars:     o.maxWordChars,
		Worst:            o.worst,
	}
	lang := s.Language()

	docs := make([]lintDoc, 0, len(items))
	rows := make([]output.Row, 0, len(items))
	records := make([]any, 0, len(items))
	total := 0
	truncated := false

	for _, it := range items {
		d, err := textproc.Analyze(it.Text, lang)
		if err != nil {
			return rdDocumentErr(err, it.ID, len(items))
		}
		// Resolved per document, not once per run: a JSONL batch may mix
		// languages, and German rules on an English row are noise.
		rules, err := resolveRules(o.rules, d.Language)
		if err != nil {
			return rdDocumentErr(err, it.ID, len(items))
		}
		findings := lint.FilterSeverity(lint.Run(d, rules, cfg), minSeverity)

		docs = append(docs, lintDoc{
			ID:               it.ID,
			Language:         string(d.Language),
			LanguageDetected: d.Detected,
			Findings:         findings,
			Summary:          lint.Summary(findings),
			Total:            len(findings),
			text:             d.Text,
		})
		for _, f := range findings {
			rows = append(rows, lintRow(it.ID, f, lintExcerptWidth))
			records = append(records, rdWithFields(lintRow(it.ID, f, 0), it.Fields))
		}
		total += len(findings)
		truncated = truncated || it.Truncated
	}

	var data any = map[string]any{"documents": docs}
	if len(docs) == 1 {
		data = docs[0]
	}

	if err := emitResult(cmd, emitOpts{
		Data:    data,
		Meta:    lintMeta(docs, truncated),
		Columns: lintColumns(),
		Rows:    rows,
		Records: records,
		Text:    func(w io.Writer) error { return writeLintText(w, docs) },
	}); err != nil {
		return err
	}

	// The gate comes after the output: a CI job wants the findings *and* the
	// non-zero exit. CodeGeneric keeps the exit code at 1, the conventional
	// "the check failed" of every other linter.
	if o.failOnFindings && total > 0 {
		return errs.Newf(errs.CodeGeneric, "%d finding(s) reported", total).
			WithHint("Fix them, raise --severity, or drop --fail-on-findings.")
	}
	return nil
}

// resolveRules turns a --rules selection into the rules to run for one
// document's language.
//
// "auto" and "all" agree today, exactly as they do for --metrics: every rule
// declares its languages, so both mean "everything valid for this document".
// Running the English passive detector over German would not be more thorough,
// only wrong.
func resolveRules(sel string, lang textproc.Language) ([]lint.Rule, error) {
	switch strings.ToLower(strings.TrimSpace(sel)) {
	case "", "auto", "all":
		rs := lint.ForLanguage(lang)
		if len(rs) == 0 {
			return nil, errs.Newf(errs.CodeUnsupportedLanguage, "no lint rule supports language %q", lang).
				WithHint("Supported languages: " + strings.Join(ruleLanguages(), ", ") + ". Force one with --lang.")
		}
		return rs, nil
	}

	var (
		out  []lint.Rule
		seen = map[string]bool{}
	)
	for _, name := range strings.Split(sel, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		r, known, supported := lint.GetFor(name, lang)
		if !known {
			return nil, errs.Newf(errs.CodeInvalidArgs, "unknown lint rule: %q", name).
				WithHint("Known rules: " + strings.Join(lint.Names(), ", ") + ". Run `text lint rules` for details.")
		}
		if !supported {
			return nil, errs.Newf(errs.CodeUnsupportedLanguage, "rule %q does not support language %q", r.Name, lang).
				WithHint(ruleLanguageHint(lang))
		}
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errs.New(errs.CodeInvalidArgs, "--rules selected nothing").
			WithHint("Pass rule names, or use --rules auto.")
	}
	return out, nil
}

// ruleLanguageHint names the rules that would have worked, so the caller's next
// command is obvious.
func ruleLanguageHint(lang textproc.Language) string {
	var names []string
	for _, r := range lint.ForLanguage(lang) {
		names = append(names, r.Name)
	}
	if len(names) == 0 {
		return fmt.Sprintf("No registered rule supports %q. Supported languages: %s.",
			lang, strings.Join(ruleLanguages(), ", "))
	}
	return fmt.Sprintf("Rules for %q: %s. Use --rules auto to pick by language, or --lang to force the analysis language.",
		lang, strings.Join(names, ", "))
}

// ruleLanguages is the union of every registered rule's languages.
func ruleLanguages() []string {
	set := map[string]bool{}
	for _, r := range lint.All() {
		for _, l := range r.Languages {
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

// lintColumns are the CSV/table columns: one row per finding, keyed by the
// document it came from.
func lintColumns() []string {
	return []string{"doc_id", "rule", "severity", "sentence", "start", "end", "value", "excerpt", "message"}
}

// lintExcerptWidth caps the excerpt in the human-facing forms: a table with a
// 200-character cell in it is unreadable.
//
// It is deliberately NOT applied to JSON, TOON, or NDJSON. The whole point of
// a finding is that text[start:end] == excerpt, so a caller can apply an edit
// without re-searching for the span; a silently shortened excerpt in a
// machine-facing format breaks that invariant exactly where something is most
// likely to rely on it.
const lintExcerptWidth = 80

// lintRow builds one finding row. maxExcerpt <= 0 keeps the excerpt whole.
func lintRow(id string, f lint.Finding, maxExcerpt int) output.Row {
	excerpt := f.Excerpt
	if maxExcerpt > 0 {
		excerpt = lint.Shorten(excerpt, maxExcerpt)
	}
	row := output.Row{
		"doc_id":   id,
		"rule":     f.Rule,
		"severity": string(f.Severity),
		"sentence": f.Sentence,
		"start":    f.Start,
		"end":      f.End,
		"excerpt":  excerpt,
		"message":  f.Message,
	}
	// A rule without a number leaves the cell empty rather than claiming zero,
	// and leaves the key out of the NDJSON record entirely.
	if f.Value != 0 {
		row["value"] = f.Value
	}
	return row
}

// lintMeta summarises the run. Language is reported only when every document
// agreed on one; a mixed batch carries its language per document.
func lintMeta(docs []lintDoc, truncated bool) output.Meta {
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

// writeLintText groups by rule, because a human fixes one class of problem at a
// time: all the long sentences, then all the passives.
func writeLintText(w io.Writer, docs []lintDoc) error {
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
		if d.Total == 0 {
			if _, err := fmt.Fprintf(w, "no findings (%s)\n", d.Language); err != nil {
				return err
			}
			continue
		}
		lines := newLineIndex(d.text)
		for _, rule := range sortedRules(d.Summary) {
			if _, err := fmt.Fprintf(w, "%s (%d)\n", rule, d.Summary[rule]); err != nil {
				return err
			}
			for _, f := range d.Findings {
				if f.Rule != rule {
					continue
				}
				excerpt := lint.Shorten(f.Excerpt, 60)
				if excerpt == "" {
					excerpt = "(document)"
				}
				// "L" prefix: this is the source line, whereas every other
				// output format reports f.Sentence in the same position. The
				// two differ, and an unlabelled number invites reading a line
				// number as a sentence index.
				if _, err := fmt.Fprintf(w, "  L%d: %s — %s\n", lines.at(f.Start), excerpt, f.Message); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintf(w, "\n%d finding(s)\n", d.Total); err != nil {
			return err
		}
	}
	return nil
}

func sortedRules(summary map[string]int) []string {
	out := make([]string, 0, len(summary))
	for r := range summary {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// lineIndex turns a byte offset into a 1-based line number, so a terminal
// reader can jump straight to the place in the file.
type lineIndex []int // byte offset of the start of each line

func newLineIndex(text string) lineIndex {
	idx := lineIndex{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			idx = append(idx, i+1)
		}
	}
	return idx
}

func (l lineIndex) at(offset int) int {
	// The first line start strictly greater than offset is the line after it.
	return sort.Search(len(l), func(i int) bool { return l[i] > offset })
}

// ---------------------------------------------------------------------------
// `text lint rules`
// ---------------------------------------------------------------------------

// lintRuleInfo is the discovery record for one rule, built from the registry
// and never from a hand-maintained list — the same contract as `text metrics
// list`.
type lintRuleInfo struct {
	Name        string   `json:"name"`
	Title       string   `json:"title,omitempty"`
	Languages   []string `json:"languages"`
	Severity    string   `json:"severity"`
	Description string   `json:"description,omitempty"`
}

func newLintRuleInfo(r lint.Rule) lintRuleInfo {
	langs := r.Languages
	if langs == nil {
		langs = []string{}
	}
	return lintRuleInfo{
		Name:        r.Name,
		Title:       r.Title,
		Languages:   langs,
		Severity:    string(r.Severity),
		Description: r.Description,
	}
}

func (ri lintRuleInfo) row() output.Row {
	return output.Row{
		"name":        ri.Name,
		"title":       ri.Title,
		"languages":   strings.Join(ri.Languages, ","),
		"severity":    ri.Severity,
		"description": ri.Description,
	}
}

func newLintRulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rules",
		Aliases: []string{"list"},
		Short:   "List every registered lint rule",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all := lint.All()
			infos := make([]lintRuleInfo, 0, len(all))
			rows := make([]output.Row, 0, len(all))
			records := make([]any, 0, len(all))
			for _, r := range all {
				ri := newLintRuleInfo(r)
				infos = append(infos, ri)
				rows = append(rows, ri.row())
				records = append(records, ri)
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"rules": infos},
				Meta:    output.Meta{},
				Columns: []string{"name", "title", "languages", "severity", "description"},
				Rows:    rows,
				Records: records,
				Text: func(w io.Writer) error {
					for _, ri := range infos {
						if _, err := fmt.Fprintf(w, "%s  [%s]  %s  %s\n",
							ri.Name, strings.Join(ri.Languages, ","), ri.Severity, ri.Description); err != nil {
							return err
						}
					}
					return nil
				},
			})
		},
	}
}
