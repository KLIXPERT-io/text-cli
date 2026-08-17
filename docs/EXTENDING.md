# Extending `text`

This CLI is built to grow. The design rule is **register, don't wire**: a new capability declares itself at init time and every consumer — the commands, `text metrics list`, `text lint rules`, the docs, `--metrics all` — picks it up from the same registry. There is no switch statement to update and no place where a list of features is repeated.

There are **six** registries:

| registry | package | adds | discovery command |
|---|---|---|---|
| metrics | [`internal/analyze`](../internal/analyze/registry.go) | a text measurement | `text metrics list` |
| entity providers | [`internal/entity`](../internal/entity/provider.go) | an entity / sentiment / classification backend | `--provider` hint on error |
| lint rules | [`internal/lint`](../internal/lint/lint.go) | a prose check | `text lint rules` |
| knowledge sources | [`internal/knowledge`](../internal/knowledge/knowledge.go) | an encyclopedia backend | `--source` hint on error |
| fetchers | [`internal/fetch`](../internal/fetch/fetch.go) | a URL-to-prose backend | `--fetcher` hint on error |
| research sources | [`internal/research`](../internal/research/research.go) | a literature index | `--source` hint on error |

Seven recipes follow — the six registries, then a whole new command. All of them quote the actual signatures in this repo.

---

## 1. Add a readability metric

**Cost: one new file plus one line in the direction table. No command wiring.**

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
func Get(name string) (Metric, bool)          // resolves aliases, case-insensitively
func All() []Metric                            // sorted by name
func ForLanguage(lang textproc.Language) []Metric  // what --metrics auto resolves to
func Names() []string
```

`Compute` never sees raw text. It receives a `*textproc.Doc` that has already been tokenized once, so a run with `--metrics flesch,amstad` tokenizes the document once and scores it twice. The counts you need are on `d.Stats`: `Sentences`, `Words`, `Syllables`, `Characters`, `PolysyllabicWords`, `MonosyllabicWords`, `LongWords`, plus the precomputed `AvgSentenceLength`, `AvgSyllablesPerWord`, and `AvgWordLength`.

`Result` is what you return:

```go
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

Because the package is already imported for its side effect, a metric registered this way now:

- appears in `text metrics list` and `text metrics show <name>`,
- is selected by `--metrics all`,
- is selected by `--metrics auto` for its declared languages, via `analyze.ForLanguage`,
- is accepted by name or alias on `--metrics`.

### The one place that is not automatic: `DirectionOf`

`analyze.Metric` has no direction field, and that registry is not widened for it. The score direction lives in a hand-maintained table in [`internal/analyze/readability/wstf.go`](../internal/analyze/readability/wstf.go):

```go
type Direction string

const (
	// HigherIsEasier marks a reading-ease score (Flesch, Amstad): 0–100, up is
	// easier. A "must be at least X" gate is meaningful; "at most X" is not.
	HigherIsEasier Direction = "higher_is_easier"
	// LowerIsEasier marks a grade-level score (WSTF, LIX, Flesch-Kincaid,
	// Gunning Fog, SMOG, Coleman-Liau, ARI): the number is a schooling level,
	// down is easier.
	LowerIsEasier Direction = "lower_is_easier"
)

var directions = map[string]Direction{
	"flesch": HigherIsEasier,
	"amstad": HigherIsEasier,
	"wstf1":  LowerIsEasier,
	// ... every registered metric, keyed by canonical name
}

// DirectionOf reports which way a metric's score runs, resolving aliases
// through the registry. The second return value is false for a metric that has
// not declared a direction.
func DirectionOf(name string) (Direction, bool)
```

It is a table rather than something parsed out of the `Scale` string, because a threshold check must not depend on prose that is written in two languages and free to be reworded. `text diff` and the `--fail-under` / `--fail-over` gates read it; a metric missing from it would be silently skipped by a CI gate.

**A test enforces this.** `TestDirectionOf` in [`wstf_test.go`](../internal/analyze/readability/wstf_test.go) walks `analyze.All()` and fails the build for any registered metric without an entry:

