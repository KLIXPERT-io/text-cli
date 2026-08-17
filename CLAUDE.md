# text-cli — repo guide

Go CLI for text analysis. Module `github.com/KLIXPERT-io/text-cli`, binary `text`, main package `./cmd/text`. Released as a single static binary via GoReleaser.

## Layout

```
cmd/text/                    main; version injected via -ldflags
internal/cmd/                cobra command tree, one file per command
internal/analyze/            metric registry (registry.go)
internal/analyze/readability/  Flesch (en), Amstad (de) — one file per metric
internal/entity/             provider-neutral entity types + registry + Google backend
internal/textproc/           tokenizer: sentences, words, syllables, per language
internal/input/              stdin / file / args resolution, text|lines|jsonl
internal/output/             JSON / NDJSON / CSV / table / text rendering, the Meta envelope
internal/config/             ~/.config/text-cli/config.toml
internal/errs/               structured errors and exit codes
internal/cache/              disk cache with TTLs
internal/logging/            stderr logging
internal/update/             self-updater
docs/EXTENDING.md            the three extension recipes — read before adding anything
skills/text-cli/SKILL.md     agent skill shipped with the CLI
```

## The one rule: register, don't wire

New capabilities declare themselves; nothing enumerates them.

- **A metric** is one file in `internal/analyze/readability/` (or a new package under `internal/analyze/`) with an `analyze.Register(analyze.Metric{...})` call in `init()`. It then appears in `text metrics list`, in `--metrics all`, and in `--metrics auto` for its declared languages. Do not add a switch statement, a name list, or a command flag for it.
- **An entity/knowledge backend** is one file in `internal/entity/` with `entity.Register(name, factory)` in `init()`. The factory must stay cheap — no clients, no credentials, no network; `Open` calls it lazily. This is how the planned Wikipedia backend lands.
- **A command** is one file in `internal/cmd/` exporting `newXCmd() *cobra.Command`, plus one line in the `root.AddCommand(...)` list in `Execute` (`internal/cmd/root.go`). That is the only wiring point in the repo.

If you find yourself editing more than one file to add a feature of these kinds, you are working against the design. See `docs/EXTENDING.md`.

## House conventions

- **Structured errors everywhere.** Return `*errs.E` (`errs.New`, `errs.Newf`, `.WithHint`, `.WithRetry`), never a bare `error` that reaches the user. The exit code is derived from the code by `errs.ExitCode`; a bare error is exit 1 and a wasted opportunity. Hints should name the exact next command or console page.
- **Every command goes through `emitResult`.** Never `fmt.Println` to stdout. `emitResult` owns format resolution (`--output`, TTY detection), the envelope, and the CSV/table/NDJSON/text fallbacks. Fill in what your command can: `Data` is required, the rest degrade.
- **The JSON envelope is contractual.** `{"data": ..., "meta": {...}}`, errors as one JSON line on stderr. Adding a field is fine; renaming or removing one is a breaking change, and `install.sh`, `install.ps1`, and `internal/update` additionally depend on the archive naming scheme `text_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) defined in `.goreleaser.yaml`. Change one, change all four.
- **Input resolution belongs to `internal/input`.** Commands call `State.LoadInput(args)`. Never read `os.Stdin` directly and never add a second `--file`-shaped flag. A terminal stdin with no args must stay an `empty_input` error, never a hang.
- **Tests are table-driven.** One `tests := []struct{...}` slice, `t.Run(tc.name, ...)`, cases named for the behaviour they pin. Test the `internal/` package, not the cobra command, wherever the logic can be lifted out of `RunE`.
- **Language gates are load-bearing.** A metric's `Languages` field is what stops German prose being scored with English constants. Set it honestly; use `analyze.AnyLanguage` only for genuinely language-agnostic measurements.
- **Do not clamp scores**, only band labels. A negative reading-ease score is information.
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
