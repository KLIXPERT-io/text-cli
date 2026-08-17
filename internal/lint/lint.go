// Package lint turns a readability score into a work list.
//
// A metric says a document is hard. A lint rule says which sentence, which
// phrase, and what to write instead. Every Finding carries byte offsets into
// the source text, so whoever consumes the JSON — an editor, a script, or an
// LLM asked to rewrite the document — can slice the original or apply an edit
// without having to find the span again.
//
// Rules register themselves at init time, exactly like the metrics in
// internal/analyze: adding a rule is one Register call in one file, never a new
// switch statement in a command.
//
// The package deliberately does not import internal/analyze. Lint findings are
// per-sentence and per-phrase, the metric registry is per-document, and the two
// would otherwise have to know about each other; the little arithmetic lint
// needs from readability (see rules_any.go) is duplicated on purpose.
package lint

import (
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// AnyLanguage marks a rule as language-agnostic.
const AnyLanguage = "*"

// Severity separates "this is wrong" from "this is worth a look". Two levels,
// on purpose: a scale with five steps only moves the argument from the text to
// the scale.
type Severity string

const (
	SeverityInfo Severity = "info"
	SeverityWarn Severity = "warn"
)

var severityRank = map[Severity]int{SeverityInfo: 0, SeverityWarn: 1}

// ParseSeverity resolves a severity name case-insensitively.
func ParseSeverity(s string) (Severity, bool) {
	switch Severity(strings.ToLower(strings.TrimSpace(s))) {
	case SeverityInfo:
		return SeverityInfo, true
	case SeverityWarn:
		return SeverityWarn, true
	}
	return "", false
}

// AtLeast reports whether s is at least as severe as min.
func (s Severity) AtLeast(min Severity) bool { return severityRank[s] >= severityRank[min] }

// Finding is one thing to fix, located exactly.
//
// Start and End are byte offsets into the analysed document's text, and Excerpt
// is always exactly text[Start:End] — never a shortened or prettified version
// of it. Display truncation belongs to the renderer (see Shorten), because a
// caller that applies an edit needs the span and the excerpt to agree.
type Finding struct {
	Rule       string   `json:"rule"`
	Severity   Severity `json:"severity"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
	// Sentence is the 1-based sentence index, or 0 for a document-level
	// finding anchored at offset 0.
	Sentence int    `json:"sentence"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Excerpt  string `json:"excerpt"`
	// Value is the measured number behind the finding (a word count, a
	// reading-ease score, a density), when the rule has one.
	Value float64 `json:"value,omitempty"`
}

// Rule is a registered check.
type Rule struct {
	// Name is the stable identifier used by --rules, in JSON output, and as
	// the key of the per-document summary.
	Name string
	// Title is the human name shown by `text lint rules`.
	Title string
	// Description is a one-line explanation of what the rule looks for.
	Description string
	// Languages the rule applies to, or []string{AnyLanguage}.
	Languages []string
	// Severity is the default severity of the rule's findings; a Check may
	// override it per finding (a very long sentence warns, a merely long one
	// informs).
	Severity Severity
	// Check inspects an already-tokenized document.
	Check func(d *textproc.Doc, cfg Config) []Finding
}

// Supports reports whether the rule applies to the given language.
func (r Rule) Supports(lang textproc.Language) bool {
	for _, l := range r.Languages {
		if l == AnyLanguage || l == string(lang) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------
//
// Unlike the metric registry, a name here may be registered more than once, as
// long as the variants cover disjoint languages: German passive voice and
// English passive voice are genuinely different detectors, but calling them
// "passive-de" and "passive-en" would push that implementation detail into
// every summary key and every CI threshold. So the registry keys by name and
// resolves the variant per document language.

var (
	mu       sync.RWMutex
	registry = map[string][]Rule{}
	ordering []string
)

// Register adds a rule. It panics on a name already registered for an
// overlapping language, which can only be a programming error, and only ever at
// init time.
func Register(r Rule) {
	mu.Lock()
	defer mu.Unlock()
	name := strings.ToLower(strings.TrimSpace(r.Name))
	if name == "" || r.Check == nil {
		panic("lint: rule needs a name and a Check")
	}
	if len(r.Languages) == 0 {
		panic("lint: rule " + name + " declares no languages")
	}
	if r.Severity == "" {
		r.Severity = SeverityInfo
	}
	r.Name = name
	for _, existing := range registry[name] {
		if languagesOverlap(existing.Languages, r.Languages) {
			panic("lint: duplicate rule " + name + " for languages " +
				strings.Join(r.Languages, ","))
		}
	}
	if _, seen := registry[name]; !seen {
		ordering = append(ordering, name)
	}
	registry[name] = append(registry[name], r)
}

func languagesOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y || x == AnyLanguage || y == AnyLanguage {
				return true
			}
		}
	}
	return false
}