```go
for _, m := range analyze.All() {
	if _, known := DirectionOf(m.Name); !known {
		t.Fatalf("metric %q declares no direction: add it to the directions table", m.Name)
	}
}
```

So the sequence is: add the file, run `go test ./...`, and let the failure tell you to add the line.

### House rules for a metric

- **Reuse the shared helpers** in [`readability.go`](../internal/analyze/readability/readability.go): `asl(d)`, `asw(d)`, `round(v, places)`, `classify(bands, score)`, `classifyGrade(bands, score, inclusiveMax)`, `extra(d)`, `emptyErr(name)`, `language(d, fallback)`. They exist so every metric rounds, bands, and fails the same way.
- **Return an `*errs.E`, never a bare error.** `emptyErr` already does this with `errs.CodeEmptyInput`.
- **Do not clamp the score.** A text that scores −12 is information. Only the *band lookup* clamps, and only where the scale really ends — `classify` clamps to 0–100 for reading ease; `classifyGrade` does not, because a grade level has no meaningful ceiling, and a WSTF outside 4–15 is labelled `unter der Skala` / `über der Skala` rather than pretending to be `sehr leicht`.
- **Band from the rounded score**, so the printed number and its label cannot contradict each other at a boundary.
- **Set `Languages` honestly.** Use `analyze.AnyLanguage` (`"*"`) only for a genuinely language-agnostic measurement; the language gate is what stops `--metrics auto` from scoring German prose with English constants.
- **Write a table-driven test** next to the file, in the style of `flesch_test.go`.

---

## 2. Add an entity provider

**Cost: one new file. No command wiring.**

The interface is in [`internal/entity/provider.go`](../internal/entity/provider.go):

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
func Registered(name string) bool
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

func (o Options) EffectiveTimeout() time.Duration
```

Anything a new provider needs that is not in `Options` belongs in its own config section, not in this struct — otherwise every new backend widens the contract for everyone.

### Capability interfaces: sentiment and classification are optional

`Provider` is deliberately the *minimum*. Sentiment and classification live in separate interfaces next to it, and a backend implements them only if it really can:

```go
// SentimentAnalyzer is the optional capability of scoring how a document feels.
//
// It is deliberately NOT part of Provider. A knowledge-base backend can name
// entities perfectly well without having any notion of polarity, and folding
// this method into Provider would force every future backend to stub out
// something it cannot honour.
type SentimentAnalyzer interface {
	AnalyzeSentiment(ctx context.Context, text string, opts Options) (*SentimentResult, error)
}

// TextClassifier is the optional capability of sorting a document into content
// categories. Same reasoning as SentimentAnalyzer: capability, not requirement.
type TextClassifier interface {
	ClassifyText(ctx context.Context, text string, opts Options) (*ClassificationResult, error)
}

// Commands ask for the capability rather than assuming it.
func RequireSentiment(p Provider) (SentimentAnalyzer, error)
func RequireClassifier(p Provider) (TextClassifier, error)

