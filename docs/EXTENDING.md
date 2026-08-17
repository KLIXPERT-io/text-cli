# Extending `text`

This CLI is built to grow. The design rule is **register, don't wire**: a new capability declares itself at init time and every consumer — the commands, `text metrics list`, the docs, `--metrics all` — picks it up from the same registry. There is no switch statement to update and no place where a list of features is repeated.

Three recipes follow, in increasing order of size. All of them quote the actual signatures in this repo.

---

## 1. Add a readability metric

**Cost: one new file. No command wiring.**

The registry lives in [`internal/analyze/registry.go`](../internal/analyze/registry.go). A metric is a value of this type:

```go
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

func Register(m Metric)
```

`Compute` never sees raw text. It receives a `*textproc.Doc` that has already been tokenized once, so a run with `--metrics flesch,amstad` tokenizes the document once and scores it twice. The counts you need are on `d.Stats`: `Sentences`, `Words`, `Syllables`, `Characters`, `PolysyllabicWords`, `MonosyllabicWords`, `LongWords`, plus the precomputed `AvgSentenceLength`, `AvgSyllablesPerWord`, and `AvgWordLength`.

`Result` is what you return:

```go
type Result struct {
	Metric string  `json:"metric"`
	Title  string  `json:"title,omitempty"`
	Score  float64 `json:"score"`
	Level  string  `json:"level,omitempty"`
	Grade  string  `json:"grade,omitempty"`
	Scale  string  `json:"scale,omitempty"`
	Language string `json:"language,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"`
}
```

### Worked example

[`internal/analyze/readability/flesch.go`](../internal/analyze/readability/flesch.go) is the whole pattern, and it is short. The essential parts:

```go
// fleschBands are Flesch's own reading-ease bands, easiest first, with the US
// schooling level each implies.
var fleschBands = []band{
	{90, "very easy", "5th grade"},
	{80, "easy", "6th grade"},
	// ...
	{0, "very confusing", "college graduate"},
}

func Flesch(d *textproc.Doc) (analyze.Result, error) {
	if d.Empty() {
		return analyze.Result{}, emptyErr("flesch")
	}
	score := round(206.835-1.015*asl(d)-84.6*asw(d), 1)
	level, grade := classify(fleschBands, score)
	return analyze.Result{
		Metric:   "flesch",
		Title:    "Flesch Reading Ease",
		Score:    score,
		Level:    level,
		Grade:    grade,
		Scale:    fleschScale,
		Language: language(d, textproc.LangEnglish),
		Extra:    extra(d),
	}, nil
}

func init() {
	analyze.Register(analyze.Metric{
		Name:        "flesch",
		Aliases:     []string{"fre", "flesch-reading-ease"},
		Title:       "Flesch Reading Ease",
		Description: "English reading ease: 206.835 − 1.015×ASL − 84.6×ASW, 0–100, higher is easier.",
		Languages:   []string{string(textproc.LangEnglish)},
		Compute:     Flesch,
	})
}
```

To add, say, a Wiener Sachtextformel: create `internal/analyze/readability/wiener.go`, write the `Compute` function and an `init()` with `Languages: []string{string(textproc.LangGerman)}`, and stop. Because the package is already imported for its side effect, the metric now:

- appears in `text metrics list` and `text metrics show wiener`,
- is selected by `--metrics all`,
- is selected by `--metrics auto` for any German document, via `analyze.ForLanguage`,
- is accepted by name or alias on `--metrics`.

### House rules for a metric

- **Reuse the shared helpers** in [`readability.go`](../internal/analyze/readability/readability.go): `asl(d)`, `asw(d)`, `round(v, places)`, `classify(bands, score)`, `extra(d)`, `emptyErr(name)`, `language(d, fallback)`. They exist so every metric rounds, bands, and fails the same way.
- **Return an `*errs.E`, never a bare error.** `emptyErr` already does this with `errs.CodeEmptyInput`.
- **Do not clamp the score.** A text that scores −12 is information. Only the *band lookup* clamps, because the labels stop at the ends of the scale.
- **Band from the rounded score**, so the printed number and its label cannot contradict each other at a boundary.
- **Set `Languages` honestly.** Use `analyze.AnyLanguage` (`"*"`) only for a genuinely language-agnostic measurement; the language gate is what stops `--metrics auto` from scoring German prose with English constants.
- **Write a table-driven test** next to the file, in the style of `flesch_test.go`.

