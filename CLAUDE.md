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
internal/firecrawl/            shared Firecrawl HTTP client: key resolution and
                                 error translation, no domain types
internal/fetch/                Fetcher registry + Firecrawl scrape backend +
                                 Google Docs backend; backs `text fetch` and
                                 the global --url, routed per URL
internal/gdocs/                Google Docs: Docs API for text, Drive API for
                                 comments, service-account auth, the
                                 Document→markdown converter
internal/research/             research-source registry + Firecrawl papers
                                 backend; SearchPapers is required, inspect and
                                 related-work are capability interfaces
internal/doc/                  document decoders: doc.go (registry, Document,
                                 binary detection), container.go (the shared zip
                                 + XML helpers), markdown.go (mdBuilder — the
                                 one definition of what a heading looks like),
                                 then one file per format: pdf.go, docx.go,
                                 pptx.go, odf.go (odt + ods), epub.go, rtf.go
internal/strip/                Markdown / HTML → prose, with format sniffing
internal/textproc/             tokenizer: sentences, words, syllables, per language
internal/input/                stdin / file / args resolution, text|lines|jsonl;
                                 --file is repeatable and --from routes a file
                                 through internal/doc before the strip pass
internal/output/               JSON / TOON / NDJSON / CSV / table / text rendering,
                                 the Meta envelope; toon.go is the TOON encoder