// …and these name the backends that would have worked, for the error hint.
func SentimentProviders() []string
func ClassifierProviders() []string
```

`text sentiment` calls `RequireSentiment(p)`. A provider that does not implement it gets a `provider_unavailable` error (exit 6) whose hint lists the providers that do — **not** an `invalid_args` error, because the provider name may have come from `entities.provider` in the config rather than from a flag the user typed. An unknown provider *name* stays `invalid_args`; that one really is a bad argument. The distinction is what lets an agent tell "you typo'd the backend" from "this backend cannot do sentiment, try another".

Follow the same shape for any further optional capability: a new interface, a `RequireX`, an `XProviders()`. Never add a method to `Provider`.

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

Note that the Google provider builds its client on **first use**, not in the factory: constructing it opens a gRPC connection and resolves credentials, and `text entities --help` should pay for neither. Follow that. It matters more than it looks: `SentimentProviders()` constructs every registered provider just to type-assert on it, which is only affordable because factories are cheap.

You return the provider-neutral `entity.Result`:

```go
type Result struct {
	Provider string `json:"provider"`
	// Language is the document language the provider used.
	Language string `json:"language,omitempty"`
	// LanguageSupported reports whether the provider officially supports that
	// language. A false here with entities present means best-effort output.
	//
	// Not every backend has such a signal: Cloud Natural Language v1 rejects an
	// unsupported language with InvalidArgument instead of answering with a
	// flag, so a successful v1 response sets this true.
	LanguageSupported bool     `json:"language_supported"`
	Entities          []Entity `json:"entities"`
}
```

### Salience versus probability

`Entity` carries both, and they are not the same kind of number:

```go
// Salience is how central this entity is to its document, in [0, 1]. Within
// one document the salience of all entities sums to roughly 1.0, so it is a
// share of attention, not a confidence.
Salience float64 `json:"salience"`
// Probability is the confidence that this really is an entity of this type.
// The Google v1 backend does NOT report it, so it is 0 for every Google
// entity today.
Probability float64 `json:"probability"`
```

Ranking and filtering go through `Entity.Score()`, which prefers salience and falls back to probability, so a backend that reports only one of the two still sorts correctly. Populate whichever you genuinely have and leave the other at zero — do not synthesise one from the other.

`Round4` is the rounding helper: salience arrives as a `float32` whose widening is full of noise, and four decimals is more precision than the ranking carries.

### Knowledge-base identifiers

`entity.go` defines the two metadata keys the knowledge-database features key off. **Populate them rather than inventing a synonym:**

```go
const (
	MetaWikipediaURL = "wikipedia_url"
	MetaMID          = "mid"
)
```

Fill `Entity.Metadata` and then call `e.LiftMetadata()`, which promotes those two keys into the top-level `WikipediaURL` and `MID` fields. The promotion rule lives in exactly one place on purpose, and `text entities --enrich` depends on it: it parses the language edition and the article title straight out of `WikipediaURL`.

### House rules for a provider

- **Translate your own failures into `*errs.E`.** The caller renders the code, not the prose. `google.go`'s `Translate` maps gRPC status codes onto `auth_denied`, `quota_exceeded`, `api_5xx`, and friends; do the equivalent for your transport. Hints should name the exact console page or doc — an error that just says "permission denied" sends the user searching.
- **Leave fields out rather than lying with a zero value.** Per-entity `Sentiment` is a pointer and most detail fields are `omitempty`, so a thinner provider simply omits what it cannot produce.
- **Be safe for reuse** across calls within a process.
- **Do not filter, sort, or truncate.** `entity.FilterOptions.Apply` does that after the cache, so changing `--top` never costs an API call. `entity.Aggregate` merges across documents, also after the cache.
- **Implement `io.Closer`** if you hold a connection; the command closes you if you do, and does not if you do not.

Once registered, the provider is selectable with `text entities --provider <name>` or `text config set entities.provider <name>`, and `entity.Names()` lists it in the error hint when someone asks for one that does not exist.

---

## 3. Add a lint rule

**Cost: one new `Register` call. No command wiring.**

The registry is in [`internal/lint/lint.go`](../internal/lint/lint.go):

```go
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
	// override it per finding.
	Severity Severity
	// Check inspects an already-tokenized document.
	Check func(d *textproc.Doc, cfg Config) []Finding
}