---

## 2. Add an entity or knowledge provider

**Cost: one new file. No command wiring.**

This is how the planned Wikipedia backend will land. The interface is in [`internal/entity/provider.go`](../internal/entity/provider.go):

```go
type Provider interface {
	// Name is the stable identifier used by --provider and echoed in output.
	Name() string
	// AnalyzeEntities extracts entities from one document.
	AnalyzeEntities(ctx context.Context, text string, opts Options) (*Result, error)
}

func Register(name string, factory func() (Provider, error))
func Open(name string) (Provider, error)
func Names() []string
```

Registration takes a **factory**, not an instance, and `Open` calls it lazily:

```go
// Register adds a provider factory. Factories are called lazily by Open, so
// registering must stay cheap — no clients, no credentials, no network.
```

`Options` is the entire request contract:

```go
type Options struct {
	// Language is a BCP-47/ISO code (e.g. "de", "en", "pt-BR"). Empty means
	// "you figure it out": the provider detects the language itself.
	Language string
	// ServiceAccountPath points at a credentials file, for providers that need
	// one. Empty falls back to the provider's own resolution chain.
	ServiceAccountPath string
	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}
```

Anything a new provider needs that is not in `Options` belongs in its own config section, not in this struct — otherwise every new backend widens the contract for everyone.

### The shape of a new provider

Copy the skeleton from [`internal/entity/google.go`](../internal/entity/google.go):

```go
const ProviderGoogle = "google"

func init() {
	Register(ProviderGoogle, func() (Provider, error) { return &googleProvider{}, nil })
}

func (p *googleProvider) Name() string { return ProviderGoogle }

func (p *googleProvider) AnalyzeEntities(ctx context.Context, text string, opts Options) (*Result, error) {
	c, err := p.clientFor(ctx, opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opts.EffectiveTimeout())
	defer cancel()
	// ... call the API, convert the response ...
}
```

Note that the Google provider builds its client on **first use**, not in the factory: constructing it opens a gRPC connection and resolves credentials, and `text entities --help` should pay for neither. Follow that.

You return the provider-neutral `entity.Result`:

```go
type Result struct {
	Provider string `json:"provider"`
	Language string `json:"language,omitempty"`
	// LanguageSupported reports whether the provider officially supports that
	// language. A false here with entities present means best-effort output.
	LanguageSupported bool     `json:"language_supported"`
	Entities          []Entity `json:"entities"`
}
```

### Knowledge-base identifiers

`entity.go` defines the two metadata keys the planned knowledge-database features key off. **Populate them rather than inventing a synonym:**

```go
const (
	MetaWikipediaURL = "wikipedia_url"
	MetaMID          = "mid"
)
```

Fill `Entity.Metadata` and then call `e.LiftMetadata()`, which promotes those two keys into the top-level `WikipediaURL` and `MID` fields. The promotion rule lives in exactly one place on purpose.

### House rules for a provider

- **Translate your own failures into `*errs.E`.** The caller renders the code, not the prose. `google.go`'s `Translate` maps gRPC status codes onto `auth_denied`, `quota_exceeded`, `api_5xx`, and friends; do the equivalent for your transport. Hints should name the exact console page or doc — an error that just says "permission denied" sends the user searching.
- **Leave fields out rather than lying with a zero value.** Per-entity `Sentiment` is a pointer and most detail fields are `omitempty`, so a thinner provider simply omits what it cannot produce.
- **Be safe for reuse** across calls within a process.
- **Do not filter, sort, or truncate.** `entity.FilterOptions.Apply` does that after the cache, so changing `--top` never costs an API call.

