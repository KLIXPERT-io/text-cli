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
- **`--file` reads documents, not just text files.** PDF, DOCX, PPTX, ODT, ODS, EPUB and RTF are decoded to markdown before analysis — no `pdftotext`, no `pandoc` — so `text lint --file report.pdf` reports on the file a colleague actually sent, with the heading structure intact. `--file` is repeatable, and binary input that no decoder reads is refused rather than scored. `text extract report.pdf` prints the same markdown, writes it with `--out`, or pipes it onwards.
- **`--url` reads the web.** `text fetch` scrapes any page to clean prose via Firecrawl, and every analysis command takes `--url` directly — so `text entities --url https://…` works without a pipe. Pages are cached, so analysing one page three ways costs one scrape.
- **`text docs` works with Google Docs.** Read a document as prose, read the comments on it, reply and resolve, replace a passage, insert a paragraph. A Google Docs URL routes there automatically, so `text lint --url <doc-url>` reports what to fix in a document — and `text docs replace` applies it. Access is granted by sharing the document with the CLI's service account, like a colleague.
- **`text research`** searches scientific literature (arXiv, PubMed, DOI) and returns abstracts — which are prose, so the rest of the CLI composes straight onto them.
- Seven registries — metrics, entity providers, lint rules, knowledge sources, fetchers, research sources, document decoders — so a new measurement, rule, backend, or file format is one file that wires itself.
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

`text` ships an agent skill that teaches LLM coding agents how to drive the CLI (commands, flags, the JSON envelope, exit codes, which metric fits which language, when to ask for TOON instead of JSON).

It is split for progressive disclosure: `SKILL.md` is a short router carrying only what is true of every command — input precedence, the envelope, exit codes, the cost of each backend — and one file per capability under `references/` that an agent reads only when the task needs it (`scoring`, `lint`, `entities`, `knowledge`, `web`, `research`, `io`). A readability question never loads the entity documentation.