func Register(r Rule)
func All() []Rule
func ForLanguage(lang textproc.Language) []Rule      // what --rules auto resolves to
func Get(name string) (Rule, bool)
func GetFor(name string, lang textproc.Language) (Rule, exists bool, supportsLang bool)
func Variants(name string) []Rule
func Names() []string
func Run(d *textproc.Doc, rules []Rule, cfg Config) []Finding
```

Registration looks like this ([`rules_en.go`](../internal/lint/rules_en.go)):

```go
func init() {
	en := []string{string(textproc.LangEnglish)}
	Register(Rule{
		Name:        "passive",
		Title:       "Passive voice",
		Description: "A be-form plus a past participle — the actor is missing.",
		Languages:   en,
		Severity:    SeverityWarn,
		Check:       checkPassiveEN,
	})
	// ...
}
```

### Same name, different languages

Unlike the metric registry, a rule name may be registered **more than once**, as long as the variants cover disjoint languages:

```go
// German passive voice and English passive voice are genuinely different
// detectors, but calling them "passive-de" and "passive-en" would push that
// implementation detail into every summary key and every CI threshold. So the
// registry keys by name and resolves the variant per document language.
```

`Register` panics on a name already registered for an *overlapping* language set — including `AnyLanguage`, which overlaps everything. `GetFor` is how a command resolves the right variant, and its two booleans let it distinguish "no such rule" from "that rule does not do German".

### Findings carry byte offsets, and that is the contract

```go
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
	// Value is the measured number behind the finding, when the rule has one.
	Value float64 `json:"value,omitempty"`
}
```

The whole point of `text lint` is that an LLM or a script can slice the source at `[Start:End]` and replace it. **Never narrow, widen, trim, or normalise `Excerpt` relative to the span you report.** The `token` type inside the package carries absolute byte offsets into `Doc.Text` precisely so a rule never has to translate between sentence-relative and document offsets — the single most likely place to confuse bytes with runes.

### Thresholds live in `Config`

Rules read their numbers from `lint.Config` rather than from constants, so they are tunable from the command line:

```go
type Config struct {
	MaxSentenceWords      int     // long-sentence fires above this (default 25)
	WarnSentenceWords     int     // ...and warns rather than informs above this
	MaxWordChars          int     // long-word fires above this (default 20)
	Worst                 int     // cap on hard-sentence findings (default 5)
	HardSentenceScore     float64
	HardSentenceWarnScore float64
	MinHardSentenceWords  int
	RepeatWindow          int
	MinRepeatWordChars    int
	RepeatedStartRun      int
	MinVarianceSentences  int
	// ...
}

func Defaults() Config
func (c Config) WithDefaults() Config  // fills only the zero fields
```

`WithDefaults` fills only the zero fields, so a caller can pass `Config{MaxSentenceWords: 30}` and nothing else. If your rule needs a threshold, add a field with a documented default rather than hard-coding a number in the check.

### House rules for a rule

- **The package deliberately does not import `internal/analyze`.** Lint findings are per-sentence and per-phrase, the metric registry is per-document, and the two would otherwise have to know about each other. The small amount of readability arithmetic lint needs is duplicated in `rules_any.go` on purpose. Do not "fix" this.
- **Return `[]Finding{}`, not `nil`**, so the JSON is an empty array rather than `null`.
- **Prefer a false positive to a miss** for structural rules (passive voice, nominalization): a flagged sentence costs the reader a glance, an unflagged passive costs the document its actor. Say so in a comment, as the existing rules do.
- **Set `Severity` honestly.** `warn` is "this is wrong", `info` is "worth a look" — `--severity warn` and `--fail-on-findings` are what a CI gate keys on. There are two levels on purpose: a five-step scale only moves the argument from the text to the scale.
- **Write a table-driven test** next to the file, asserting the offsets as well as the count.

---

## 4. Add a knowledge source

**Cost: one new file. No command wiring.**

The interface is in [`internal/knowledge/knowledge.go`](../internal/knowledge/knowledge.go):

```go
type Source interface {
	// Name is the stable identifier used by --source and echoed in output.
	Name() string
	// Lookup resolves one title. A missing article is errs.CodeNotFound, not a
	// nil article: "no such page" is an answer the caller must be able to
	// branch on.
	Lookup(ctx context.Context, title, lang string) (*Article, error)
	// Search returns candidate titles for a free-text query, best first.
	Search(ctx context.Context, query, lang string, limit int) ([]SearchHit, error)
}

func Register(name string, factory func() (Source, error))
func Open(name string) (Source, error)
func Names() []string
func Registered(name string) bool
```

Same lazy-factory rule as the entity registry, and the same capability pattern for anything optional:

```go
// TimeoutSetter is implemented by sources whose per-request deadline can be
// configured. It is deliberately not part of Source: a source backed by a local
// index has nothing to time out, and should not have to implement a no-op. The
// command type-asserts for it, the same way it type-asserts for io.Closer.
type TimeoutSetter interface {
	SetTimeout(d time.Duration)
}
```

Registration, from [`wikipedia.go`](../internal/knowledge/wikipedia.go):

```go
const SourceWikipedia = "wikipedia"