// Variants returns every registered rule with this name, sorted by language.
func Variants(name string) []Rule {
	mu.RLock()
	defer mu.RUnlock()
	rs := registry[strings.ToLower(strings.TrimSpace(name))]
	out := make([]Rule, len(rs))
	copy(out, rs)
	sortRules(out)
	return out
}

// Get resolves a rule by name. When a name has per-language variants it returns
// the first by language; use GetFor to pick the one that fits a document.
func Get(name string) (Rule, bool) {
	vs := Variants(name)
	if len(vs) == 0 {
		return Rule{}, false
	}
	return vs[0], true
}

// GetFor resolves the variant of a rule that applies to lang. The second return
// value reports whether the name exists at all, so a caller can tell "no such
// rule" from "that rule does not do German".
func GetFor(name string, lang textproc.Language) (Rule, bool, bool) {
	vs := Variants(name)
	if len(vs) == 0 {
		return Rule{}, false, false
	}
	for _, r := range vs {
		if r.Supports(lang) {
			return r, true, true
		}
	}
	return vs[0], true, false
}

// All returns every registered rule (every language variant), sorted by name
// then language, so listings and tests never depend on init order.
func All() []Rule {
	mu.RLock()
	out := make([]Rule, 0, len(registry))
	for _, rs := range registry {
		out = append(out, rs...)
	}
	mu.RUnlock()
	sortRules(out)
	return out
}

// ForLanguage returns the rules valid for a language, sorted by name. This is
// what `--rules auto` resolves to.
func ForLanguage(lang textproc.Language) []Rule {
	out := []Rule{}
	for _, r := range All() {
		if r.Supports(lang) {
			out = append(out, r)
		}
	}
	return out
}

// Names returns every registered rule name once, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func sortRules(rs []Rule) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Name != rs[j].Name {
			return rs[i].Name < rs[j].Name
		}
		return strings.Join(rs[i].Languages, ",") < strings.Join(rs[j].Languages, ",")
	})
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config carries the tunable thresholds. Every zero field takes its default
// from Defaults(), so a caller that only overrides the sentence length writes
// Config{MaxSentenceWords: 30} and nothing else.
type Config struct {
	// MaxSentenceWords is the word count above which long-sentence fires.
	MaxSentenceWords int
	// WarnSentenceWords is the word count above which long-sentence warns
	// rather than informs.
	WarnSentenceWords int
	// MaxWordChars is the rune count above which long-word fires.
	MaxWordChars int
	// Worst caps how many hard-sentence findings are reported.
	Worst int
	// HardSentenceScore is the per-sentence reading ease below which a
	// sentence counts as hard (0–100, higher is easier).
	HardSentenceScore float64
	// HardSentenceWarnScore is the reading ease below which hard-sentence
	// warns rather than informs.
	HardSentenceWarnScore float64
	// MinHardSentenceWords keeps headlines and fragments out of the
	// hard-sentence list.
	MinHardSentenceWords int
	// RepeatWindow is how many words apart two occurrences of the same
	// content word may be before repeated-word fires.
	RepeatWindow int
	// MinRepeatWordChars is the shortest word repeated-word considers.
	MinRepeatWordChars int
	// RepeatedStartRun is how many consecutive sentences must open with the
	// same word before repeated-sentence-start fires.
	RepeatedStartRun int
	// MinVarianceSentences is the shortest document sentence-length-variance
	// judges; below it, rhythm is not a thing yet.
	MinVarianceSentences int
	// MinLengthCV and MaxLengthCV bracket an acceptable coefficient of
	// variation of sentence lengths.
	MinLengthCV float64
	MaxLengthCV float64
	// MinDensityWords is the shortest document the density rules judge.
	MinDensityWords int
	// MinDensityHits is how many occurrences a density rule needs before it
	// says anything at all.
	MinDensityHits int
	// MaxDensityFindings caps how many occurrences a density rule reports, so
	// a rule that could fire forty times on one page stays a work list.
	MaxDensityFindings int
	// NominalizationDensity, ModalHedgeDensity and AdverbDensity are the
	// share of all words above which the respective rule fires.
	NominalizationDensity float64
	ModalHedgeDensity     float64
	AdverbDensity         float64
	// PassiveLookahead is how many words after an auxiliary a participle may
	// sit and still count as passive voice. It is generous because German is
	// verb-final — "wird ... vorgenommen" routinely puts ten words between the
	// two — and the scan stops at a clause boundary anyway.
	PassiveLookahead int

	// view caches the tokenization for the document currently being linted.
	// Run fills it in so eleven rules tokenize once, not eleven times.
	view *docView
}

