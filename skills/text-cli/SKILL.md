---
name: text-cli
description: Analyse text from the command line with the `text` CLI — readability scoring (Flesch for English, Amstad for German), token statistics, and named-entity extraction with Wikipedia/knowledge-base identifiers. Use whenever the task involves how readable, hard, or complex a piece of writing is, comparing drafts, checking prose against a reading level, counting words/sentences/syllables, or pulling people, places, organizations, and products out of text. Triggers include "how readable is this", "reading level", "Flesch score", "Lesbarkeit", "simplify this text", "is this too hard to read", "extract entities", "who and what is mentioned", "named entities", "word count of this article".
---

# text — text analysis CLI

`text` reads prose from stdin, a file, or an argument and prints structured data. It never writes anything but its output.

## Input

Precedence, fixed for every command:

1. `--file <path>` (`-` means stdin)
2. positional arguments, joined into one document
3. piped or redirected stdin

A terminal stdin with no arguments is an `empty_input` error, not a hang. Prefer `--file` over `cat file | text …`.

Batch input: `--input-format lines` (one document per line) or `--input-format jsonl` (one JSON object per line; `--text-field`, default `text`, and `--id-field`, default `id`). Sidecar JSONL fields are carried through into NDJSON output, so results join back to the source. Pair batches with `--output ndjson`.

## Output contract

Default is JSON, one envelope:

```json
{ "data": { ... }, "meta": { "cached": false, "ttl_remaining_sec": null, "api_calls": 0, "language": "en", "language_detected": true, "documents": 1 } }
```

- `meta.language_detected: true` means the language was guessed, not given. A surprising score with `detected: true` is often a detection miss — re-run with `--lang en` or `--lang de`.
- `meta.cached` / `meta.ttl_remaining_sec` — served from the local cache. `--refresh` forces a fresh call, `--no-cache` bypasses the cache entirely.
- `meta.api_calls` is `0` for readability, always. Readability is pure computation and costs nothing.
- `--output ndjson` for batches (one flat record per line, streamed), `--output csv` for a spreadsheet, `--output text` for a human, `--output table` for a terminal.

Errors are a single JSON line on **stderr**, in every output format:

```json
{"error":{"code":"unknown_metric","message":"unknown metric: \"foo\"","hint":"Known metrics: amstad, flesch. Run `text metrics list` for details.","retriable":false}}
```

Read `.error.hint` — it names the command that fixes the problem.

| exit | class | codes | what to do |
|---|---|---|---|
| 0 | ok | — | — |
| 1 | generic | `generic` | read the message |
| 2 | auth | `auth_missing`, `auth_expired`, `auth_denied` | credentials for `text entities`; see Entities below |
| 3 | quota / rate | `quota_exceeded`, `rate_limited` | back off; check `retry_after_sec` |
| 4 | not found | `not_found` | wrong `--file` path |
| 5 | bad input | `invalid_args`, `empty_input`, `unsupported_language`, `unknown_metric` | read the hint; `text metrics list` |
| 6 | network / provider | `network_unreachable`, `api_5xx`, `provider_unavailable` | retriable; retry once |

## Readability

```bash
text readability [text...]        # aliases: read, rd, flesch, amstad
```

`data` for a single document is one object: `id`, `language`, `language_detected`, `stats`, `metrics[]`. A batch is a list of those under `data.documents`. Each entry of `metrics[]` has `metric`, `title`, `score`, `level`, `grade`, `scale`, `language`, and `extra` (the ASL/ASW/word/sentence/syllable counts that produced the score).

Flags:

- `--metrics auto|all|<names>` — default `auto`: every metric valid for the document's language. Names are comma-separated and accept aliases. `auto` and `all` currently agree.
- `--stats` — token counts, **on by default**. Pass `--stats=false` to drop them.
- `--lang auto|en|de` — root flag; forces the analysis language instead of detecting it.

### Pick the metric by language

- **English → `flesch`** (Flesch Reading Ease, `206.835 − 1.015×ASL − 84.6×ASW`). Aliases `fre`, `flesch-reading-ease`.
- **German → `amstad`** (Amstad 1978, `180 − ASL − 58.5×ASW`). Aliases `flesch-de`, `flesch-amstad`, `fre-de`. This is *the German equivalent of Flesch*, not a different kind of measurement.