func init() {
	Register(SourceWikipedia, func() (Source, error) { return newWikipedia(), nil })
}
```

The result types are source-neutral, so a caller parsing `text kb` output does not have to change when a second source lands:

```go
type Article struct {
	// Title is the resolved title, which may differ from the one requested:
	// a redirect ("JFK") lands on the canonical page.
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`  // the short one-liner
	Extract     string `json:"extract,omitempty"`      // lead paragraph, plain text
	URL         string `json:"url,omitempty"`
	// Lang is the language edition the article came from, not the language of
	// the query.
	Lang         string   `json:"lang,omitempty"`
	ThumbnailURL string   `json:"thumbnail_url,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
	// Disambiguation reports that the title resolved to a disambiguation page
	// rather than to a thing.
	Disambiguation bool `json:"disambiguation,omitempty"`
}

type SearchHit struct {
	Title       string  `json:"title"`
	Description string  `json:"description,omitempty"`
	URL         string  `json:"url,omitempty"`
	Score       float64 `json:"score,omitempty"`
}
```

### Shared helpers you must use

```go
// NormalizeLang turns the CLI's --lang value into a concrete language edition:
// "" and "auto" become "en" (there is no auto.wikipedia.org), "de-AT" becomes
// "de", and anything that is not letters and digits is rejected rather than
// interpolated into a hostname.
func NormalizeLang(lang string) string

// CanonicalTitle is the form a title is cached and requested under: underscores
// folded to spaces, internal whitespace collapsed. Case is deliberately NOT
// folded — Wikipedia titles are case-sensitive after the first character
// ("iPhone", "eBay").
func CanonicalTitle(title string) string

// ParseWikipediaURL pulls the article title and the language edition out of a
// Wikipedia URL. It is the join between `text entities` and this package.
func ParseWikipediaURL(raw string) (title, lang string, ok bool)
```

`CanonicalTitle` is what makes `text entities --enrich` free where it can be: a title derived from a `wikipedia_url` and a title a human typed at `text kb lookup` produce the same cache key. `ParseWikipediaURL` takes the language from the URL's subdomain, not from `--lang`, because the entity provider already picked an edition — looking a German document's titles up in the English edition would miss.

### House rules for a source

- **A missing article is `errs.CodeNotFound`, not a nil `*Article`.** The command turns a single miss into exit 4 and a miss inside a batch into a stderr line; it cannot do either if you return `(nil, nil)`.
- **Translate transport failures into `*errs.E`**, as for entity providers.
- **Bound every call.** `DefaultTimeout` is 15s. Public encyclopedia APIs are fast when healthy and unbounded when not.
- **Cap the response body.** `wikipedia.go` uses `maxBodyBytes = 4 << 20`.
- **Leave fields out rather than lying with a zero value.** Everything but `Title` is `omitempty` so a thinner backend simply omits it.
- **Make the base URL injectable** so tests can point at an `httptest.Server` and the suite never touches the real API.

---

## 5. Add a fetcher

**Cost: one new file. No command wiring.**

A fetcher turns a URL into prose. It is what backs `text fetch` and the `--url` flag on every analysis command. The interface is in [`internal/fetch/fetch.go`](../internal/fetch/fetch.go):

```go
type Fetcher interface {
	// Name is the stable identifier used by --fetcher and echoed in output.
	Name() string
	// Fetch reads one URL. A page that does not exist is errs.CodeNotFound.
	Fetch(ctx context.Context, url string, opts Options) (*Page, error)
}

func Register(name string, factory func() (Fetcher, error))
```

Register it in an `init()` and it becomes the backend for `--fetcher <name>`, for `fetch.provider` in the config, and — if it is the only one registered — the default:

```go
const FetcherMine = "mine"

