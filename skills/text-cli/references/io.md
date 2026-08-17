# I/O contract — input, output, errors, credentials

Everything shared by every command: where the text comes from, what the output looks like, how failures are reported, and which credential each backend needs. Feature-specific shapes live in the other references.

---

## Input

Precedence is fixed and identical for every command:

1. `--url <url>` — repeatable. The page is fetched and then treated exactly like a file. See [web.md](web.md).
2. `--file <path>` — `-` means stdin.
3. **positional arguments**, joined with a space into one document.
4. **piped or redirected stdin.**

A terminal stdin with no arguments is an `empty_input` error (exit 5), never a hang — the CLI does not silently wait for a human to type.

Prefer `--file doc.md` over `cat doc.md | text …`, and `--url X` over `text fetch X | text …`. Both are one process instead of two, and both give the document a meaningful `id`.

### Batch input

| flag | meaning |
|---|---|
| `--input-format text` | default — the whole input is one document |
| `--input-format lines` | every non-empty line is its own document |
| `--input-format jsonl` | one JSON object per line |
| `--text-field <name>` | JSONL field holding the text (default `text`) |
| `--id-field <name>` | JSONL field holding the id (default `id`) |
| `--max-bytes <n>` | input size cap (default 10 MiB) |

Sidecar JSONL fields are carried through into NDJSON output, so results join back to their source. Pair a batch with `--output ndjson`.

**Do not loop the CLI once per document** when the documents are already in a file. Build JSONL and make one call:

```bash
jq -c '{id: .slug, text: .body}' posts.jsonl \
  | text readability --input-format jsonl --output ndjson
```

Each output line is a flat record: `id`, `language`, the token counts, one column per metric named after it (`flesch`, `flesch_level`), plus any passthrough fields. Filter with `jq -c 'select(.flesch < 40)'`.

For a directory, feed the sweep through one process rather than N:

```bash
find content -name '*.md' -print0 \
  | xargs -0 -n1 sh -c 'jq -Rnc --arg id "$0" --rawfile t "$0" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson
```

---

## Stripping is on by default

`--strip auto|markdown|html|none`, default `auto`. Markdown and HTML are reduced to prose **before anything is measured**, so a fenced code block, a table, or a bare URL never counts as a sentence. Plain prose is untouched.

**Do not turn it off to "measure the real document".** Scoring this repo's `docs/EXTENDING.md` raw gives Flesch 48.1 "difficult"; stripped it gives 66.7 "standard" — two whole bands, caused entirely by fenced code blocks being counted as sentences. `--strip none` is only for text you know carries no markup, or when you deliberately want the markup counted.

Stripping happens once, in the shared input loader, so `readability`, `lint`, and `diff` can never disagree about what the document is. It is also why a fetched page and a piped file produce identical numbers and identical byte offsets.

---

## Output

Default is JSON, always one envelope:

```json
{ "data": { ... }, "meta": { "cached": false, "ttl_remaining_sec": null, "api_calls": 0, "language": "en", "language_detected": true, "documents": 1 } }
```

Reading `meta`:

- **`language_detected: true`** — the language was guessed, not given. A surprising score with `detected: true` is often a detection miss; re-run with `--lang en` or `--lang de`.
- **`cached` / `ttl_remaining_sec`** — served from the local cache. `cached` is only `true` when the *whole* batch came from cache. `--refresh` forces a fresh call, `--no-cache` bypasses the cache entirely, `--cache-ttl <duration>` overrides the TTL for one call.
- **`api_calls`** — always `0` for `readability`, `lint`, `diff`, and `metrics`. They are pure computation.
- **`provider`** — names the backend for commands that call one (`google`, `wikipedia`, `firecrawl`).
- **`truncated`** — `--max-bytes` cut the input. Check it before trusting counts on a large file.

### Choosing a format

| you are… | use |
|---|---|
| feeding the output into an LLM prompt (including your own context) | `--output toon` |
| piping into `jq`, or parsing it yourself | `--output json` |
| streaming a batch, one flat record per line | `--output ndjson` |
| producing a spreadsheet | `--output csv` |
| showing a human | `--output text` (or `table`) |

Default is `json`, or `table` when stdout is a terminal. Persist a preference with `text config set defaults.output toon`.

`--output toon` is the same `{data, meta}` envelope encoded as TOON (Token-Oriented Object Notation): JSON's data model without most of its punctuation, with the field names of a uniform array hoisted into one header line. Measured on this CLI's payloads it costs **16–40% fewer tokens** than the same JSON — most on uniform arrays (`diff`, `lint rules`, `research papers`), least when one long string dominates. **Nothing in the usual shell toolchain parses it, so never pipe TOON into `jq`.**

`csv`, `table`, and `text` are lossy by design: they drop nested fields and shorten long strings for display. The machine-facing formats — `json`, `toon`, `ndjson` — are exact.

---

## Errors and exit codes

Errors are a single JSON line on **stderr**, in every output format:

```json
{"error":{"code":"unknown_metric","message":"unknown metric: \"foo\"","hint":"Known metrics: amstad, ari, coleman-liau, flesch, flesch-kincaid, gunning-fog, lix, smog, wstf1, wstf2, wstf3, wstf4. Run `text metrics list` for details.","retriable":false}}
```

**Read `.error.hint`** — it names the command or console page that fixes the problem. Branch on `.error.code`, never on the message text.

| exit | class | codes | what to do |
|---|---|---|---|
| 0 | ok | — | — |
| 1 | generic | `generic` | read the message. A failed `--fail-under` / `--fail-over` / `--fail-on-findings` gate lands here, **and the full result was still printed to stdout** |
| 2 | auth | `auth_missing`, `auth_expired`, `auth_denied` | a credential problem — see below. **Do not retry**; tell the user which one is missing |
| 3 | quota / rate | `quota_exceeded`, `rate_limited` | back off; check `retry_after_sec`. `quota_exceeded` means out of credits and will not fix itself |
| 4 | not found | `not_found` | wrong `--file` path, or a `kb`/`research` lookup with no match |
| 5 | bad input | `invalid_args`, `empty_input`, `unsupported_language`, `unknown_metric` | read the hint; run `text metrics list` or `text lint rules` before retrying |
| 6 | network / provider | `network_unreachable`, `api_5xx`, `provider_unavailable` | retriable; retry once. `provider_unavailable` also means "this backend cannot do that" — the hint names the ones that can |

`auth_missing` ("you have not set a credential") and `auth_denied` ("the one you set is wrong") are different fixes. `invalid_args` means you typed it wrong; `provider_unavailable` means the backend cannot do it. Do not conflate them.

---

## Credentials

| for | precedence, highest first |
|---|---|
| `entities`, `sentiment`, `classify` | `--service-account`, `TEXT_SERVICE_ACCOUNT`, `GOOGLE_APPLICATION_CREDENTIALS`, config `entities.service_account_path`, then Application Default Credentials |
| `fetch`, `--url` | `FIRECRAWL_API_KEY`, then config `firecrawl.api_key` |

`readability`, `lint`, `diff`, `metrics`, `kb`, and `research` need **no credentials at all**.

- The Google path additionally requires the Cloud Natural Language API to be enabled on the project. A configured path that does not exist is `auth_missing` rather than a silent fallback to ADC, so a typo surfaces immediately.
- **There is deliberately no `--api-key` flag for Firecrawl.** A secret on the command line lands in shell history and the process list. `text config list` fingerprints the key (`…017f`); `text config get firecrawl.api_key` returns it in full.
- `text research` uses the Firecrawl account but does **not** require a key — the index answers unauthenticated requests. A key only raises the rate limit.
