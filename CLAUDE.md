# text-cli — repo guide

Go CLI for text analysis. Module `github.com/KLIXPERT-io/text-cli`, binary `text`, main package `./cmd/text`. Released as a single static binary via GoReleaser.

## Layout

```
cmd/text/                      main; version injected via -ldflags
internal/cmd/                  cobra command tree, one file per command
internal/analyze/              metric registry (registry.go)
internal/analyze/readability/  the 12 metrics, one file per family:
                                 flesch.go (en), amstad.go (de), english.go
                                 (flesch-kincaid, gunning-fog, smog,
                                 coleman-liau, ari), wstf.go (wstf1-4 + the
                                 DirectionOf table), lix.go (any language)
internal/lint/                 lint registry + rules_any.go / rules_de.go / rules_en.go
internal/entity/               provider-neutral entity types + registry + Google
                                 backend; sentiment.go and classify.go hold the
                                 optional capability interfaces
internal/knowledge/            knowledge-source registry + Wikipedia backend
internal/strip/                Markdown / HTML → prose, with format sniffing
internal/textproc/             tokenizer: sentences, words, syllables, per language
internal/input/                stdin / file / args resolution, text|lines|jsonl
internal/output/               JSON / TOON / NDJSON / CSV / table / text rendering,
                                 the Meta envelope; toon.go is the TOON encoder
internal/config/               ~/.config/text-cli/config.toml
internal/errs/                 structured errors and exit codes
internal/cache/                disk cache with TTLs
internal/logging/              stderr logging
internal/update/               self-updater
docs/EXTENDING.md              the five extension recipes — read before adding anything
skills/text-cli/SKILL.md       agent skill shipped with the CLI
```

Commands: `readability`, `lint` (+ `lint rules`), `diff`, `metrics`, `entities`, `sentiment`, `classify`, `kb` (+ `lookup`, `search`), `config`, `update`.

## The one rule: register, don't wire

New capabilities declare themselves; nothing enumerates them. There are **four** registries, and they all work the same way:

- **A metric** — one file in `internal/analyze/readability/` (or a new package under `internal/analyze/`) with `analyze.Register(analyze.Metric{...})` in `init()`. It then appears in `text metrics list`, in `--metrics all`, and in `--metrics auto` for its declared languages.
  **The one manual step:** add it to the `directions` table in `internal/analyze/readability/wstf.go`. `readability.DirectionOf` is what `text diff` and the `--fail-under` / `--fail-over` gates read to decide whether up or down is better; a metric missing from it is silently skipped by a gate. `TestDirectionOf` walks `analyze.All()` and fails the build if you forget.
- **A lint rule** — `lint.Register(lint.Rule{...})` in `init()`, in `internal/lint/rules_*.go`. It appears in `text lint rules` and is selected by `--rules auto` for its languages. A rule *name* may be registered more than once for **disjoint** languages (German `passive` and English `passive` are different detectors under one name); overlapping languages panic at init.
- **An entity provider** — one file in `internal/entity/` with `entity.Register(name, factory)` in `init()`. Only `Name()` and `AnalyzeEntities` are required. Sentiment and classification are **capability interfaces** (`SentimentAnalyzer`, `TextClassifier`) that a backend implements only if it can; commands ask via `RequireSentiment` / `RequireClassifier` and get a `provider_unavailable` error naming the backends that would have worked. Never add a method to `Provider`.
- **A knowledge source** — one file in `internal/knowledge/` with `knowledge.Register(name, factory)` in `init()`. Backs `text kb` and `text entities --enrich`.

Every factory must stay cheap — no clients, no credentials, no network; `Open` calls it lazily, and `SentimentProviders()` constructs every registered provider just to type-assert on it.

**A command** is one file in `internal/cmd/` exporting `newXCmd() *cobra.Command`, plus one line in the `root.AddCommand(...)` list in `Execute` (`internal/cmd/root.go`). That is the only wiring point in the repo.

If you find yourself editing more than one file to add a feature of these kinds, you are working against the design. See `docs/EXTENDING.md`.

## House conventions