func init() {
	Register(FetcherMine, func() (Fetcher, error) { return &mineFetcher{}, nil })
}
```

### Return markdown, not plain text

This is the one substantive rule, and it is not a style preference. `State.LoadInput` runs every document through `internal/strip` before anything measures it, and that pass already knows how to reduce markdown to prose correctly. A backend that returns pre-flattened text hands the tokenizer a heading glued onto the sentence that follows it, which inflates average sentence length and moves the readability score — silently, on every page.

Returning markdown is what makes `text readability --url X` and `text fetch X --output text | text readability` produce identical numbers. If those two disagree, this rule was broken.

### Credentials go in a capability interface, not in `Options`

`Options` is the whole contract, and everything in it is something *any* fetcher could act on: `MainContentOnly`, `IncludeLinks`, `MaxAge`, `Timeout`. An API key is not — a fetcher driving a local headless browser has none. So the credential arrives through an optional interface the command type-asserts for:

```go
type APIConfigurer interface {
	SetAPIKey(key string)
	SetBaseURL(url string)
}
```

Implement it only if you need it. This is the same reasoning as `knowledge.TimeoutSetter`, and it is also what keeps factories cheap: the key is injected *after* construction, so `Register`'s "no credentials, no network" rule holds even though every backend needs one.

### House rules for a fetcher

- **A page with no text is `errs.CodeEmptyInput` with a hint**, never a `*Page` with an empty `Content`. A login wall must not reach a later command as a mysterious empty document.
- **Leave fields out rather than lying with a zero value.** Only `URL` and `Content` are guaranteed; everything else is `omitempty`.
- **Set `RequestedURL` only when it differs from `URL`.** Echoing the same string twice is noise in a format whose point is being compact.
- **Validate the URL before spending a call.** `firecrawl.ValidateURL` rejects a non-http scheme locally, which is a better answer than a 400 relayed a second later.
- **Make the base URL injectable** so tests point at an `httptest.Server`.

---

## 6. Add a research source

**Cost: one new file. No command wiring.**

A research source is a literature index. Searching is the only thing every source must do; reading one paper by id and finding related work are **capability interfaces**, exactly as sentiment and classification are for entity providers. The interfaces are in [`internal/research/research.go`](../internal/research/research.go):

```go
type Source interface {
	Name() string
	SearchPapers(ctx context.Context, opts SearchOptions) ([]Paper, error)
}

// Optional. Implement only what your index can actually do.
type PaperInspector interface {
	InspectPaper(ctx context.Context, id string, opts InspectOptions) (*PaperDetail, error)
}
type SimilarFinder interface {
	SimilarPapers(ctx context.Context, id string, opts SimilarOptions) ([]Paper, error)
}
```

An index built from titles and abstracts alone implements `Source` and stops there. `text research paper` then fails with a `provider_unavailable` error that **names the sources that would have worked** — because `InspectorSources()` constructs every registered source just to type-assert on it. That is only affordable because factories are cheap, which is why `Register` forbids clients, credentials, and network calls inside one.

Never add a method to `Source` to accommodate one backend.

### Identifiers are namespaced, and unknown ones resolve to nothing

`Paper.PrimaryID` is `arxiv:1706.03762`, `doi:10.1145/3442188`, `pmid:18027780`, `pmcid:PMC1431743`. Two rules follow:

- **A bare id is rejected, not guessed.** `NormalizeID("1706.03762")` is an `invalid_args` error, because that string is as plausibly a PMID as an arXiv id and guessing would be wrong about half the time.
- **`LandingURL` returns `""` for a namespace it does not know.** A guessed URL in cited output is worse than no URL. Add a namespace to the switch rather than constructing one at the call site.

### House rules for a source

- **Return `[]Paper{}`, never `nil`.** An empty result renders as `[]` rather than `null`, so a consumer can iterate without a nil check.
- **`Score` is comparable within one result set and meaningless across two.** The search ranking and the similar-papers ranking are different scales — which is why nothing in this repo thresholds on it.
- **Dates are strings, verbatim.** The indexes behind one source are not uniform (an RFC 1123 arXiv timestamp next to a bare `YYYY-MM-DD`), and normalising to `time.Time` would mean discarding the ones that do not parse.
- **`Authors` is one string, not a parsed list.** Splitting it would invent a structure the source does not guarantee.
- **Require a key only if you truly need one.** The Firecrawl source deliberately does *not* call `firecrawl.RequireKey`: the index answers unauthenticated requests, and demanding a key would fail calls that would have succeeded.

---

## 7. Add a whole new command

**Cost: one new file plus one line.**

Commands live in [`internal/cmd/`](../internal/cmd/), one file per command, each exporting a `newXCmd()` constructor. The wiring point is the single `AddCommand` call in `Execute` ([`internal/cmd/root.go`](../internal/cmd/root.go)):

```go
root.AddCommand(
	newReadabilityCmd(),
	newMetricsCmd(),
	newEntitiesCmd(),
	newSentimentCmd(),
	newClassifyCmd(),
	newDiffCmd(),
	newLintCmd(),
	newKBCmd(),
	newFetchCmd(),
	newResearchCmd(),
	newConfigCmd(),
	newUpdateCmd(version),
)
```

Your command gets the shared state from the context and reads its input through it:

```go
func getState(cmd *cobra.Command) *State