// Defaults returns the thresholds the CLI uses when nothing is overridden.
func Defaults() Config {
	return Config{
		MaxSentenceWords:      25,
		WarnSentenceWords:     35,
		MaxWordChars:          20,
		Worst:                 5,
		HardSentenceScore:     50,
		HardSentenceWarnScore: 30,
		MinHardSentenceWords:  6,
		RepeatWindow:          40,
		MinRepeatWordChars:    5,
		RepeatedStartRun:      3,
		MinVarianceSentences:  5,
		MinLengthCV:           0.25,
		MaxLengthCV:           0.90,
		MinDensityWords:       40,
		MinDensityHits:        3,
		MaxDensityFindings:    5,
		NominalizationDensity: 0.06,
		ModalHedgeDensity:     0.025,
		AdverbDensity:         0.05,
		PassiveLookahead:      12,
	}
}

// WithDefaults fills every zero field from Defaults.
func (c Config) WithDefaults() Config {
	d := Defaults()
	fillInt(&c.MaxSentenceWords, d.MaxSentenceWords)
	fillInt(&c.WarnSentenceWords, d.WarnSentenceWords)
	fillInt(&c.MaxWordChars, d.MaxWordChars)
	fillInt(&c.Worst, d.Worst)
	fillFloat(&c.HardSentenceScore, d.HardSentenceScore)
	fillFloat(&c.HardSentenceWarnScore, d.HardSentenceWarnScore)
	fillInt(&c.MinHardSentenceWords, d.MinHardSentenceWords)
	fillInt(&c.RepeatWindow, d.RepeatWindow)
	fillInt(&c.MinRepeatWordChars, d.MinRepeatWordChars)
	fillInt(&c.RepeatedStartRun, d.RepeatedStartRun)
	fillInt(&c.MinVarianceSentences, d.MinVarianceSentences)
	fillFloat(&c.MinLengthCV, d.MinLengthCV)
	fillFloat(&c.MaxLengthCV, d.MaxLengthCV)
	fillInt(&c.MinDensityWords, d.MinDensityWords)
	fillInt(&c.MinDensityHits, d.MinDensityHits)
	fillInt(&c.MaxDensityFindings, d.MaxDensityFindings)
	fillFloat(&c.NominalizationDensity, d.NominalizationDensity)
	fillFloat(&c.ModalHedgeDensity, d.ModalHedgeDensity)
	fillFloat(&c.AdverbDensity, d.AdverbDensity)
	fillInt(&c.PassiveLookahead, d.PassiveLookahead)
	if c.WarnSentenceWords < c.MaxSentenceWords {
		// An override of --max-sentence-words above the warn threshold would
		// otherwise make every long sentence a warning.
		c.WarnSentenceWords = c.MaxSentenceWords
	}
	return c
}

func fillInt(p *int, def int) {
	if *p <= 0 {
		*p = def
	}
}

func fillFloat(p *float64, def float64) {
	if *p == 0 {
		*p = def
	}
}

// ---------------------------------------------------------------------------
// Running
// ---------------------------------------------------------------------------

// Run applies rules to a document and returns the findings sorted by Start,
// then rule name — deterministic, because output that reorders between runs is
// neither testable nor usable in CI.
func Run(d *textproc.Doc, rules []Rule, cfg Config) []Finding {
	out := []Finding{}
	if d == nil || len(d.Sentences) == 0 {
		return out
	}
	cfg = cfg.WithDefaults()
	cfg.view = newView(d)
	for _, r := range rules {
		if r.Check == nil {
			continue
		}
		for _, f := range r.Check(d, cfg) {
			if f.Rule == "" {
				f.Rule = r.Name
			}
			if f.Severity == "" {
				f.Severity = r.Severity
			}
			out = append(out, f)
		}
	}
	SortFindings(out)
	return out
}

// SortFindings imposes the canonical order: position first, then rule name, so
// two runs over the same bytes produce byte-identical output.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		switch {
		case a.Start != b.Start:
			return a.Start < b.Start
		case a.Rule != b.Rule:
			return a.Rule < b.Rule
		case a.End != b.End:
			return a.End < b.End
		default:
			return a.Message < b.Message
		}
	})
}