- **Stripping happens once, in `State.LoadInput`.** Markdown and HTML are reduced to prose there, before any command sees the text, defaulting to `--strip auto`. Never strip inside a command and never re-strip: `readability`, `lint`, and `diff` must not be able to disagree about what the document is. Scoring a fenced code block as prose is a real bug, not a rounding error — it moves this repo's own README a full Flesch band.
- **Every command goes through `emitResult`.** Never `fmt.Println` to stdout. `emitResult` owns format resolution (`--output`, TTY detection), the envelope, and the TOON/CSV/table/NDJSON/text fallbacks. Fill in what your command can: `Data` is required, the rest degrade. TOON is free — it re-encodes the same `{Data, Meta}` envelope, so it cannot drift from the JSON unless someone bypasses `emitResult`.
- **Byte-offset discipline.** `lint.Finding.Start`/`End` are byte offsets into the analysed text and `Excerpt` is *exactly* `text[Start:End]`. That invariant is the feature: it is what lets an LLM apply a precise edit. Never trim, normalise, or prettify an excerpt relative to the span you report; display truncation belongs to the renderer (`lint.Shorten`, used only in the flat formats). The `token` type carries absolute offsets into `Doc.Text` so a rule never converts between sentence-relative and document offsets — the single most likely place to confuse bytes with runes. Remember German: `ä` is two bytes.
- **No silent contract drift.** The JSON envelope is contractual: `{"data": ..., "meta": {...}}`, errors as one JSON line on stderr. Adding a field is fine; renaming or removing one, or changing what a field *means*, is a breaking change and must be called out in the README, SKILL.md, and the release notes. `entity.Probability` is the worked example: the move from Cloud NL v2 to v1 made it permanently 0 for the Google backend, and the field stayed — with a comment saying why — rather than disappearing from consumers' parsers. The same applies to a number's scale: `--min-salience 0.8` meant something under v2's probability and means "return nothing" under v1's salience.
- **`install.sh`, `install.ps1`, and `internal/update`** additionally depend on the archive naming scheme `text_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) defined in `.goreleaser.yaml`. Change one, change all four.
- **Structured errors everywhere.** Return `*errs.E` (`errs.New`, `errs.Newf`, `.WithHint`, `.WithRetry`), never a bare `error` that reaches the user. The exit code is derived from the code by `errs.ExitCode`; a bare error is exit 1 and a wasted opportunity. Hints should name the exact next command or console page. `invalid_args` means "you typed it wrong"; `provider_unavailable` means "this backend cannot do that" — do not conflate them.
- **Input resolution belongs to `internal/input`.** Commands call `State.LoadInput(args)`. Never read `os.Stdin` directly and never add a second `--file`-shaped flag. A terminal stdin with no args must stay an `empty_input` error, never a hang.
- **Tests are table-driven.** One `tests := []struct{...}` slice, `t.Run(tc.name, ...)`, cases named for the behaviour they pin. Test the `internal/` package, not the cobra command, wherever the logic can be lifted out of `RunE`.
- **Language gates are load-bearing.** A metric's or rule's `Languages` field is what stops German prose being scored with English constants. Set it honestly; use `AnyLanguage` (`"*"`) only for genuinely language-agnostic work.
- **Do not clamp scores**, only band labels — and only where the scale really ends. Reading ease clamps to 0–100 for the *label*; grade levels do not clamp, and a WSTF outside 4–15 is labelled `unter der Skala` / `über der Skala` rather than pretending to be `sehr leicht`.
- **Paid calls are cached on the provider's inputs, never on the filters.** `entities`/`sentiment`/`classify` key on (provider, language, text), so changing `--top`, `--types`, or `--min-salience` re-filters a cached payload instead of paying again. Keep it that way.
- **German is domain vocabulary, not localisation.** Amstad and WSTF level labels, and the German lint messages and suggestions, stay in German because that is what the formulas and the style guides call them. Do not translate them and do not add an i18n layer.
- **Comments explain why, not what.** The existing files are the style reference: they justify the constants, the fallbacks, and the design choices, and skip narrating the code.

## Build and test

```bash
make build            # ./text, with VERSION baked in
make test             # go test ./...
make vet              # go vet ./...
make fmt              # gofmt -l -w ./cmd ./internal
make install          # go install
make release-snapshot # goreleaser --snapshot, no publish
```

CI (`.github/workflows/ci.yml`) runs build, vet, a `gofmt -l .` check that fails on any output, and `go test ./... -count=1` on every push to `main` and every pull request. Run `make fmt` before committing.

## Releasing

`VERSION` at the repo root is the source of truth. Bump it and merge to `main`: `tag-and-release.yml` creates the `vX.Y.Z` tag and calls `release.yml`, which runs GoReleaser and publishes the archives plus `checksums.txt`. Do not tag by hand unless the workflow is broken.