**Flesch is English-only and Amstad is German-only, and the registry enforces it.** Asking for `--metrics flesch` on German text is an `unsupported_language` error (exit 5), by design: the English constants punish German compounds so hard that almost any German prose scores below zero, which is not information. Never report a Flesch number for German text. Use `--metrics auto` and let the language decide.

Both use the same 0–100 scale, higher is easier, so `level` labels are comparable across the two. German labels are the domain vocabulary and are returned in German: `sehr leicht`, `leicht`, `mittelleicht`, `mittel`, `mittelschwer`, `schwer`, `sehr schwer`. English: `very easy`, `easy`, `fairly easy`, `standard`, `fairly difficult`, `difficult`, `very confusing`.

Scores are not clamped. A negative score is real and means the text is punishing.

### Discover what is available

```bash
text metrics             # same as `text metrics list`
text metrics list
text metrics show <name> # resolves aliases
```

Read the registry rather than assuming: metrics are added over time, and `metrics list` reports each one's `languages` and `description`.

## Entities

```bash
text entities [text...]   # alias: ents
```

Calls the Google Cloud Natural Language API v2 `AnalyzeEntities`. This costs money and network — it is the only command that does.

Flags: `--provider <name>`, `--service-account <key.json>`, `--types <CSV>`, `--min-probability <0..1>`, `--top <n>`, `--timeout <duration>`.

A single document renders as one object: `id`, `provider`, `language`, `language_supported`, `entities[]`. A batch is a list of those under `data.documents`.

Each entity carries `name`, `type`, `probability`, `mention_count`, `mentions[]`, `metadata`, and — where the provider knows them — `wikipedia_url` and `mid`. `language_supported: false` with entities present means best-effort output, so caveat the result.

Types: `PERSON`, `LOCATION`, `ORGANIZATION`, `EVENT`, `WORK_OF_ART`, `CONSUMER_GOOD`, `NUMBER`, `DATE`, `ADDRESS`, `PHONE_NUMBER`, `PRICE`, `OTHER`, `UNKNOWN`. `--types person,organization` is case-insensitive.

`probability` is confidence that the thing is an entity of that type. **It is not salience.** The v2 API has no salience field, so it says nothing about how central an entity is to the document. Do not describe a high-probability entity as the document's "main topic".

Filtering with `--types`, `--min-probability`, and `--top` happens after the cache, so tightening them never costs another API call.

Credentials, highest precedence first: `--service-account`, `TEXT_SERVICE_ACCOUNT`, `GOOGLE_APPLICATION_CREDENTIALS`, config `entities.service_account_path`, then Application Default Credentials. On exit 2, read the hint — it names the console page. The Cloud Natural Language API must be enabled on the project.

`--output ndjson` emits one entity per line, tagged with `doc_id` and `provider`, plus any passthrough JSONL fields. `--output csv` gives `doc_id,name,type,probability,mentions,wikipedia_url`.

Results are cached per (provider, language, text), so re-running the same document costs nothing. `--refresh` forces a fresh call. `meta.cached` is only `true` when the *whole* batch came from cache.

## Batching

Do not loop the CLI once per document when the documents are already in a file. Build JSONL and make one call:

```bash
jq -c '{id: .slug, text: .body}' posts.jsonl \
  | text readability --input-format jsonl --output ndjson
```

Each output line is a flat record: `id`, `language`, the token counts, and one column per metric named after it (`flesch`, `flesch_level`), plus any sidecar fields from the input. Filter with `jq -c 'select(.flesch < 40)'`.

For a directory, feed the sweep through one process rather than N:

```bash
find content -name '*.md' -print0 \
  | xargs -0 -n1 sh -c 'jq -Rn --arg id "$0" --rawfile t "$0" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson
```

`--output csv` when a human will open the result; `--output ndjson` when a program will.

## Rules of thumb

- Use `--metrics auto` unless the caller named a formula. It picks the right one per document, even in a mixed-language batch.
- Set `--lang` explicitly when you know the language; detection is a heuristic and `meta.language_detected` tells you when it was used.
- Report the `level` label alongside the number — the raw score means little on its own.
- Never quote a Flesch score for German text, or an Amstad score for English.
- Check `meta.truncated` before trusting counts on a large file; it means `--max-bytes` cut the input.
- Reach for `text entities` only when the task actually needs named things. Readability questions never need it, and it is the only command that spends money.
- On exit 5, run `text metrics list` before retrying; on exit 2, stop and tell the user which credential is missing rather than retrying.
- Large batches: write NDJSON to a file and query it with `jq`, do not read every line into context.