// FilterSeverity drops findings below min, keeping the order.
func FilterSeverity(fs []Finding, min Severity) []Finding {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		if f.Severity.AtLeast(min) {
			out = append(out, f)
		}
	}
	return out
}

// Summary counts findings per rule, the shape a CI job thresholds on.
func Summary(fs []Finding) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[f.Rule]++
	}
	return out
}

// Shorten collapses a span to at most n runes for display. Findings keep the
// full excerpt; only renderers shorten.
func Shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if n <= 1 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimRight(string(runes[:n-1]), " ") + "…"
}

// ---------------------------------------------------------------------------
// Document view
// ---------------------------------------------------------------------------

// token is one word of the document with absolute byte offsets into Doc.Text,
// so a rule never has to translate between sentence-relative and document
// offsets — the single most likely place to confuse bytes with runes.
type token struct {
	Text  string
	Lower string
	Runes int
	Syll  int
	Start int
	End   int
	// Sentence is the 1-based sentence index the token belongs to.
	Sentence int
	// Index is the 0-based position of the token in the whole document, which
	// is what the repetition window counts in.
	Index int
}

// sentView pairs a sentence with its tokens.
type sentView struct {
	Num  int
	S    textproc.Sentence
	Toks []token
}

type docView struct {
	doc       *textproc.Doc
	tokens    []token
	sentences []sentView
}

// newView locates every word of every sentence in the source text.
//
// textproc gives sentences byte offsets but words only as strings, so word
// offsets are recovered by walking each sentence with a cursor. The tokens came
// out of this very string in this very order, so the first match at or after
// the cursor is the token itself — no re-tokenization, and no chance of the two
// disagreeing.
func newView(d *textproc.Doc) *docView {
	v := &docView{doc: d}
	v.tokens = make([]token, 0, d.Stats.Words)
	type span struct{ from, to int }
	spans := make([]span, 0, len(d.Sentences))

	for si := range d.Sentences {
		s := d.Sentences[si]
		from := len(v.tokens)
		cursor := 0
		for _, w := range s.Words {
			off := strings.Index(s.Text[cursor:], w.Text)
			if off < 0 {
				continue // defensive: cannot happen, the tokens are slices of s.Text
			}
			start := s.Start + cursor + off
			cursor += off + len(w.Text)
			v.tokens = append(v.tokens, token{
				Text:     w.Text,
				Lower:    strings.ToLower(w.Text),
				Runes:    w.Runes,
				Syll:     w.Syllables,
				Start:    start,
				End:      start + len(w.Text),
				Sentence: si + 1,
				Index:    len(v.tokens),
			})
		}
		spans = append(spans, span{from, len(v.tokens)})
	}
	// Subslices are taken only after the appends are done: growing v.tokens
	// would otherwise leave earlier sentences pointing at a stale array.
	v.sentences = make([]sentView, 0, len(spans))
	for i, sp := range spans {
		v.sentences = append(v.sentences, sentView{
			Num:  i + 1,
			S:    d.Sentences[i],
			Toks: v.tokens[sp.from:sp.to],
		})
	}
	return v
}

// tokens returns the cached tokenization, building it when a rule is called
// outside Run (a unit test, say).
func (c Config) tokens(d *textproc.Doc) *docView {
	if c.view != nil && c.view.doc == d {
		return c.view
	}
	return newView(d)
}

// ---------------------------------------------------------------------------
// Finding helpers
// ---------------------------------------------------------------------------

// finding builds a Finding whose Excerpt is exactly text[start:end].
func finding(d *textproc.Doc, rule string, sev Severity, sentence, start, end int, msg, suggestion string, value float64) Finding {
	if start < 0 {
		start = 0
	}
	if end > len(d.Text) {
		end = len(d.Text)
	}
	if end < start {
		end = start
	}
	return Finding{
		Rule:       rule,
		Severity:   sev,
		Message:    msg,
		Suggestion: suggestion,
		Sentence:   sentence,
		Start:      start,
		End:        end,
		Excerpt:    d.Text[start:end],
		Value:      value,
	}
}

// clauseBreak reports whether the source between two adjacent tokens contains
// punctuation that ends a clause. A passive-voice span that runs across a comma
// has stopped describing one construction and started describing two, and the
// span is what a caller rewrites.
func clauseBreak(d *textproc.Doc, prev, next token) bool {
	if next.Start <= prev.End || next.Start > len(d.Text) {
		return false
	}
	return strings.ContainsAny(d.Text[prev.End:next.Start], ",;:–—()")
}