internal/config/               ~/.config/text-cli/config.toml
internal/errs/                 structured errors and exit codes
internal/cache/                disk cache with TTLs
internal/logging/              stderr logging
internal/update/               self-updater
docs/EXTENDING.md              the eight extension recipes — read before adding anything
skills/text-cli/                agent skill shipped with the CLI:
                                 SKILL.md is the router (loaded always) and
                                 references/*.md one file per capability
                                 (loaded on demand)
```

Commands: `readability`, `lint` (+ `lint rules`), `diff`, `extract`, `metrics`, `entities`, `sentiment`, `classify`, `kb` (+ `lookup`, `search`), `fetch`, `docs` (+ `read`, `comments`, `comment`, `reply`, `replace`, `insert`, `whoami`), `research` (+ `papers`, `paper`, `similar`), `config`, `update`.

## The one rule: register, don't wire

New capabilities declare themselves; nothing enumerates them. There are **seven** registries, and they all work the same way:

- **A metric** — one file in `internal/analyze/readability/` (or a new package under `internal/analyze/`) with `analyze.Register(analyze.Metric{...})` in `init()`. It then appears in `text metrics list`, in `--metrics all`, and in `--metrics auto` for its declared languages.
  **The one manual step:** add it to the `directions` table in `internal/analyze/readability/wstf.go`. `readability.DirectionOf` is what `text diff` and the `--fail-under` / `--fail-over` gates read to decide whether up or down is better; a metric missing from it is silently skipped by a gate. `TestDirectionOf` walks `analyze.All()` and fails the build if you forget.
- **A lint rule** — `lint.Register(lint.Rule{...})` in `init()`, in `internal/lint/rules_*.go`. It appears in `text lint rules` and is selected by `--rules auto` for its languages. A rule *name* may be registered more than once for **disjoint** languages (German `passive` and English `passive` are different detectors under one name); overlapping languages panic at init.
- **An entity provider** — one file in `internal/entity/` with `entity.Register(name, factory)` in `init()`. Only `Name()` and `AnalyzeEntities` are required. Sentiment and classification are **capability interfaces** (`SentimentAnalyzer`, `TextClassifier`) that a backend implements only if it can; commands ask via `RequireSentiment` / `RequireClassifier` and get a `provider_unavailable` error naming the backends that would have worked. Never add a method to `Provider`.
- **A knowledge source** — one file in `internal/knowledge/` with `knowledge.Register(name, factory)` in `init()`. Backs `text kb` and `text entities --enrich`.
- **A fetcher** — one file in `internal/fetch/` with `fetch.Register(name, factory)` in `init()`. Backs `text fetch` and the global `--url`. **It must return markdown, not plain text**: `State.LoadInput` runs every document through `internal/strip`, and a backend that pre-flattens hands the tokenizer a heading glued to the next sentence. That is what makes `--url` and `text fetch … | text …` produce identical scores and identical byte offsets. Credentials arrive via capability interfaces, never via `Options`: `APIConfigurer` for an API key, `ServiceAccountConfigurer` for a Google key file. Two more capabilities live here: `URLMatcher` lets a backend claim the URLs only it can read (`fetch.ForURL`, how a Google Docs URL reaches the `gdocs` backend instead of the scraper), and `CacheTTLHinter` lets a backend opt out of the page cache (`gdocs` returns 0 — free to read, and mutable).
- **A document decoder** — one file in `internal/doc/` with `doc.Register(name, factory)` in `init()`. Backs `--file report.pdf`, `--from`, and the sniff on stdin; shipped: `pdf`, `docx`, `pptx`, `odt`, `ods`, `epub`, `rtf`. **It must return markdown, not plain text**, for exactly the reason a fetcher must: `State.LoadInput` runs every document through `internal/strip`, and a decoder that flattens its own headings hands the tokenizer one document-sized sentence. Build the output with `mdBuilder` (`markdown.go`), never a bare `strings.Builder` — it is the one definition of what a heading looks like, and its `Heading`/`Para`/`Item`/`Row` methods each separate themselves from the previous block, which is the only thing the strip pass cannot recover if a decoder gets it wrong. `Extensions()` is matched before `Sniff()` by `doc.ForFile`, because an extension is what the user typed and a magic number is only a guess — and because the four zip-based formats share one. Two error codes and they are not interchangeable: a file that is not this format is `invalid_args`, a file that *is* this format but holds no text is `doc.EmptyErr` (`empty_input`, "it needs OCR"). Every unexported identifier is prefixed with the format name, because all the decoders share one package.
- **A research source** — one file in `internal/research/` with `research.Register(name, factory)` in `init()`. `SearchPapers` is the only required method; `PaperInspector` and `SimilarFinder` are capability interfaces, asked for via `RequireInspector` / `RequireSimilarFinder`. Never add a method to `Source`.

Every factory must stay cheap — no clients, no credentials, no network; `Open` calls it lazily, and `SentimentProviders()` constructs every registered provider just to type-assert on it.

**A command** is one file in `internal/cmd/` exporting `newXCmd() *cobra.Command`, plus one line in the `root.AddCommand(...)` list in `Execute` (`internal/cmd/root.go`). That is the only wiring point in the repo.

If you find yourself editing more than one file to add a feature of these kinds, you are working against the design. See `docs/EXTENDING.md`.

## House conventions

- **`text docs` is the only thing that writes, and it is built like it.** Every other command reads text and prints numbers. The Google Docs write path is deliberately narrow — one literal replacement, one inserted paragraph — and three properties are load-bearing, not polish: the document is **read first** so an ambiguous `--find` is refused before anything is sent (`invalid_args`) and a `--find` that matches nothing is `not_found` rather than a successful no-op; every `batchUpdate` carries `writeControl.requiredRevisionId` from that read, so a concurrent edit is **rejected** instead of overwritten; and read commands ask for read-only scopes so their token cannot modify anything. Do not add a write that skips the read, and do not widen the surface to "apply this markdown" — the CLI must not become a document formatter.
- **Comments are Drive, text is Docs.** The live Docs API v1 has no comment surface; `documents.get` cannot return one. Comments, replies, and resolving go through the Drive API's `comments`/`replies` resources, which is why a comment command asks for a Drive scope and a body read does not. A comment created through the API is unanchored — the editor's anchor is an opaque region descriptor no API can mint — so `docs comment` quotes the passage into the text instead of pretending otherwise.
- **Two renderings of a document, on purpose.** `renderMarkdown` feeds the strip pass and every downstream command; `renderPlain` is the document's literal text and is what an occurrence count for `--find` is computed against. A search string that spans a bold word matches the document and would not match markdown with `**` in the middle of it. Both walk the same tree so neither can silently skip content.
- **Stripping happens once, in `State.LoadInput`.** Markdown and HTML are reduced to prose there, before any command sees the text, defaulting to `--strip auto`. Never strip inside a command and never re-strip: `readability`, `lint`, and `diff` must not be able to disagree about what the document is. Scoring a fenced code block as prose is a real bug, not a rounding error — it moves this repo's own README a full Flesch band.
- **Binary input is refused, not scored.** An input that no decoder claims and that `doc.LooksBinary` calls binary — a NUL byte in the first 8000, or invalid UTF-8 — ends in `doc.UnsupportedErr` (`invalid_args`), and the legacy-Office case is named separately because a 2003 `.doc` looks exactly like a `.docx` to everyone but the parser. The reading ease of a JPEG is not a rounding error; it is a number with no meaning attached to a file the user believed was analysed. The one escape hatch is `--from text`, which skips the sniff *and* the refusal — that is the whole point of it, because a latin-1 export of German prose is invalid UTF-8 and is still real text somebody has a reason to measure.
- **Two size ceilings, and they bound different things.** `--max-bytes` (10 MiB) bounds the text that gets *analysed*; `input.DefaultMaxFileBytes` (100 MiB, not a flag) bounds the bytes read from one *container*. They are separate because a container cannot be truncated: half a zip has no central directory and half a PDF has no xref table, so cutting one at `--max-bytes` turns "your file is too big" into "this file is corrupt". So plain text keeps the streaming read and its early stop, a document is read whole or refused outright, and `--max-bytes` is applied afterwards to the markdown the decoder produced — by `input.CapText`, on a rune boundary, because a cut mid-sequence hands the tokenizer an invalid byte. There is one `CapText`, shared with the fetch path, for the same reason there is one strip. A third bound lives one level down and is not a ceiling on a file at all: `doc.zipMaxText` (64 MiB, `container.go`) bounds what one Decode call may *inflate* out of a zip, because deflate reaches about 1000:1 on the runs of one byte a bomb is built from and a 2 MB `.docx` that passes both ceilings above expands to two gigabytes. Every zip-based decoder reads its parts through `zipRead` against one budget per document — never `io.ReadAll` on an entry — and the budget is charged against the bytes that actually come out, because the declared size is a number an attacker writes. `pdfMinPageBytes` is the same idea for the format that is not a zip: `NumPage` returns `/Root/Pages/Count` verbatim, so the declared page count is checked against the file's own size before the decode loop believes it.
- **Every command goes through `emitResult`.** Never `fmt.Println` to stdout. `emitResult` owns format resolution (`--output`, TTY detection), the envelope, and the TOON/CSV/table/NDJSON/text fallbacks. Fill in what your command can: `Data` is required, the rest degrade. TOON is free — it re-encodes the same `{Data, Meta}` envelope, so it cannot drift from the JSON unless someone bypasses `emitResult`.
- **Byte-offset discipline.** `lint.Finding.Start`/`End` are byte offsets into the analysed text and `Excerpt` is *exactly* `text[Start:End]`. That invariant is the feature: it is what lets an LLM apply a precise edit. Never trim, normalise, or prettify an excerpt relative to the span you report; display truncation belongs to the renderer (`lint.Shorten`, used only in the flat formats). The `token` type carries absolute offsets into `Doc.Text` so a rule never converts between sentence-relative and document offsets — the single most likely place to confuse bytes with runes. Remember German: `ä` is two bytes.
- **No silent contract drift.** The JSON envelope is contractual: `{"data": ..., "meta": {...}}`, errors as one JSON line on stderr. Adding a field is fine; renaming or removing one, or changing what a field *means*, is a breaking change and must be called out in the README, SKILL.md, and the release notes. `entity.Probability` is the worked example: the move from Cloud NL v2 to v1 made it permanently 0 for the Google backend, and the field stayed — with a comment saying why — rather than disappearing from consumers' parsers. The same applies to a number's scale: `--min-salience 0.8` meant something under v2's probability and means "return nothing" under v1's salience.
- **`install.sh`, `install.ps1`, and `internal/update`** additionally depend on the archive naming scheme `text_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) defined in `.goreleaser.yaml`. Change one, change all four.
- **Structured errors everywhere.** Return `*errs.E` (`errs.New`, `errs.Newf`, `.WithHint`, `.WithRetry`), never a bare `error` that reaches the user. The exit code is derived from the code by `errs.ExitCode`; a bare error is exit 1 and a wasted opportunity. Hints should name the exact next command or console page. `invalid_args` means "you typed it wrong"; `provider_unavailable` means "this backend cannot do that" — do not conflate them.
- **Input resolution belongs to `internal/input`.** Commands call `State.LoadInput(args)`. Never read `os.Stdin` directly and never add a second `--file`-shaped flag. A terminal stdin with no args must stay an `empty_input` error, never a hang.
- **Tests are table-driven.** One `tests := []struct{...}` slice, `t.Run(tc.name, ...)`, cases named for the behaviour they pin. Test the `internal/` package, not the cobra command, wherever the logic can be lifted out of `RunE`.
- **Language gates are load-bearing.** A metric's or rule's `Languages` field is what stops German prose being scored with English constants. Set it honestly; use `AnyLanguage` (`"*"`) only for genuinely language-agnostic work.
- **Do not clamp scores**, only band labels — and only where the scale really ends. Reading ease clamps to 0–100 for the *label*; grade levels do not clamp, and a WSTF outside 4–15 is labelled `unter der Skala` / `über der Skala` rather than pretending to be `sehr leicht`.
- **Paid calls are cached on the provider's inputs, never on the filters.** `entities`/`sentiment`/`classify` key on (provider, language, text), so changing `--top`, `--types`, or `--min-salience` re-filters a cached payload instead of paying again. `fetch` keys on (fetcher, URL, main-content, links) — deliberately *not* on `MaxAge`, which bounds staleness in Firecrawl's cache rather than in this one; including it would write two entries for one page. Keep it that way.
- **A 404 from Google means "share it with me".** Google answers a request for a document the caller cannot see with 404 rather than 403, so an id cannot be probed for existence. Reported literally that reads as "not found" for a document the user has open in another tab. `internal/gdocs` turns it into a `not_found` whose hint names the service account address and asks for the document to be shared. Every access failure in that package ends there, and `text docs whoami` prints the same address on demand — a service account has no consent screen and cannot request access.
- **Credentials come from the environment and the config, never from a flag.** There is no `--api-key`: a secret on the command line lands in shell history and in the process list. `config.Secret`/`config.Redact` fingerprint the key in `text config list`, because that output is what people paste into bug reports; `config get` still returns it in full.
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