Install it into any tool that supports the [`skills`](https://github.com/anthropics/skills) format:

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

### Google Docs access (only for `text docs` and Google Docs URLs)

`text docs` authenticates as a **service account**, and a service account sees a document only when the document has been shared with it — exactly like a colleague. There is no consent screen and no way for the CLI to grant itself access.

1. **Enable both APIs** on a Google Cloud project: [Google Docs API](https://console.cloud.google.com/apis/library/docs.googleapis.com) and [Google Drive API](https://console.cloud.google.com/apis/library/drive.googleapis.com). Drive is not optional: comments do not exist in the Docs API.
2. **Create a service account and a JSON key**, exactly as above — the same key works for both features.
3. **Point `text` at it**, then print the address to share with:

   ```bash
   text config set docs.service_account_path ~/secrets/text-sa.json
   text docs whoami
   # text-cli@your-project.iam.gserviceaccount.com
   ```

4. **Share the document with that address** — open it in Google Docs, click Share, paste the address, pick **Viewer** to read or **Editor** to write, and untick "Notify people".

The credential chain is the shared one: `--service-account`, then `TEXT_SERVICE_ACCOUNT`, then `GOOGLE_APPLICATION_CREDENTIALS`, then `docs.service_account_path`, then `entities.service_account_path`. One machine usually has one Google credential, so a key already configured for `text entities` works here once the two APIs are enabled on its project.

Google answers a request for a document you cannot see with **404, not 403** — deliberately, so an id cannot be probed. `text` reports that as what it almost always is, with the address to fix it already in the hint:

```json
{"error":{"code":"not_found","message":"the document is not available to this account: Requested entity was not found.","hint":"Google returns \"not found\" for a document that exists but has not been shared. Share the document with text-cli@your-project.iam.gserviceaccount.com as Viewer: …"}}
```

### Firecrawl credentials (only for `--url` and `text fetch`)

Fetching a page calls [Firecrawl](https://firecrawl.dev)'s scrape API, which renders JavaScript, follows redirects, and returns markdown rather than a tag soup. Get a key from the [dashboard](https://firecrawl.dev/app/api-keys), then set it either way — environment first, config second:

```bash
export FIRECRAWL_API_KEY=fc-...
text config set firecrawl.api_key fc-...
```

There is deliberately no `--api-key` flag: a credential on the command line lands in shell history and in the process list. `text config list` prints the key as a fingerprint (`…f017f`) rather than in full, because that output is what people paste into bug reports; `text config get firecrawl.api_key` still returns it.

Self-hosting Firecrawl? Point the CLI at it with `text config set firecrawl.base_url http://localhost:3002`.

`text research` uses the same account but does **not** require a key — the literature index answers unauthenticated requests. Setting one raises your rate limit.

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
| `extract` (`convert`, `md`) | a document file as markdown: print it, write it, or pipe it onwards |
| `fetch` (`scrape`, `read`) | a web page as clean prose, ready for any other command |
| `docs read\|comments\|comment\|reply\|replace\|insert\|whoami` (`gdocs`) | Google Docs: read, review, and edit |
| `research papers\|paper\|similar` | scientific literature search, one paper by id, related work |
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

# The web (Firecrawl API key required)
text fetch https://example.com/post
text fetch https://example.com/post --output text | text entities
text readability --url https://example.com/post   # same thing, no pipe
text lint --url https://example.com/post

# Google Docs (service account, shared with the document)
text docs whoami                                  # the address to share the document with
text docs read <doc-url>
text docs comments <doc-url>                      # open review threads, with the quoted passage
text lint --url <doc-url>                         # what to fix, at byte offsets
text docs replace <doc-url> --find "…" --replace "…"
text docs reply <doc-url> <comment-id> "done" --resolve

# Scientific literature (no credentials required)
text research papers "how is text readability measured?"
text research paper arxiv:1706.03762 --query "what is the attention mechanism?"
text research similar arxiv:1706.03762 --intent "cheaper attention variants"

# Config and self-update
text config path
text config list
text update status
```

Input precedence is fixed and identical for every command:

1. `--url <url>` (repeatable) — fetched, then treated exactly like a file
2. `--file <path>` (repeatable; `--file -` for stdin)
3. positional arguments, joined into one document
4. stdin, when it is a pipe or a redirect

A terminal stdin with no arguments is an `empty_input` error, not a hang. The CLI never silently waits for a human to type.

### Document formats

`--file report.pdf` works, and so does `cat report.pdf | text readability`. PDF, DOCX, PPTX, ODT, ODS, EPUB and RTF are decoded to markdown before analysis, which means the heading structure survives into the strip pass and the scores match what you would get from the same text pasted in by hand.

| format | extensions | notes |
|---|---|---|
| `pdf` | `.pdf` | paragraphs, headings and lists are rebuilt from glyph geometry; running headers and footers are dropped; hyphenation at a line break is rejoined |
| `docx` | `.docx` `.docm` | headings from paragraph styles, lists, tables |
| `pptx` | `.pptx` `.pptm` | one heading per slide title, bullets as items; `pages` is the slide count |
| `odt` | `.odt` `.fodt` | headings, lists, tables |
| `ods` | `.ods` `.fods` | one heading per sheet, one row per row |
| `epub` | `.epub` | read in spine order, not archive order |
| `rtf` | `.rtf` | headings from outline levels, bullets recovered from the list text |

Detection is by extension first, then by content — the extension is what you typed and a magic number is only a guess, and DOCX, PPTX, ODT, ODS and EPUB all share one. So a file named wrongly, or arriving on stdin with no name at all, still decodes. `--from <format>` forces one, and `--from text` turns decoding off entirely:

```bash
text readability --file bericht.pdf --lang de
text lint --file rollout.docx --output ndjson
text readability --file a.docx --file b.epub          # two documents, one call
text readability --file mislabelled.txt --from docx   # force past a wrong name
cat deck.pptx | text entities                         # stdin has no name; sniffed
text readability --file export.latin1.txt --from text # real text, invalid UTF-8
```

`--file` is repeatable, so a batch of documents is one invocation rather than a shell loop, and each result is keyed by its path rather than by an index.

A binary file that no decoder claims is refused with `invalid_args` rather than scored, because a reading ease computed over a JPEG is a number with no meaning attached to a file the user believed was analysed. A pre-2007 `.doc`, `.xls` or `.ppt` is named as such and told to re-save. `--from text` is the deliberate escape hatch from the sniff and from half the refusal: a latin-1 export is invalid UTF-8 and would otherwise be read as binary, and it is real text. It does not waive the other half — bytes containing a NUL are refused even under `--from text`, because "treat this as text" is a claim about an encoding, not a licence to score a zip.

### Getting the text out: `text extract`

Analysis commands print numbers. `text extract` prints the document — the same markdown they measure, which is what makes it the way to check an extraction before trusting a score computed from it.

```bash
text extract report.pdf                      # markdown on stdout
text extract report.pdf > report.md          # …or redirect it
text extract report.pdf --out report.md      # …or name the destination
text extract slides.pptx notes.docx --out docs/   # a directory takes one .md each
text extract report.pdf | text lint          # pipe it into anything
text extract report.pdf --strip auto         # flat prose instead of markdown
text extract report.pdf --output json        # the envelope: file, format, title, chars
```

It is the only command whose default output is text rather than the JSON envelope, because its payload *is* text and an envelope would defeat the pipe it exists to feed. `--output json|ndjson|csv|table` all still work.

`--out` is the one place this CLI writes a file you did not already have. An existing path is refused rather than overwritten; `--force` is how a rerun says it meant it. With several documents `--out` must name a directory, so nothing is silently concatenated or clobbered.

Piping and `--file` are equivalent — `text extract a.pdf | text readability` and `text readability --file a.pdf` produce identical numbers — so prefer `--file`, which is one process instead of two. Reach for `extract` when you want the text itself.

Two failures are worth telling apart. A PDF that is a scan carries no text at all, so it is `empty_input` with a hint saying it needs OCR, which this CLI does not do — not a score of zero. A password-protected PDF is `invalid_args` and says so.

Decoded documents carry `format` and the document's own `title` through to the output as passthrough fields, alongside `file` when more than one source was given. They show up in `--output ndjson`:

```console
$ text lint --file rollout.docx --output ndjson | head -1
{"doc_id":"rollout.docx","excerpt":"Einführung","format":"docx","title":"Rollout-Plan",…}
```

### `--max-bytes` and a document

`--max-bytes` bounds the text that gets **analysed**, and for plain text that is also the read: the reader stops at the limit and `meta.truncated` is set. A container cannot be cut that way — half a zip has no central directory and half a PDF has no xref table — so a document is read whole and `--max-bytes` is applied afterwards, to the markdown the decoder produced, on a rune boundary. A separate, fixed ceiling of 100 MiB bounds the container itself; a file above it is refused outright with `invalid_args` rather than half-read into a decode failure.

A third, fixed bound of 64 MiB applies to what a document may **expand to** while it is being decoded, since a zip-based format compresses far better than it decompresses: a small `.docx` whose body inflates past that is refused with `invalid_args` instead of being unpacked into memory. The same reasoning bounds a PDF's declared page count against the size of the file that declares it.

### Shared flags

| flag | meaning |
|---|---|
| `--output json\|toon\|ndjson\|csv\|table\|text` | default `json`, or `table` when stdout is a terminal |
| `--strip auto\|markdown\|html\|none` | reduce markup to prose before analysis; default `auto` |
| `--lang auto\|en\|de` | analysis language; `auto` detects it |
| `-f, --file <path>` | read the text from a file, decoding a document format if it is one (`-` for stdin); repeatable |
| `--from auto\|text\|docx\|epub\|ods\|odt\|pdf\|pptx\|rtf` | force the input format; `auto` detects it, `text` disables decoding |
| `--url <url>` | fetch a web page and analyse it; repeatable |
| `--fetcher <name>` | backend for `--url` and `text fetch` (default `firecrawl`; a Google Docs URL routes to `gdocs` unless this names one) |
| `--main-content` | drop nav, headers, and footers from a fetched page (default true) |
| `--input-format text\|lines\|jsonl` | one document, one per line, or one JSON object per line |
| `--text-field <name>` | JSONL field holding the text (default `text`) |
| `--id-field <name>` | JSONL field holding the document id (default `id`) |
| `--max-bytes <n>` | cap on the analysed text (default 10 MiB); a document is decoded whole first, then capped |
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

## The web as input

Every command reads stdin, a file, or an argument. `--url` adds a fourth source: the page is scraped to clean prose and then treated exactly like a file — same stripping, same language detection, same output envelope.

```bash
# The page, as prose
text fetch https://example.com/post
text fetch https://example.com/post --output text      # just the text, for a pipe
text fetch https://example.com/post --links            # plus outbound links

# Analyse a page directly — no pipe, no temp file
text readability --url https://example.com/post
text entities --url https://example.com/post --min-salience 0.02
text lint --url https://example.com/post --output text

# Several pages at once; each result carries its URL as the document id
text fetch https://a.com/x https://b.com/y --output ndjson
text readability --url https://a.com/x --url https://b.com/y --output csv
```

Two details worth knowing:

- **The fetcher returns markdown, not plain text**, and the CLI's own `--strip` pass reduces it to prose. That is not incidental: a scraper's text extraction flattens a heading into the sentence that follows it, which inflates average sentence length and moves the score. Letting the existing strip pass do the work means `--url` and `cat page.md |` produce identical numbers, byte offsets included.
- **Pages are cached for 24h**, keyed on the URL and the options that change the returned text — not on your output format or filters. Analysing one page for readability, entities, and lint costs one scrape. `--refresh` re-reads it, and also bypasses Firecrawl's own cache so you actually get a fresh page rather than their stored copy.

A page that yields no text — a login wall, a canvas app — is an `empty_input` error naming the likely cause, not a silently empty document that a later command reports as a mystery.

## Google Docs

`text docs` is the same CLI pointed at a document instead of a file — and it is the one place `text` writes.

```bash
# The address to share documents with, before anything else works
text docs whoami

# The document, as markdown
text docs read <doc-url>
text docs read <doc-url> --output text > draft.md

# The review threads, with the passage each one is anchored to
text docs comments <doc-url>
text docs comments <doc-url> --include-resolved --output table

# Analyse it — no pipe, no temp file: a Google Docs URL routes to this backend
text readability --url <doc-url> --lang de
text lint --url <doc-url> --output text
text diff --url <doc-before> --url <doc-after>

# Edit it
text docs replace <doc-url> --find "Die Inanspruchnahme" --replace "Wer die Leistung nutzt" --dry-run
text docs replace <doc-url> --find "Die Inanspruchnahme" --replace "Wer die Leistung nutzt"
text docs insert <doc-url> "Nachtrag: die Frist wurde verlängert."

# Close the loop
text docs reply <doc-url> <comment-id> "Umformuliert — Amstad von 31 auf 58." --resolve
text docs comment <doc-url> --quote "Die Inanspruchnahme" --text "Substantivstil."
```

### The loop this exists for

`text lint` reports an excerpt that is *exactly* the source text at the offsets it names. So the excerpt is the `--find` string, and applying a finding involves no index arithmetic at all:

```bash
text lint --url <doc-url> --output ndjson |
  jq -r 'select(.severity == "warn") | .excerpt'
# → feed one of those to:
text docs replace <doc-url> --find "<excerpt>" --replace "<your rewrite>"
```

`text docs comments` closes the same loop from the human side: a reviewer's comment carries `quoted`, the passage it is attached to, and that is a literal substring of the document too.

### What keeps a write safe

- **An ambiguous `--find` is refused.** The document is read and the matches counted before anything is sent; a string that matches twice is an `invalid_args` error naming the count, not two rewrites. `--all` opts in.
- **No match is `not_found`**, not a successful call that changed nothing — the failure mode where an agent moves on believing the edit landed.
- **Every write pins the revision it read.** If anyone edited the document in between, Google rejects the batch and `text` tells you to re-run, rather than applying an edit computed against text that has moved.
- **`--dry-run`** counts and validates without writing.
- **Read commands hold read-only scopes.** `text docs read` and `text docs comments` authenticate with `documents.readonly` / `drive.readonly`: the token they hold cannot modify anything.

### Limits worth knowing

- **A new comment cannot be anchored to a passage.** The anchor the Docs editor writes is an opaque region descriptor no API can mint, so `text docs comment` posts a document-level comment; `--quote` puts the passage in the comment text. Replies and resolving work fully.
- **`--find` cannot span a paragraph break** — Docs matches within a paragraph — and it is matched literally. Watch for autocorrect: the document may hold a curly quote or an em dash where your source had a straight one.
- **Documents are never cached.** Reading one is free and it may be being typed into right now, so a stored copy would only ever be a way to be wrong. Every other `--url` page is still cached for 24h.
- **Pending suggestions are left out by default** (`--suggestions clean`): the score of "the text plus everything anyone has proposed" is a score of a document that does not exist. `--suggestions accepted` reads it the other way.
- **Images, equations, and footnote markers contribute no prose**, and a table of contents is skipped rather than counted a second time.
- **Google Docs only.** A Sheets, Slides, or Forms URL is rejected by name, not with a puzzling "invalid id".

## Research: the literature

`text kb` answers "what is this thing?" from an encyclopedia. `text research` answers "what has been published about it?" from a paper index spanning arXiv, PubMed, and DOI-addressed work.

```bash
# Search. The query is embedded, not keyword-matched, so a question works better
text research papers "how is text readability measured?"
text research papers "diffusion image synthesis" --limit 20 --categories cs.LG
text research papers "attention" --from 2017-01-01 --to 2018-01-01 --output csv

# One paper, with the passages that answer a question
text research paper arxiv:1706.03762
text research paper arxiv:1706.03762 --query "what is the attention mechanism?"

# Related work, in either direction
text research similar arxiv:1706.03762 --intent "cheaper attention variants"
text research similar arxiv:1706.03762 --intent "vision applications" --relation citers
text research similar arxiv:1706.03762 --intent "sequence models" --relation references
```

Ids are namespaced — `arxiv:1706.03762`, `doi:10.1145/3442188`, `pmid:18027780`, `pmcid:PMC1431743`. A bare number is rejected rather than guessed: it is as likely a PMID as an arXiv id.

`--intent` on `similar` is required and is the point of the command. "Papers like this one" is ambiguous until you say what makes them alike — the method, the application, or the dataset — and the ranking is built from that sentence.

Abstracts are prose, so the rest of the CLI composes onto them:

```bash
# How readable is the literature on readability?
text research papers "readability formulas" --limit 20 --output ndjson |
  jq -r .abstract | text readability --metrics flesch --output csv

# Who and what does a field talk about?
text research papers "sepsis biomarkers" --limit 25 --output ndjson |
  jq -r .abstract | text entities --input-format lines --aggregate --top 20
```

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

[firecrawl]
api_key = "fc-..."          # or export FIRECRAWL_API_KEY, which wins
# base_url = "http://localhost:3002"   # a self-hosted Firecrawl

[docs]
service_account_path = "~/secrets/text-sa.json"   # empty falls back to [entities]

[fetch]
# provider = "firecrawl"    # empty lets the registry pick

[research]
# source = "firecrawl"      # empty lets the registry pick

[cache]
# dir = "~/custom/cache"   # default: <config dir>/cache
default_ttl = "24h"
ttl_entities = "24h"
ttl_fetch = "24h"
ttl_research = "24h"

[logging]
verbose = false
format = "text"
```

Manage it with `text config get|set|list|path`. `text config list` prints every key with its current value. The directory is `text-cli`, not `text`, on purpose — `text` is too generic a name for a shared `~/.config` namespace.

The `entities` section also governs `sentiment` and `classify`: they share the provider, the credential chain, and `cache.ttl_entities`. Knowledge lookups (`text kb`, `--enrich`) use the same cache directory with a fixed 24h TTL; `--cache-ttl` overrides it per call.

The `docs` section holds the Google Docs credential. Empty falls back to `entities.service_account_path`, so one key can serve both — the two features call different Google APIs, which is why the key can also be split if you want one scoped to each.

The `firecrawl` section is shared by `text fetch`, `--url`, and `text research` — one account, one quota. `FIRECRAWL_API_KEY` beats the config file, so CI can override a stored key without editing it. `text config list` fingerprints the key rather than printing it.

## Roadmap / extending

The registries are the point. There are seven, and each follows the same rule — **register, don't wire**:

| registry | package | what it adds |
|---|---|---|
| metrics | `internal/analyze` | a readability formula, visible in `text metrics list` and `--metrics all` at once |
| entity providers | `internal/entity` | an entity backend, optionally sentiment and classification via capability interfaces |
| lint rules | `internal/lint` | a check, visible in `text lint rules` and selected by `--rules auto` for its languages |
| knowledge sources | `internal/knowledge` | a database behind `text kb` and `text entities --enrich` |
| fetchers | `internal/fetch` | a way to turn a URL into prose, behind `text fetch` and `--url`; optionally claiming the URLs only it can read |
| research sources | `internal/research` | a literature index behind `text research`, with optional inspect and related-work capabilities |
| document decoders | `internal/doc` | a file format `--file` can read, decoded to markdown so `internal/strip` stays the one place markup becomes prose |

Each is one file with a `Register` call in `init()`: no command wiring, no switch statement, no list of features repeated anywhere.

Shipped since 0.1.0: the lint engine, `diff`, `sentiment`, `classify`, ten more metrics, Markdown/HTML stripping, TOON output, knowledge-database lookups behind `text kb`, **the web as an input source** (`text fetch`, `--url`) and **literature search** (`text research`) on Firecrawl, **Google Docs** (`text docs`), the first feature that writes, and — newest — **document files as an input source** (`--file report.pdf`, `--from`), which closes the same gap for a file that `--url` closed for a page.

Still ahead:

- **More metrics.** Further readability formulas, and measurements beyond readability.
- **More lint rules**, and rule packs for languages beyond English and German.
- **More backends.** A second knowledge source (Wikidata, an internal database), a credential-free fetcher for static pages, or a second literature index — each lands as a registration, not a rewrite.
- **More document formats.** Same registry, same one-file cost. OCR is explicitly not on the list: a scanned PDF is reported as `empty_input` rather than guessed at.

See [docs/EXTENDING.md](./docs/EXTENDING.md) for the extension recipes, with the real signatures from this repo.

## Non-goals

- **No writes except where you name the target.** Everything that measures text only reads it: `readability`, `lint`, `diff`, `entities`, `fetch` and the rest print numbers and findings and change nothing. The one exception is `text docs`, which edits a Google Doc you named, with a service account you configured, on a document somebody shared with it — and even there the write path is narrow (one literal replacement or one inserted paragraph), refuses an ambiguous match, and pins the revision it read so a concurrent edit is rejected rather than overwritten.
- No rewriting on your behalf — `text` does not decide what your text should say. It tells a human or an LLM exactly where to change it.
- No web UI, no daemon mode.
- No visualisation — pipe CSV into your plotting tool of choice.

## License

MIT