Once registered, the provider is selectable with `text entities --provider <name>` or `text config set entities.provider <name>`, and `entity.Names()` lists it in the error hint when someone asks for one that does not exist.

---

## 3. Add a whole new command

**Cost: one new file plus one line.**

Commands live in [`internal/cmd/`](../internal/cmd/), one file per command, each exporting a `newXCmd()` constructor. The wiring point is the single `AddCommand` call in `Execute` ([`internal/cmd/root.go`](../internal/cmd/root.go)):

```go
root.AddCommand(
	newReadabilityCmd(),
	newMetricsCmd(),
	newEntitiesCmd(),
	newConfigCmd(),
	newUpdateCmd(version),
)
```

Your command gets the shared state from the context and reads its input through it:

```go
func getState(cmd *cobra.Command) *State

// LoadInput resolves the documents to analyse from flags, args, and stdin.
func (s *State) LoadInput(args []string) ([]input.Item, error)

// Language returns the requested analysis language, honoring --lang then
// config, and defaulting to auto-detection.
func (s *State) Language() textproc.Language

// TTLFor returns the effective cache TTL: --cache-ttl wins, then the caller's
// per-command default.
func (s *State) TTLFor(def time.Duration) time.Duration
```

Never touch `os.Stdin` or the `--file` / `--input-format` flags yourself. `LoadInput` implements the documented precedence (`--file` → args → piped stdin, with a terminal stdin being an `empty_input` error rather than a hang) once, for every command.

And you emit through `emitResult`, never `fmt.Println`:

```go
type emitOpts struct {
	// Data is the payload of the JSON envelope.
	Data any
	Meta output.Meta
	// Columns and Rows drive CSV and table rendering.
	Columns []string
	Rows    []output.Row
	// Records is the NDJSON stream: one object per line. Defaults to Rows.
	Records []any
	// Text renders the human-readable form for --output text.
	Text func(w io.Writer) error
}

func emitResult(cmd *cobra.Command, o emitOpts) error
```

Fill in what you can. `Data` is required; the rest degrade gracefully — CSV without `Columns` is an `invalid_args` error, table without `Columns` falls back to JSON, and `--output text` without a `Text` function falls back to JSON.

### Skeleton

```go
package cmd

import (
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

func newWordsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "words [text]",
		Aliases: []string{"wc"},
		Short:   "Count words, sentences, and syllables",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)
			items, err := s.LoadInput(args)
			if err != nil {
				return err
			}
			rows := make([]output.Row, 0, len(items))
			for _, it := range items {
				rows = append(rows, output.Row{"id": it.ID, "chars": len(it.Text)})
			}
			return emitResult(cmd, emitOpts{
				Data:    map[string]any{"documents": rows},
				Meta:    output.Meta{Documents: len(items)},
				Columns: []string{"id", "chars"},
				Rows:    rows,
			})
		},
	}
	return c
}
```

Then add `newWordsCmd(),` to the `AddCommand` list. That is the entire wiring.

### House rules for a command

- Return `*errs.E` values, never bare errors — the exit code is derived from the code.
- Do not print to stdout directly; `emitResult` owns the output contract.
- Do not add a persistent flag for something that is already a root flag.
- Keep `RunE` thin: the analysis belongs in an `internal/` package that can be tested without cobra.

---

## Where the extension points are

| I want to add… | Touch | Wiring needed |
|---|---|---|
| a readability metric | one file in `internal/analyze/readability/` | none — `analyze.Register` in `init()` |
| any other measurement | a package under `internal/analyze/` | none — `analyze.Register` in `init()` |
| an entity / knowledge backend | one file in `internal/entity/` | none — `entity.Register` in `init()` |
| a command | one file in `internal/cmd/` | one line in `Execute` |
| a config key | `internal/config/config.go` | add to the struct, `Get`, `Set`, and `Keys()` |
| an output format | `internal/output/output.go` | add to `Format`, `Valid`, and the switch in `emitResult` |
