// Package analyze is the extension point of the CLI: every text metric
// registers itself here at init time, and the commands, `text metrics list`,
// and the docs all read from the same registry.
//
// Adding a metric is one file and one Register call — no command wiring, no
// switch statement to update.
package analyze

import (
	"sort"
	"strings"
	"sync"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// AnyLanguage marks a metric as language-agnostic.
const AnyLanguage = "*"

// Result is one metric's verdict on one document.
type Result struct {
	Metric string  `json:"metric"`
	Title  string  `json:"title,omitempty"`
	Score  float64 `json:"score"`
	// Level is the human interpretation of Score ("sehr leicht", "fairly easy").
	Level string `json:"level,omitempty"`
	// Grade is the schooling level the text suits, when the metric defines one.
	Grade string `json:"grade,omitempty"`
	// Scale explains the number's direction, e.g. "0–100, higher is easier".
	Scale string `json:"scale,omitempty"`
	// Language the metric was computed for.
	Language string `json:"language,omitempty"`
	// Extra carries metric-specific detail without widening this struct.
	Extra map[string]any `json:"extra,omitempty"`
}

// Metric is a registered text measurement.
type Metric struct {
	// Name is the stable identifier used by --metrics and in JSON output.
	Name string
	// Aliases are alternative names accepted on the command line.
	Aliases []string
	// Title is the human name, e.g. "Flesch Reading Ease".
	Title string
	// Description is a one-line explanation shown by `text metrics list`.
	Description string
	// Languages the metric is valid for, or []string{AnyLanguage}.
	Languages []string
	// Compute measures an already-tokenized document.
	Compute func(d *textproc.Doc) (Result, error)
}

// Supports reports whether the metric applies to the given language.
func (m Metric) Supports(lang textproc.Language) bool {
	for _, l := range m.Languages {
		if l == AnyLanguage || l == string(lang) {
			return true
		}
	}
	return false
}

var (
	mu       sync.RWMutex
	metrics  = map[string]Metric{}
	aliases  = map[string]string{}
	ordering []string
)

// Register adds a metric to the registry. It panics on a duplicate name, which
// can only be a programming error, and only ever at init time.
func Register(m Metric) {
	mu.Lock()
	defer mu.Unlock()
	name := strings.ToLower(m.Name)
	if _, dup := metrics[name]; dup {
		panic("analyze: duplicate metric " + name)
	}
	metrics[name] = m
	ordering = append(ordering, name)
	for _, a := range m.Aliases {
		aliases[strings.ToLower(a)] = name
	}
}

// Get resolves a metric by name or alias, case-insensitively.
func Get(name string) (Metric, bool) {
	mu.RLock()
	defer mu.RUnlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := aliases[key]; ok {
		key = canonical
	}
	m, ok := metrics[key]
	return m, ok
}

// All returns every registered metric, sorted by name for stable output.
func All() []Metric {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Metric, 0, len(metrics))
	for _, m := range metrics {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ForLanguage returns the metrics valid for a language, sorted by name. This is
// what `--metrics auto` resolves to.
func ForLanguage(lang textproc.Language) []Metric {
	out := []Metric{}
	for _, m := range All() {
		if m.Supports(lang) {
			out = append(out, m)
		}
	}
	return out
}

// Names returns every registered metric name, sorted.
func Names() []string {
	out := []string{}
	for _, m := range All() {
		out = append(out, m.Name)
	}
	return out
}
