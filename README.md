# text — text analysis CLI

[![Latest release](https://img.shields.io/github/v/release/KLIXPERT-io/text-cli?sort=semver)](https://github.com/KLIXPERT-io/text-cli/releases/latest)

A fast, LLM-friendly Go CLI for text analysis. It reads prose from stdin, a file, or an argument, measures it, and writes JSON — so it drops into a shell pipeline, a Makefile, or an agent's tool loop without ceremony.

- Single static binary, no runtime, no daemon.
- Pipes in and out: `cat post.md | text readability | jq .data`.
- JSON by default, `ndjson` for streams, `csv` for spreadsheets, `table` on a TTY, `text` for humans.
- Structured errors with machine-readable codes and distinct exit codes — never a stack trace, never a hang.
- An extensible metric registry: a new measurement is one file, and it shows up in `text metrics list` on its own.
- Named entities via the Google Cloud Natural Language API, behind a provider interface built for more backends.
- Local disk cache with TTLs, so re-running a paid analysis over unchanged text costs nothing.

## Install

macOS / Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.sh | sh
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/KLIXPERT-io/text-cli/refs/heads/main/install.ps1 | iex
```

Or `go install github.com/KLIXPERT-io/text-cli/cmd/text@latest`. See [INSTALL.md](./INSTALL.md) for manual downloads, version pinning, and checksum verification.

After install, `text` keeps itself up to date in the background — see [INSTALL.md](./INSTALL.md#auto-update) for details and how to opt out.

## Use with local LLM agents (Claude, Gemini, …)

`text` ships an agent skill that teaches LLM coding agents how to drive the CLI (commands, flags, the JSON envelope, exit codes, which metric fits which language). Install it into any tool that supports the [`skills`](https://github.com/anthropics/skills) format:

```bash
npx skills add https://github.com/KLIXPERT-io/text-cli/skills --skill text-cli
```

## Setup

Readability needs no setup at all — it is pure computation on the text you hand it:

```bash
echo "The quick brown fox jumps over the lazy dog." | text readability
```

### Google credentials (only for `text entities`)

`text entities` calls the [Google Cloud Natural Language API v2](https://cloud.google.com/natural-language/docs/analyzing-entities) (`AnalyzeEntities`). Everything else runs offline.

1. **Enable the API** in a Google Cloud project: [Cloud Natural Language API](https://console.cloud.google.com/apis/library/language.googleapis.com) → **Enable**.
2. **Create a service account and a JSON key:**

   ```bash
   gcloud iam service-accounts create text-entities \
     --display-name "text CLI entities" --project YOUR_PROJECT
   gcloud iam service-accounts keys create ~/secrets/text-sa.json \
     --iam-account text-entities@YOUR_PROJECT.iam.gserviceaccount.com
   ```

3. **Point `text` at the key.** Any one of these works — highest precedence first:

   ```bash
   text entities --service-account ~/secrets/text-sa.json "Ada Lovelace worked with Charles Babbage."
   export TEXT_SERVICE_ACCOUNT=~/secrets/text-sa.json
   export GOOGLE_APPLICATION_CREDENTIALS=~/secrets/text-sa.json
   text config set entities.service_account_path ~/secrets/text-sa.json
   ```

   Flag, then environment, then config — so exporting `TEXT_SERVICE_ACCOUNT` in CI overrides a stored config path without editing the file.

   With none of them set, the provider falls back to [Application Default Credentials](https://cloud.google.com/docs/authentication/application-default-credentials) — which is what you want on GCE, Cloud Run, or after `gcloud auth application-default login`.

   A path that is configured but does not exist is an `auth_missing` error rather than a silent fallback to ADC, so a typo surfaces immediately instead of as a confusing permission error later.

Not every language is supported by the API to the same depth; check the [language support table](https://cloud.google.com/natural-language/docs/languages) before trusting entity output in a long-tail language. The `language_supported` field in the response says what the provider thinks.

## Usage

| command | what it does |
|---|---|
| `readability` (`read`, `rd`, `flesch`, `amstad`) | reading-ease scores and token statistics |
| `metrics list\|show` | discover every registered metric and the languages it covers |
| `entities` (`ents`) | named entities, with Wikipedia / knowledge-base identifiers |
| `config get\|set\|list\|path` | configuration |
| `update status\|check\|apply` | self-update |

```bash
# Readability — English Flesch, German Amstad, chosen by language
text readability "The quick brown fox jumps over the lazy dog."
cat post.md | text readability --lang de
text readability --file post.md --metrics flesch
text readability --file post.md --stats=false     # drop the token counts
text flesch --file post.md                        # alias: one metric, no --metrics needed

# What can it measure?
text metrics list
text metrics show flesch

# Named entities
text entities --file post.md
text entities --file post.md --types PERSON,ORGANIZATION --min-probability 0.5 --top 20

# Config and self-update
text config path
text config list
text update status
```

Input precedence is fixed and identical for every command:

1. `--file <path>` (or `--file -` for stdin)
2. positional arguments, joined into one document
3. stdin, when it is a pipe or a redirect

A terminal stdin with no arguments is an `empty_input` error, not a hang. The CLI never silently waits for a human to type.

### Shared flags

| flag | meaning |
|---|---|
| `--output json\|ndjson\|csv\|table\|text` | default `json`, or `table` when stdout is a terminal |
| `--lang auto\|en\|de` | analysis language; `auto` detects it |
| `-f, --file <path>` | read the text from a file (`-` for stdin) |
| `--input-format text\|lines\|jsonl` | one document, one per line, or one JSON object per line |
| `--text-field <name>` | JSONL field holding the text (default `text`) |
| `--id-field <name>` | JSONL field holding the document id (default `id`) |
| `--max-bytes <n>` | input size cap (default 10 MiB) |
| `--no-cache` | bypass cache reads and writes |
| `--refresh` | bypass the cache read, still write a fresh result |
| `--cache-ttl <duration>` | override the TTL for this call |
| `-v, --verbose` / `-q, --quiet` | logging on stderr |
| `--log-format text\|json` | log format |

## Pipelines

`text` is built to sit in the middle of a pipe. Real, copy-pasteable examples:

```bash
# Score a German blog post you just wrote
cat post.md | text readability --lang de
```

```bash
# Batch over a JSONL corpus, one result object per line, streamed
jq -c '{id: .slug, text: .body}' posts.jsonl \
  | text readability --input-format jsonl --output ndjson \
  > scores.ndjson

# Then find the hard ones. In NDJSON each line is a flat record: the id, the
# language, the token counts, and one column per metric named after it.
jq -c 'select(.flesch < 40)' scores.ndjson
```

```bash
# Straight into a spreadsheet
text readability --file report.md --metrics all --output csv > readability.csv
```

```bash
# People and organisations mentioned in a post, one per line
text entities --file post.md --types PERSON,ORGANIZATION \
  | jq -r '.data.entities[] | [.name, .type, .probability] | @tsv'

# Only entities that carry a Wikipedia link — the seed for a knowledge lookup
text entities --file post.md \
  | jq -r '.data.entities[] | select(.wikipedia_url != null) | "\(.name)\t\(.wikipedia_url)"'
```

```bash
# Sweep a content directory: one JSON line per file, tagged with its path
find content -name '*.md' -print0 \
  | xargs -0 -n1 sh -c 'text readability --file "$0" \
      | jq -c --arg file "$0" "{file: \$file, score: .data.metrics[0].score, level: .data.metrics[0].level}"' \
  > sweep.ndjson

# The twenty hardest pages
jq -s -c "sort_by(.score)[]" sweep.ndjson | head -20
```

```bash
# Same sweep, machine-readable: one JSON line per file
find content -name '*.md' -print0 \
  | xargs -0 -n1 -I{} sh -c 'jq -Rn --arg id "{}" --rawfile t "{}" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson
```

Because errors go to stderr as JSON and the exit code carries the class of failure, a pipeline can branch without parsing prose:

```bash
if ! out=$(text entities --file post.md 2>err.json); then
  code=$(jq -r '.error.code' err.json)
  echo "entities failed: $code" >&2
fi
```

## Output contract

Every JSON response has the same envelope:

```json
{
  "data": {
    "id": "0",
    "language": "en",
    "language_detected": true,
    "stats": {
      "sentences": 1,
      "words": 9,
      "syllables": 11,
      "characters": 35,
      "polysyllabic_words": 0,
      "monosyllabic_words": 7,
      "long_words": 0,
      "avg_sentence_length": 9,
      "avg_syllables_per_word": 1.2222222222222223,
      "avg_word_length": 3.888888888888889
    },
    "metrics": [
      {
        "metric": "flesch",
        "title": "Flesch Reading Ease",
        "score": 94.3,
        "level": "very easy",
        "grade": "5th grade",
        "scale": "0–100, higher is easier",
        "language": "en",
        "extra": { "asl": 9, "asw": 1.22, "words": 9, "sentences": 1, "syllables": 11 }
      }
    ]
  },
  "meta": {
    "cached": false,
    "ttl_remaining_sec": null,
    "api_calls": 0,
    "language": "en",
    "language_detected": true,
    "documents": 1
  }
}
```

A single document is emitted as that object directly; a batch is a list of the same objects under `data.documents`. Either way a consumer parses one shape.

`meta` fields:

| field | meaning |
|---|---|
| `cached` | the answer came from the local cache |
| `cached_at` | RFC 3339 timestamp of the cached entry (omitted when not cached) |
| `ttl_remaining_sec` | seconds of TTL left, `null` when not cached |
| `api_calls` | remote calls this invocation made — `0` for readability, always |
| `language` | the language the analysis ran in |
| `language_detected` | `true` when the language was detected rather than given with `--lang` |
| `provider` | the backend name, for commands that call one (e.g. `google`) |
| `documents` | number of input documents processed |
| `truncated` | set when input hit `--max-bytes` |

`--output ndjson` drops the envelope and emits one result object per line, which is the right shape for batches: each line is complete on its own, so a consumer can start work before the run finishes.

Errors are a single JSON line on **stderr**, in every output format:

```json
{"error":{"code":"empty_input","message":"input contained no text","hint":"Check the file or the upstream command in your pipeline.","retriable":false}}
```

## Exit codes

| exit | class | codes |
|---|---|---|
| 0 | success | — |
| 1 | generic | `generic` |
| 2 | auth | `auth_missing`, `auth_expired`, `auth_denied` |
| 3 | quota / rate limit | `quota_exceeded`, `rate_limited` |
| 4 | not found | `not_found` |
| 5 | bad input | `invalid_args`, `empty_input`, `unsupported_language`, `unknown_metric` |
| 6 | network / provider | `network_unreachable`, `api_5xx`, `provider_unavailable` |

## Config

`~/.config/text-cli/config.toml` on Linux, `~/Library/Application Support/text-cli/config.toml` on macOS. `text config path` prints the exact location.

```toml
auto_update = true

[defaults]
output = "json"     # json|ndjson|csv|table|text; empty means auto (table on a TTY)
lang = "auto"       # auto|en|de
metrics = "auto"    # auto|all|<comma-separated names>

[entities]
provider = "google"
service_account_path = "~/secrets/text-sa.json"
# language = "de"   # force the document language sent to the provider

[cache]
# dir = "~/custom/cache"   # default: <config dir>/cache
default_ttl = "24h"
ttl_entities = "24h"

[logging]
verbose = false
format = "text"
```

Manage it with `text config get|set|list|path`. The directory is `text-cli`, not `text`, on purpose — `text` is too generic a name for a shared `~/.config` namespace.

## Roadmap / extending

The registry is the point. Metrics register themselves at init time, so a new formula is one file in `internal/analyze/readability/` with a `Register` call — no command wiring, no switch statement — and it appears in `text metrics list` and in `--metrics all` immediately. Entity backends work the same way behind the `entity.Provider` interface.

What that makes cheap, and what is coming:

- **More metrics.** Further readability formulas, and measurements beyond readability.
- **Knowledge-database lookups.** `text entities` already carries `wikipedia_url` and `mid` through from the provider; a Wikipedia backend registered alongside the Google one is the planned next step, and it lands as an additional provider rather than a rewrite.

See [docs/EXTENDING.md](./docs/EXTENDING.md) for the three extension recipes, with the real signatures from this repo.

## Non-goals

- No writes anywhere — `text` reads text and prints numbers.
- No web UI, no daemon mode.
- No visualisation — pipe CSV into your plotting tool of choice.

## License

MIT
