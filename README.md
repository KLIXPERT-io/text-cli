# text — text analysis CLI

[![Latest release](https://img.shields.io/github/v/release/KLIXPERT-io/text-cli?sort=semver)](https://github.com/KLIXPERT-io/text-cli/releases/latest)

A fast, LLM-friendly Go CLI for text analysis. It reads prose from stdin, a file, or an argument, measures it, tells you what to fix, and writes JSON — so it drops into a shell pipeline, a Makefile, or an agent's tool loop without ceremony.

- **Markup is reduced to prose before anything is measured** (`--strip auto`, on by default). A fenced code block or an HTML table never counts as a sentence. This is the difference between a right number and a wrong one on a real document — see [Stripping](#stripping-is-on-by-default).
- **`text lint` says what to change, not just how bad it is.** Sentence- and phrase-level findings with exact byte offsets, so an editor, a script, or an LLM can apply the edit without searching for the span again. German Passiv, Substantivstil, Behördendeutsch, Füllwörter; English passive, filler, hedging, nominalization.
- **Twelve metrics** — English, German, and one language-agnostic — each with its score direction declared: Flesch and Amstad are 0–100 reading-ease scores where higher is easier; WSTF 1–4, LIX, Flesch-Kincaid, Gunning Fog, SMOG, Coleman-Liau and ARI are grade levels where **lower** is easier.
- **`--output toon`** emits the same `{data, meta}` envelope in TOON (Token-Oriented Object Notation) — fewer tokens when the output is going into an LLM prompt. JSON stays the right choice for `jq`.
- Also `ndjson` for streams, `csv` for spreadsheets, `table` on a TTY, `text` for humans.
- Structured errors with machine-readable codes and distinct exit codes — never a stack trace, never a hang. `--fail-under` / `--fail-over` / `--fail-on-findings` turn any of it into a CI gate.
- Named entities, sentiment, and content classification via the Google Cloud Natural Language API, behind a provider interface built for more backends. Wikipedia lookups via `text kb`, with no credentials at all.
- Four registries — metrics, entity providers, lint rules, knowledge sources — so a new measurement, rule, or backend is one file that wires itself.
- Local disk cache with TTLs, so re-running a paid analysis over unchanged text costs nothing.
- Single static binary, no runtime, no daemon.

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

`text` ships an agent skill that teaches LLM coding agents how to drive the CLI (commands, flags, the JSON envelope, exit codes, which metric fits which language, when to ask for TOON instead of JSON). Install it into any tool that supports the [`skills`](https://github.com/anthropics/skills) format:

```bash
npx skills add https://github.com/KLIXPERT-io/text-cli/skills --skill text-cli
```

## Setup

Readability, lint, diff, and metric discovery need no setup at all — they are pure computation on the text you hand them:

```bash
echo "The quick brown fox jumps over the lazy dog." | text readability
```

`text kb` needs the network but no credentials: it reads the public Wikipedia API.

### Google credentials (only for `entities`, `sentiment`, `classify`)

Those three call the [Google Cloud Natural Language API v1](https://cloud.google.com/natural-language/docs/analyzing-entities) — `AnalyzeEntities`, `AnalyzeSentiment`, `ClassifyText`. They are the only commands that need a credential, and the only ones that cost money. `text kb` needs the network but no key; everything else runs entirely offline.

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

Check the [language support table](https://cloud.google.com/natural-language/docs/languages) before trusting output in a long-tail language. The v1 API rejects a language it does not support with an error rather than answering best-effort, so for the Google backend `language_supported` in the response is always `true`; the field stays in the contract for providers that report it.

## Usage

| command | what it does |
|---|---|
| `readability` (`read`, `rd`, `flesch`, `amstad`) | reading-ease and grade-level scores, plus token statistics |
| `lint` (`lint rules`) | sentence- and phrase-level findings with byte offsets — the work list |
| `diff <before> <after>` | per-metric delta between two drafts, with an `improved` flag |
| `metrics list\|show` | discover every registered metric and the languages it covers |
| `entities` (`ents`) | named entities with salience and knowledge-base identifiers |
| `sentiment` (`sent`) | polarity, magnitude, and a label, optionally per sentence |
| `classify` (`cls`) | content categories as taxonomy paths |
| `kb lookup\|search` (`knowledge`) | Wikipedia descriptions and lead paragraphs |
| `config get\|set\|list\|path` | configuration |
| `update status\|check\|apply` | self-update |

```bash
# Readability — English Flesch, German Amstad, chosen by language
text readability "The quick brown fox jumps over the lazy dog."
cat post.md | text readability --lang de
text readability --file post.md --metrics flesch
text readability --file post.md --stats=false     # drop the token counts
text flesch --file post.md                        # alias: one metric, no --metrics needed

# What to fix, and exactly where
text lint --file post.de.md --output text
text lint --file post.de.md --rules passive,bureaucratic --severity warn
text lint rules                                   # every registered rule

# Did the rewrite help?
text diff draft1.md draft2.md

# What can it measure?
text metrics list
text metrics show flesch

# Named entities, sentiment, categories (Google credentials required)
text entities --file post.md
text entities --file post.md --types PERSON,ORGANIZATION --min-salience 0.02 --top 20
text sentiment --file review.md
text classify --file post.md --top 3

# Wikipedia (no credentials)
text kb lookup "Ada Lovelace"
text kb search "analytical engine"

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
| `--output json\|toon\|ndjson\|csv\|table\|text` | default `json`, or `table` when stdout is a terminal |
| `--strip auto\|markdown\|html\|none` | reduce markup to prose before analysis; default `auto` |
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

## Stripping is on by default

`--strip auto` sniffs the input and reduces Markdown or HTML to prose before a single word is counted. Plain prose is passed through untouched, so the flag costs nothing on text that has no markup.

It is not a nicety. Scoring this repository's own `docs/EXTENDING.md` raw against stripped:

```
$ text readability --file docs/EXTENDING.md --metrics flesch --strip none --output text
language: en (detected)
words 4539  sentences 280  syllables 7632  asl 16.21  asw 1.68
flesch  48.1  difficult (college)

$ text readability --file docs/EXTENDING.md --metrics flesch --output text
language: en (detected)
words 2008  sentences 152  syllables 3008  asl 13.21  asw 1.5
flesch  66.7  standard (8th–9th grade)
```

Eighteen points and two bands, from "difficult" to "standard", produced entirely by fenced Go blocks being counted as prose — note the word count falling from 4539 to 2008. Any tool that scores Markdown without stripping it reports a confidently incorrect number.

`--strip markdown` and `--strip html` force one of the two; `--strip none` opts out. Stripping happens once, in the shared input loader, so every command sees the same prose — `readability`, `lint`, and `diff` cannot disagree about what the document is.

## Metrics

`text metrics list` is the authority; this is what it prints today.

| name | title | languages | direction |
|---|---|---|---|
| `flesch` | Flesch Reading Ease | en | 0–100, **higher is easier** |
| `amstad` | Amstad (Flesch für Deutsch) | de | 0–100, **higher is easier** |
| `flesch-kincaid` | Flesch-Kincaid Grade Level | en | US grade, **lower is easier** |
| `gunning-fog` | Gunning Fog Index | en | US grade, **lower is easier** |
| `smog` | SMOG Index | en | US grade, **lower is easier** |
| `coleman-liau` | Coleman-Liau Index | en | US grade, **lower is easier** |
| `ari` | Automated Readability Index | en | US grade, **lower is easier** |
| `wstf1` … `wstf4` | Wiener Sachtextformel 1–4 | de | Schulstufe 4–15, **lower is easier** |
| `lix` | LIX (Läsbarhetsindex) | any | 20–60+, **lower is easier** |

Two families, two directions. Reading ease runs up; every grade level runs down. Read a Flesch of 30 as hard and a Flesch-Kincaid of 30 as hard too — but a *rising* Flesch is an improvement and a rising Flesch-Kincaid is not. `text diff` and the CI gates derive the direction from the registry rather than from the sign of the delta, so they cannot get this backwards; a dashboard that hard-codes "bigger is better" can.

The WSTF metrics report the raw formula output and label anything outside the 4–15 Schulstufe range `unter der Skala` or `über der Skala`, rather than calling a computed 0.0 "sehr leicht":

```
$ text readability --file post.de.md --metrics wstf1 --output text
wstf1   16.8  über der Skala (über der 12. Schulstufe)
```

`--metrics auto` (the default) resolves per document, so a mixed-language JSONL batch is scored row by row with the formulas calibrated for each row's language. Asking for a metric outside its language — `--metrics flesch` on German — is an `unsupported_language` error, by design.

## Lint: from a score to a work list

`text readability` says a document is hard. `text lint` says which sentence, which phrase, and what to write instead. Every finding carries `start` and `end` as **byte offsets into the analysed text**, and `excerpt` is exactly `text[start:end]` — never a prettified version of it — so a consumer can slice the document and apply the edit without searching for the span again.

```
$ text lint "Die Bearbeitung des Antrags wurde seitens der zuständigen Stelle im Rahmen der geltenden Vorschriften durchgeführt." --output text
bureaucratic (2)
  1: seitens — Behördendeutsch: „seitens“.
  1: im Rahmen der — Behördendeutsch: „im Rahmen der“.
hard-sentence (1)
  1: Die Bearbeitung des Antrags wurde seitens der zuständigen S… — Schwer lesbarer Satz: Lesbarkeit 40.2 von 100 (Amstad), 15 Wörter.
passive (1)
  1: wurde seitens der zuständigen Stelle im Rahmen der geltende… — Passiv: „wurde … durchgeführt“ — der Handelnde fehlt.

4 finding(s)
```

The same run as JSON, which is the form to hand to a program:

```json
{
  "rule": "passive",
  "severity": "warn",
  "message": "Passiv: „wurde … durchgeführt“ — der Handelnde fehlt.",
  "suggestion": "Aktiv formulieren und das Subjekt nennen: wer tut es?",
  "sentence": 1,
  "start": 28,
  "end": 116,
  "excerpt": "wurde seitens der zuständigen Stelle im Rahmen der geltenden Vorschriften durchgeführt"
}
```

`suggestion` is the fix in the document's own language. The rules cover long and hard sentences, repeated words and repeated sentence openings, sentence-length variance, over-long words; German Passiv, Substantivstil, Behördendeutsch, Füllwörter, Konjunktiv-Häufung; English passive voice, filler, hedging, nominalization, and adverb density. `--rules auto` (the default) runs everything registered for the document's language.

Run `text lint rules` for the current list with severities. Tune with `--max-sentence-words` (default 25), `--max-word-chars` (default 20), `--worst` (default 5), and `--severity info|warn`.

`text[start:end] == excerpt` holds exactly in the machine-facing formats — JSON, TOON, and NDJSON. Only the human-facing `csv`, `table`, and `text` shorten `excerpt` to 80 characters for readability; the offsets themselves are exact in every format. `--output text` prefixes each finding with `L<n>`, the **line** number, for a human reading the file; the `sentence` field in the structured formats is the sentence index, and the two differ.

## CI gating

Three gates, all of which still print the full result to stdout before failing, so the log shows the numbers that caused it:

```bash
text readability --file post.md --metrics flesch --fail-under 60      # reading ease: at least
text readability --file post.de.md --metrics wstf1 --fail-over 10     # grade level: at most
text lint --file post.md --fail-on-findings --severity warn           # no warn-level finding
```

Pointing a gate the wrong way is an `invalid_args` error that names the flag you wanted, rather than a silently inverted verdict:

```
$ text readability --file post.md --metrics lix --fail-under 60
{"error":{"code":"invalid_args","message":"--fail-under applies to metrics where a higher score is easier, but --metrics selected only \"lix\", where a lower score is easier","hint":"Gate those with --fail-over instead, or select a metric where a higher score is easier.","retriable":false}}
```

Under `--metrics auto` a gate simply applies to the metrics it fits and ignores the rest, because the selection was the registry's choice and not the user's.

In a workflow:

```yaml
- name: Readability gate
  run: |
    text readability --file docs/guide.md --metrics flesch --fail-under 60 --output text
    text lint --file docs/guide.md --severity warn --fail-on-findings --output text
```

## Entities and salience

> **Breaking change in 0.2.0.** Entity analysis moved from Cloud Natural Language **v2 to v1**. v2 dropped salience; v1 has it, and salience is the more useful signal.

- Every entity now carries `salience` — how central it is to its document, in `[0, 1]`.
- `probability` is **`0` for every Google entity**. v1 has no per-mention probability. The key stays in the JSON contract because it is the natural confidence field for other backends, but do not read it as a confidence for this one.
- `--min-salience` is the canonical filter flag. `--min-probability` remains as an alias for the same threshold.
- **Salience sums to about 1.0 across one document's entities, so the useful threshold range is 0.01–0.1, not 0.5–0.8.** A script still passing `--min-probability 0.8` from the v2 days will return almost nothing. 0.05 is already a strong entity; 0.8 essentially never happens.
- v1 restores `wikipedia_url` and `mid` for known entities, which is what makes `--enrich` and `text kb` work.
- `language_supported` is always `true` for the Google backend: v1 errors out on an unsupported language instead of answering best-effort. Treat the field as meaningful only for providers that report it.

Three flags on top:

```bash
# Merge entities across a whole corpus into one ranked list
jq -c '{id, text}' posts.jsonl | text entities --input-format jsonl --aggregate --top 20

# Order by something other than salience
text entities --file post.md --sort mentions

# Attach the Wikipedia description and lead paragraph to each entity
text entities --file post.md --top 5 --enrich
```

`--aggregate` produces `combined_salience` (the sum of the per-document salience — read it as a count, bounded by the number of documents rather than by 1.0), `avg_salience`, `mentions`, and `documents`. Merging keys on the case-folded name **plus** the type, so "Apple" the ORGANIZATION and "apple" the CONSUMER_GOOD stay apart.

**Filter composition matters.** `--types` and the salience threshold are per-document statements and apply **before** the merge; `--sort` and `--top` are statements about the final list and apply **after** it. `--enrich` runs after filtering too, so no Wikipedia lookup is paid for an entity `--top` was about to discard, and it is ignored with `--aggregate` (a merged entity has no single article). All of this happens after the cache, so tightening a filter never costs another API call.

## Pipelines

`text` is built to sit in the middle of a pipe. Real, copy-pasteable examples:

```bash
# Score a German blog post you just wrote
cat post.md | text readability --lang de
```

```bash
# Lint findings into jq: the ten worst spans, as offsets an editor can apply
text lint --file post.de.md --severity warn \
  | jq -r '.data.findings | sort_by(-(.value // 0))[:10][] | "\(.start)-\(.end)\t\(.rule)\t\(.message)"'

# Or one finding per line, for a script
text lint --file post.de.md --output ndjson | jq -c 'select(.rule == "passive")'
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
# What is the whole corpus about? One ranked list, merged across every document.
jq -c '{id: .slug, text: .body}' posts.jsonl \
  | text entities --input-format jsonl --min-salience 0.02 --aggregate --sort salience --top 25 --output csv \
  > topics.csv
```

```bash
# Entities, enriched with Wikipedia, straight into a briefing
text entities --file post.md --types PERSON,ORGANIZATION --top 5 --enrich \
  | jq -r '.data.entities[] | "\(.name) (\(.type), salience \(.salience))\n  \(.knowledge.description // "—")"'

# Or keep the two calls separate: entity names feed kb lookup directly
text entities --file post.md --top 10 --output ndjson | jq -r .name | text kb lookup --output csv
```

```bash
# Did the rewrite actually help? One row per metric, with the direction resolved.
text diff draft1.md draft2.md --output table

# Diff a generated draft against the saved original without a temp file
text diff original.md - --output text < rewrite.md

# Fail a script when the rewrite made things worse on the metric you care about
text diff draft1.md draft2.md --metrics amstad \
  | jq -e '.data.metrics[0].improved' > /dev/null || echo "rewrite regressed" >&2
```

```bash
# Feeding an LLM: TOON, not JSON. Same envelope, fewer tokens.
{
  echo "Rewrite the flagged spans. start/end are byte offsets into the source."
  echo "---"
  cat post.de.md
  echo "---"
  text lint --file post.de.md --severity warn --output toon
} > prompt.txt   # then hand prompt.txt to whatever CLI drives your model
```

```bash
# Straight into a spreadsheet
text readability --file report.md --metrics all --output csv > readability.csv
```

```bash
# Sweep a content directory: one JSON line per file, tagged with its path
find content -name '*.md' -print0 \
  | xargs -0 -n1 sh -c 'jq -Rnc --arg id "$0" --rawfile t "$0" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson \
  > sweep.ndjson

# The twenty hardest pages
jq -s -c 'sort_by(.flesch)[:20][]' sweep.ndjson
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
| `cached` | the whole answer came from the local cache |
| `cached_at` | RFC 3339 timestamp of the cached entry (omitted when not cached) |
| `ttl_remaining_sec` | seconds of TTL left, `null` when not cached |
| `api_calls` | remote calls this invocation made — `0` for readability, lint, diff, and metrics, always |
| `language` | the language the analysis ran in |
| `language_detected` | `true` when the language was detected rather than given with `--lang` |
| `provider` | the backend name, for commands that call one (`google`, `wikipedia`) |
| `documents` | number of input documents processed |
| `truncated` | set when input hit `--max-bytes` |

### `--output toon`

TOON (Token-Oriented Object Notation) is the same envelope in a compact, indentation-based encoding: JSON's data model without most of its punctuation, and with the field names of a uniform array of objects hoisted into a single header line.

```
$ text readability "The quick brown fox jumps over the lazy dog." --metrics flesch --output toon
data:
  id: "0"
  language: en
  language_detected: true
  metrics[1]{extra{asl,asw,sentences,syllables,words},grade,language,level,metric,scale,score,title}:
    9,1.22,1,11,9,5th grade,en,very easy,flesch,"0–100, higher is easier",94.3,Flesch Reading Ease
  stats:
    avg_sentence_length: 9
    avg_syllables_per_word: 1.2222222222222223
    avg_word_length: 3.888888888888889
    characters: 35
    long_words: 0
    monosyllabic_words: 7
    polysyllabic_words: 0
    sentences: 1
    syllables: 11
    words: 9
meta:
  api_calls: 0
  cached: false
  documents: 1
  language: en
  language_detected: true
  ttl_remaining_sec: null
```

The `metrics[1]{…}:` line is the header: one array of one uniform object, its field names hoisted, its values on the row below. That is where the token saving comes from, and it grows with the number of rows.

**Use it when the output is going into an LLM prompt. Use JSON when it is going into `jq`.** The two are one document in two encodings — the TOON path marshals the same envelope, so every `json` tag and `omitempty` applies identically — but nothing in the usual toolchain parses TOON.

Measured on this CLI's own payloads with the `cl100k_base` tokenizer, TOON costs **16–40% fewer tokens** than the equivalent JSON: 41% on `text diff`, 24% on `text lint rules` and `text kb search`, around 19–20% on `readability` output and `metrics list`, 16% on a `kb lookup` batch with long extracts. The saving is largest where the array is uniform and the values are short, and smallest where one long string dominates the payload.

Set it per call with `--output toon`, or as the default with `text config set defaults.output toon`.

Other formats: `--output ndjson` drops the envelope and emits one flat record per line, which is the right shape for batches — each line is complete on its own, so a consumer can start work before the run finishes. `--output csv` and `--output table` are rectangular; `--output text` is for a human.

Errors are a single JSON line on **stderr**, in every output format:

```json
{"error":{"code":"empty_input","message":"input contained no text","hint":"Check the file or the upstream command in your pipeline.","retriable":false}}
```

## Exit codes

| exit | class | codes |
|---|---|---|
| 0 | success | — |
| 1 | generic | `generic` — including a failed `--fail-under` / `--fail-over` / `--fail-on-findings` gate |
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
output = "json"     # json|toon|ndjson|csv|table|text; empty means auto (table on a TTY)
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

Manage it with `text config get|set|list|path`. `text config list` prints every key with its current value. The directory is `text-cli`, not `text`, on purpose — `text` is too generic a name for a shared `~/.config` namespace.

The `entities` section also governs `sentiment` and `classify`: they share the provider, the credential chain, and `cache.ttl_entities`. Knowledge lookups (`text kb`, `--enrich`) use the same cache directory with a fixed 24h TTL; `--cache-ttl` overrides it per call.

## Roadmap / extending

The registries are the point. There are four, and each follows the same rule — **register, don't wire**:

| registry | package | what it adds |
|---|---|---|
| metrics | `internal/analyze` | a readability formula, visible in `text metrics list` and `--metrics all` at once |
| entity providers | `internal/entity` | an entity backend, optionally sentiment and classification via capability interfaces |
| lint rules | `internal/lint` | a check, visible in `text lint rules` and selected by `--rules auto` for its languages |
| knowledge sources | `internal/knowledge` | a database behind `text kb` and `text entities --enrich` |

Each is one file with a `Register` call in `init()`: no command wiring, no switch statement, no list of features repeated anywhere.

Shipped since 0.1.0: the lint engine, `diff`, `sentiment`, `classify`, ten more metrics, Markdown/HTML stripping, TOON output, and — no longer "planned" — **knowledge-database lookups**, with Wikipedia as the first source behind `text kb` and `text entities --enrich`.

Still ahead:

- **More metrics.** Further readability formulas, and measurements beyond readability.
- **More lint rules**, and rule packs for languages beyond English and German.
- **More backends.** A second knowledge source (Wikidata, an internal database) lands as a registration, not a rewrite.

See [docs/EXTENDING.md](./docs/EXTENDING.md) for the four extension recipes, with the real signatures from this repo.

## Non-goals

- No writes anywhere — `text` reads text and prints numbers and findings. It never rewrites your document; it tells a human or an LLM exactly where to.
- No web UI, no daemon mode.
- No visualisation — pipe CSV into your plotting tool of choice.

## License

MIT