// tr picks the language of a message: German prose deserves German findings.
func tr(lang textproc.Language, de, en string) string {
	if lang == textproc.LangGerman {
		return de
	}
	return en
}

func round(v float64, places int) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// ---------------------------------------------------------------------------
// Phrase matching
// ---------------------------------------------------------------------------
//
// Every word list in this package is a phrase list: single words are phrases of
// length one, so "sehr" and "im Rahmen von" are matched by the same code and
// live in the same var block. Matching is on lower-cased tokens — no regexp in
// the per-word path.

type phrase struct {
	words      []string
	Canonical  string
	Suggestion string
}

type phraseSet struct {
	byFirst map[string][]phrase
}

// newPhrases builds a set from a whitespace-separated list of entries, one
// phrase per line or per comma.
func newPhrases(entries ...string) *phraseSet {
	m := map[string]string{}
	for _, e := range entries {
		m[e] = ""
	}
	return newPhraseMap(m)
}

// newPhraseMap builds a set from phrase → suggestion pairs. The build is
// deterministic despite the map: buckets are sorted by length, then text.
func newPhraseMap(pairs map[string]string) *phraseSet {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ps := &phraseSet{byFirst: map[string][]phrase{}}
	for _, k := range keys {
		words := strings.Fields(strings.ToLower(k))
		if len(words) == 0 {
			continue
		}
		p := phrase{words: words, Canonical: k, Suggestion: pairs[k]}
		ps.byFirst[words[0]] = append(ps.byFirst[words[0]], p)
	}
	for k := range ps.byFirst {
		bucket := ps.byFirst[k]
		// Longest first, so "im Rahmen von" wins over a hypothetical "im".
		sort.SliceStable(bucket, func(i, j int) bool {
			if len(bucket[i].words) != len(bucket[j].words) {
				return len(bucket[i].words) > len(bucket[j].words)
			}
			return bucket[i].Canonical < bucket[j].Canonical
		})
		ps.byFirst[k] = bucket
	}
	return ps
}

// match reports the longest phrase starting at toks[i] and how many tokens it
// spans.
func (ps *phraseSet) match(toks []token, i int) (phrase, int, bool) {
	for _, p := range ps.byFirst[toks[i].Lower] {
		if i+len(p.words) > len(toks) {
			continue
		}
		ok := true
		for k, w := range p.words {
			if toks[i+k].Lower != w {
				ok = false
				break
			}
		}
		if ok {
			return p, len(p.words), true
		}
	}
	return phrase{}, 0, false
}

// has reports whether a single lower-cased word is in the set.
func (ps *phraseSet) has(word string) bool {
	for _, p := range ps.byFirst[word] {
		if len(p.words) == 1 {
			return true
		}
	}
	return false
}

// scanPhrases walks the document and calls fn for every occurrence, in
// document order, skipping past a match so overlapping phrases fire once.
func scanPhrases(v *docView, ps *phraseSet, fn func(p phrase, first, last token)) {
	for _, sv := range v.sentences {
		for i := 0; i < len(sv.Toks); {
			p, n, ok := ps.match(sv.Toks, i)
			if !ok {
				i++
				continue
			}
			fn(p, sv.Toks[i], sv.Toks[i+n-1])
			i += n
		}
	}
}

// ---------------------------------------------------------------------------
// Density
// ---------------------------------------------------------------------------

// hit is a candidate occurrence collected by a density rule before it knows
// whether the document is dense enough to say anything.
type hit struct {
	first, last token
	surface     string
	suggestion  string
}

// dense reports whether the collected hits are worth reporting, and how dense
// they are. A rule that fires on every single occurrence is noise: nobody
// rewrites forty nominalizations on one page, they rewrite the paragraph. So a
// density rule stays silent below the threshold and reports only the first
// MaxDensityFindings occurrences above it, naming the total in the message.
func dense(cfg Config, words int, hits []hit, minDensity float64) ([]hit, float64, bool) {
	if len(hits) < cfg.MinDensityHits || words < cfg.MinDensityWords {
		return nil, 0, false
	}
	density := float64(len(hits)) / float64(words)
	if density < minDensity {
		return nil, 0, false
	}
	shown := hits
	if len(shown) > cfg.MaxDensityFindings {
		shown = shown[:cfg.MaxDensityFindings]
	}
	return shown, density, true
}