// LoadInput resolves the documents to analyse from flags, args, and stdin, and
// reduces markup to prose before anything measures it.
func (s *State) LoadInput(args []string) ([]input.Item, error)

// Language returns the requested analysis language, honoring --lang then
// config, and defaulting to auto-detection.
func (s *State) Language() textproc.Language

// TTLFor returns the effective cache TTL: --cache-ttl wins, then the caller's
// per-command default.
func (s *State) TTLFor(def time.Duration) time.Duration
```

Never touch `os.Stdin` or the `--file` / `--input-format` / `--strip` flags yourself. `LoadInput` implements the documented precedence (`--file` → args → piped stdin, with a terminal stdin being an `empty_input` error rather than a hang) **and the markup stripping**, once, for every command. That is deliberate: a command that forgot to strip would report a confidently incorrect number, and two commands that stripped differently would disagree about what the document is.

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

Fill in what you can. `Data` is required; the rest degrade gracefully — CSV without `Columns` is an `invalid_args` error, table without `Columns` falls back to JSON, and `--output text` without a `Text` function falls back to JSON. `--output toon` needs nothing extra at all: it re-encodes the same `{Data, Meta}` envelope, so a new command gets TOON for free and cannot drift from its own JSON.

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
- A single document emits its object as `data` directly; a batch emits a list of the same objects under `data.documents`. Keep both shapes identical so a consumer parses one struct.
- If the command calls a paid backend, cache on exactly the inputs the backend saw — never on the filters, so tightening `--top` re-filters a cached payload instead of paying again.

---

## Where the extension points are

| I want to add… | Touch | Wiring needed |
|---|---|---|
| a readability metric | one file in `internal/analyze/readability/` | `analyze.Register` in `init()`, plus one line in the `directions` table in `wstf.go` |
| any other measurement | a package under `internal/analyze/` | same |
| an entity / sentiment / classification backend | one file in `internal/entity/` | none — `entity.Register` in `init()` |
| an optional provider capability | `internal/entity/provider.go` | a new interface plus a `RequireX` — never a method on `Provider` |
| a lint rule | one file (or one `Register` call) in `internal/lint/` | none — `lint.Register` in `init()` |
| a knowledge database | one file in `internal/knowledge/` | none — `knowledge.Register` in `init()` |
| a URL-to-prose backend | one file in `internal/fetch/` | none — `fetch.Register` in `init()` |
| a literature index | one file in `internal/research/` | none — `research.Register` in `init()` |
| an optional research capability | `internal/research/research.go` | a new interface plus a `RequireX` — never a method on `Source` |
| a command | one file in `internal/cmd/` | one line in `Execute` |
| a config key | `internal/config/config.go` | add to the struct, `Get`, `Set`, and `Keys()` |
| an output format | `internal/output/output.go` | add to `Format`, `Valid`, and the switch in `emitResult` |
