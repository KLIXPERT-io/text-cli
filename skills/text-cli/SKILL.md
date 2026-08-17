---
name: text-cli
description: Analyse and improve text from the command line with the `text` CLI — readability scoring (Flesch, Amstad, WSTF, LIX, Flesch-Kincaid, Gunning Fog, SMOG, Coleman-Liau, ARI), a lint engine that reports what to fix with exact byte offsets, before/after diffing of drafts, named-entity extraction with salience and Wikipedia identifiers, sentiment, content classification, Wikipedia lookups, scraping any web page to clean prose, and scientific-literature search. Use whenever the task involves how readable, hard, or complex a piece of writing is, making a text simpler or clearer, comparing drafts, checking prose against a reading level in CI, counting words/sentences/syllables, pulling people, places, organizations and products out of text, ranking what a document or a corpus is about, how a text feels, what category it belongs to, looking a named thing up in an encyclopedia, reading or analysing a web page by URL, or finding scientific papers on a topic. Triggers include "how readable is this", "reading level", "Flesch score", "Lesbarkeit", "Lesbarkeitsindex", "simplify this text", "make this easier to read", "is this too hard to read", "rewrite this more clearly", "Passiv vermeiden", "Behördendeutsch", "Substantivstil", "lint my prose", "did my rewrite help", "compare these two drafts", "extract entities", "who and what is mentioned", "named entities", "what is this text about", "main topics", "salience", "sentiment of this review", "is this positive or negative", "classify this article", "look this up on Wikipedia", "word count of this article", "read this URL", "scrape this page", "analyse this web page", "how readable is this article <url>", "find papers about", "what does the research say", "related work", "cite a paper".
---

# text — text analysis CLI

`text` reads prose from stdin, a file, or an argument and prints structured data. It never writes anything but its output.

Commands: `readability`, `lint` (+ `lint rules`), `diff`, `metrics list|show`, `entities`, `sentiment`, `classify`, `kb lookup|search`, `fetch`, `research papers|paper|similar`, `config`, `update`.

Cost: `readability`, `lint`, `diff`, `metrics` are pure local computation and free. `kb` and `research` need the network but no credentials. `entities`, `sentiment`, `classify` call Google Cloud Natural Language and are billed per request. `fetch` and `--url` call Firecrawl and are billed per page — but cached 24h, so analysing one page three ways costs one scrape.

## Input

Precedence, fixed for every command:

1. `--url <url>` (repeatable) — the page is fetched, then treated exactly like a file
2. `--file <path>` (`-` means stdin)
3. positional arguments, joined into one document
4. piped or redirected stdin

A terminal stdin with no arguments is an `empty_input` error, not a hang. Prefer `--file` over `cat file | text …`, and `--url` over `text fetch … | text …`.

Batch input: `--input-format lines` (one document per line) or `--input-format jsonl` (one JSON object per line; `--text-field`, default `text`, and `--id-field`, default `id`). Sidecar JSONL fields are carried through into NDJSON output, so results join back to the source. Pair batches with `--output ndjson`.

## Stripping is on by default

`--strip auto|markdown|html|none`, default `auto`. Markdown and HTML are reduced to prose before anything is measured, so a fenced code block or a URL never counts as a sentence. Plain prose is untouched.

**Do not turn it off to "measure the real document".** Scoring this repo's `docs/EXTENDING.md` raw gives Flesch 48.1 "difficult"; stripped it gives 66.7 "standard" — two bands, caused entirely by fenced code blocks being counted as sentences. `--strip none` is only for text you know carries no markup, or when you deliberately want the markup counted.

Stripping happens once in the shared input loader, so `readability`, `lint`, and `diff` always see the same prose.

## Output contract

Default is JSON, one envelope:

```json
{ "data": { ... }, "meta": { "cached": false, "ttl_remaining_sec": null, "api_calls": 0, "language": "en", "language_detected": true, "documents": 1 } }
```

- `meta.language_detected: true` means the language was guessed, not given. A surprising score with `detected: true` is often a detection miss — re-run with `--lang en` or `--lang de`.
- `meta.cached` / `meta.ttl_remaining_sec` — served from the local cache. `--refresh` forces a fresh call, `--no-cache` bypasses the cache entirely. `cached` is only `true` when the *whole* batch came from cache.
- `meta.api_calls` is `0` for `readability`, `lint`, `diff`, and `metrics`, always. They are pure computation and cost nothing.
- `meta.provider` names the backend for the commands that call one (`google`, `wikipedia`).
- `meta.truncated` means `--max-bytes` cut the input; check it before trusting counts on a large file.

### Choosing a format

| you are… | use |
|---|---|
| feeding the output into an LLM prompt (including your own context) | `--output toon` |
| piping into `jq`, or parsing it yourself | `--output json` |
| streaming a batch, one flat record per line | `--output ndjson` |
| producing a spreadsheet | `--output csv` |
| showing a human | `--output text` (or `table`) |

`--output toon` is the same `{data, meta}` envelope encoded as TOON (Token-Oriented Object Notation): JSON's data model without most of its punctuation, with the field names of a uniform array hoisted into one header line. Measured on this CLI's payloads it costs 16–40% fewer tokens than the same JSON — most on uniform arrays (`diff`, `lint rules`), least when one long string dominates. Nothing in the usual shell toolchain parses it, so never pipe TOON into `jq`. Persist a preference with `text config set defaults.output toon`.

Errors are a single JSON line on **stderr**, in every output format:

```json
{"error":{"code":"unknown_metric","message":"unknown metric: \"foo\"","hint":"Known metrics: amstad, ari, coleman-liau, flesch, flesch-kincaid, gunning-fog, lix, smog, wstf1, wstf2, wstf3, wstf4. Run `text metrics list` for details.","retriable":false}}
```

Read `.error.hint` — it names the command that fixes the problem.

| exit | class | codes | what to do |
|---|---|---|---|
| 0 | ok | — | — |
| 1 | generic | `generic` | read the message; a failed `--fail-under` / `--fail-over` / `--fail-on-findings` gate lands here, and the result was still printed to stdout |
| 2 | auth | `auth_missing`, `auth_expired`, `auth_denied` | credentials for `entities` / `sentiment` / `classify`; see below |
| 3 | quota / rate | `quota_exceeded`, `rate_limited` | back off; check `retry_after_sec` |
| 4 | not found | `not_found` | wrong `--file` path, or `kb lookup` on a title with no article |
| 5 | bad input | `invalid_args`, `empty_input`, `unsupported_language`, `unknown_metric` | read the hint; `text metrics list`, `text lint rules` |
| 6 | network / provider | `network_unreachable`, `api_5xx`, `provider_unavailable` | retriable; retry once. `provider_unavailable` also means "this backend cannot do that" — pick another with `--provider` |

## Readability

```bash
text readability [text...]        # aliases: read, rd, flesch, amstad
```

`data` for a single document is one object: `id`, `language`, `language_detected`, `stats`, `metrics[]`. A batch is a list of those under `data.documents`. Each entry of `metrics[]` has `metric`, `title`, `score`, `level`, `grade`, `scale`, `language`, and `extra` (the counts that produced the score).

Flags: `--metrics auto|all|<names>` (default `auto`: every metric valid for the document's language; `auto` and `all` agree today), `--stats` (on by default, `--stats=false` to drop), `--fail-under`, `--fail-over`.

### The direction split — get this right or every number inverts

| direction | metrics | scale |
|---|---|---|
| **higher is easier** | `flesch` (en), `amstad` (de) | 0–100 reading ease |
| **lower is easier** | `wstf1`–`wstf4` (de), `lix` (any), `flesch-kincaid`, `gunning-fog`, `smog`, `coleman-liau`, `ari` (en) | school grade level |

A rising Flesch is an improvement. A rising Gunning Fog is a regression. Never summarise a grade-level score as "scored 12, quite good" — 12 is twelfth grade, which is hard. Always report the `level` label next to the number; it already encodes the direction.

- **English → `flesch`** (`206.835 − 1.015×ASL − 84.6×ASW`). Aliases `fre`, `flesch-reading-ease`.
- **German → `amstad`** (Amstad 1978, `180 − ASL − 58.5×ASW`). Aliases `flesch-de`, `flesch-amstad`, `fre-de`. It is *the German equivalent of Flesch*, on the same 0–100 scale, so `level` labels are comparable across the two.

**Flesch is English-only and Amstad is German-only, and the registry enforces it.** `--metrics flesch` on German text is an `unsupported_language` error (exit 5), by design: the English constants punish German compounds so hard that almost any German prose scores below zero. Use `--metrics auto` and let the language decide.

Level labels are the domain vocabulary and stay in the document's language. German: `sehr leicht`, `leicht`, `mittelleicht`, `mittel`, `mittelschwer`, `schwer`, `sehr schwer`. English: `very easy`, `easy`, `fairly easy`, `standard`, `fairly difficult`, `difficult`, `very confusing`.

WSTF reports the raw formula output and labels anything outside Schulstufe 4–15 `unter der Skala` / `über der Skala` — a computed 0.0 is not "sehr leicht", it is off the scale, and should be reported as such.

Scores are not clamped. A negative reading-ease score is real and means the text is punishing.

### Discover what is available

```bash
text metrics             # same as `text metrics list`
text metrics list
text metrics show <name> # resolves aliases
```

Read the registry rather than assuming: metrics are added over time, and `metrics list` reports each one's `languages` and `description`.

## Lint — this is how you improve a text

```bash
text lint [text...]
text lint rules          # every registered rule, with languages and severity
```

`readability` says a document is hard. `lint` says which sentence and which phrase, and what to write instead. **This is the command to reach for when the user wants the text made better, not just scored.**

`data` per document: `id`, `language`, `language_detected`, `findings[]`, `summary` (rule name → count), `total`. A batch is a list of those under `data.documents`.

Each finding: `rule`, `severity` (`info`|`warn`), `message`, `suggestion`, `sentence` (1-based, 0 for a document-level finding), `start`, `end`, `excerpt`, and `value` when the rule measured a number.

**`start` and `end` are byte offsets into the analysed text, and `excerpt` is exactly `text[start:end]`.** This is the mechanism for precise editing: slice the source at the offsets, replace the span, done — no searching, no ambiguity, no risk of hitting the wrong occurrence of a repeated phrase. Apply edits back-to-front (highest `start` first) so earlier offsets stay valid.

Two caveats:

- The offsets index the **stripped** text, which is what was analysed. If you stripped Markdown, apply edits against the stripped text or re-run with `--strip none` when you must patch the raw file.
- `text[start:end] == excerpt` holds exactly in **JSON, TOON, and NDJSON** — the machine-facing formats. Only `csv`, `table`, and `text` shorten `excerpt` to 80 characters for display; the offsets are exact in every format. `--output text` prefixes findings with `L<n>`, the *line* number, not the sentence index.

Flags: `--rules auto|all|<names>` (default `auto`: every rule registered for the document's language), `--severity info|warn` (minimum to report, default `info`), `--worst <n>` (hard sentences to report, default 5), `--max-sentence-words <n>` (default 25), `--max-word-chars <n>` (default 20), `--fail-on-findings`.

Rules today (run `text lint rules` for the live list): `hard-sentence`, `long-sentence`, `long-word`, `repeated-word`, `repeated-sentence-start`, `sentence-length-variance`; German `passive` (Passiv), `nominalization` (Substantivstil), `bureaucratic` (Behördendeutsch), `filler` (Füllwörter), `modal-hedge` (Konjunktiv-Häufung); English `passive`, `nominalization`, `filler`, `hedge`, `adverb`. A rule name may have per-language variants — `passive` means the German detector on German text and the English one on English text, and the summary key stays `passive` either way.

A good rewrite loop: `text lint --file doc.md` → apply the fixes at the offsets → `text diff doc.orig.md doc.md` to prove it worked.

## Diff

```bash
text diff <before> <after>
```

Both arguments are file paths; `-` reads stdin for at most one of them, so a generated draft can be diffed against the original without a temp file.

`data`: `before` and `after` (each `id`, `language`, `language_detected`, `stats`), `metrics[]`, `stats_delta`. Each metric row: `metric`, `title`, `before`, `after`, `delta`, `improved`, `direction` (`higher-is-easier` | `lower-is-easier`), `level_before`, `level_after`.

**Read `improved`, not the sign of `delta`.** `improved` is derived from the metric's declared direction, so a falling LIX and a rising Flesch are both `true`.

Flags: `--metrics`.

## CI gating

```bash
text readability --file post.md --metrics flesch --fail-under 60    # reading ease: at least
text readability --file post.de.md --metrics wstf1 --fail-over 10   # grade level: at most
text lint --file post.md --severity warn --fail-on-findings
```

`--fail-under` is for higher-is-easier metrics, `--fail-over` for lower-is-easier ones. Pointing one the wrong way at an explicitly named metric is an `invalid_args` error (exit 5) whose message names the flag you wanted. Under `--metrics auto` a gate applies to the metrics it fits and ignores the rest.

A failed gate still prints the full result to stdout, then exits 1. Read stdout for the numbers, stderr for the reason.

## Entities

```bash
text entities [text...]   # alias: ents
```

Calls Google Cloud Natural Language **v1** `AnalyzeEntities`. Costs money and network.

A single document renders as one object: `id`, `provider`, `language`, `language_supported`, `entities[]`. A batch is a list of those under `data.documents`.

Each entity: `name`, `type`, `salience`, `probability`, `mention_count`, `mentions[]`, `metadata`, and — where the provider knows them — `wikipedia_url` and `mid`.

### Salience is the ranking signal

- `salience` is how central the entity is to its document, in `[0, 1]`. **Within one document the salience of all entities sums to about 1.0.** It is a share of attention, not a confidence: 0.4 means "this document is largely about this", never "we are 40% sure".
- **Useful thresholds are therefore small: 0.01–0.1.** 0.05 is already a strong entity. `--min-salience 0.5` or `0.8` will return almost nothing — if you are carrying such a value over from an older script, it is wrong now.
- `probability` is **`0` for every Google entity**. v1 has no per-mention probability. Never rank, filter, or report on it for this backend.
- `--min-salience` is the canonical flag; `--min-probability` is an alias for the same threshold, kept for old scripts. An explicit `--min-salience` wins when both are given.
- `language_supported` is always `true` for the Google backend — v1 rejects an unsupported language with an error rather than answering best-effort. Only treat a `false` as meaningful for providers that report it.

Types: `PERSON`, `LOCATION`, `ORGANIZATION`, `EVENT`, `WORK_OF_ART`, `CONSUMER_GOOD`, `NUMBER`, `DATE`, `ADDRESS`, `PHONE_NUMBER`, `PRICE`, `OTHER`, `UNKNOWN`. `--types person,organization` is case-insensitive.

### Flags

`--provider <name>`, `--service-account <key.json>`, `--types <CSV>`, `--min-salience <0..1>`, `--min-probability <0..1>` (alias), `--sort salience|mentions|name` (default `salience`), `--top <n>`, `--aggregate`, `--enrich`, `--timeout <duration>`.

`--aggregate` merges the same entity across every input document into one corpus-level list: `name`, `type`, `combined_salience`, `avg_salience`, `mentions`, `documents`, `wikipedia_url`, `mid`, under `data.entities` with `data.documents` as the count. `combined_salience` is the **sum** of per-document salience, so it is bounded by the number of documents, not by 1.0 — read it as "how many documents' worth of attention this owns". The merge key is the case-folded name plus the type, so "Apple" the ORGANIZATION and "apple" the CONSUMER_GOOD stay separate.

`--enrich` attaches `knowledge` (`source`, `title`, `description`, `extract`, `url`, `lang`, `disambiguation`) to every entity that has a `wikipedia_url`. It never fails the command: an entity with no article simply carries no `knowledge` key. It is ignored with `--aggregate`.

**Filter composition:** `--types` and the salience threshold apply **per document, before** any merge. `--sort` and `--top` apply **after** it. `--enrich` runs after filtering, so no lookup is paid for an entity `--top` discarded. All of it happens after the cache, so tightening a filter never costs another API call.

`--output ndjson` emits one entity per line, tagged with `doc_id` and `provider`, plus any passthrough JSONL fields. `--output csv` gives `doc_id,name,type,salience,probability,mentions,wikipedia_url` (`--aggregate` gives `name,type,combined_salience,avg_salience,mentions,documents,wikipedia_url`).

Results are cached per (provider, language, text), so re-running the same document costs nothing. `--refresh` forces a fresh call.

## Sentiment

```bash
text sentiment [text...]   # alias: sent
```

`data` per document: `id`, `provider`, `language`, `score`, `magnitude`, `label`, `sentences[]`.

- `score` is −1.0 to +1.0: which way the text leans. It is an average, so opposing parts cancel out.
- `magnitude` is 0 to +inf: how much feeling there is in total, in either direction. Not normalized, grows with length — only comparable between documents of similar size.
- `label` is derived from both. Report the pair, not just the score: a near-zero score with high magnitude is `mixed` (half furious, half delighted), a near-zero score with low magnitude is `neutral`. They mean opposite things.

`--sentences` is on by default and adds the per-sentence breakdown to JSON. Passing it *explicitly* also switches `csv`, `table`, and `text` to one row per sentence. The provider is always asked for sentences and the full answer is cached, so toggling it never costs another call.

## Classify

```bash
text classify [text...]   # alias: cls
```

`data` per document: `id`, `provider`, `language`, `categories[]` with `name` (a taxonomy path like `/Computers & Electronics/Software`) and `confidence`.

Confidences are per category and **independent** — unlike entity salience they do not sum to 1.0, because a document really can belong to several categories. Split `name` on `/` for the hierarchy.

Needs roughly 20+ words. Shorter input is rejected with `invalid_args` (exit 5) before the call is made — do not retry with the same text; use `entities` or `sentiment` on short input instead.

Flags: `--top <n>`, `--min-confidence <0..1>`.

## Knowledge base

```bash
text kb lookup [title...]    # alias: knowledge
text kb search <query...>
```

Wikipedia, no credentials. Cached 24h.

`lookup` takes titles as arguments or one per line on stdin, so it sits at the end of a pipe. An `Article` has `title` (the resolved one — a redirect lands on the canonical page), `description`, `extract` (the lead paragraph, plain text), `url`, `lang`, `thumbnail_url`, `aliases`, `disambiguation`.

**Check `disambiguation`.** A `true` there means the title resolved to a "X may refer to:" page, not a thing; its `extract` is useless. Treat it as a miss and `text kb search` for a better title instead.

In a batch, a missing title is reported on stderr and left out of the results — a list of entity names always contains things no encyclopedia has. A *single* missing title is a `not_found` error (exit 4), so a scripted one-shot lookup still fails loudly.

`search` is the recovery path: article titles are exact, so "Ada Lovelace" is a page and "ada lovelace biography" is not. `--limit <n>` (default 10).

Compose with entities:

```bash
text entities --file post.md --top 10 --output ndjson | jq -r .name | text kb lookup
```

Or skip the pipe entirely with `text entities --enrich`, which uses the `wikipedia_url` the provider already returned and therefore resolves the right article and the right language edition.

## Web pages

```bash
text fetch <url...>          # aliases: scrape, read
text readability --url <url> # or entities, lint, sentiment, classify, diff — any command
```

`--url` is an input source, not a separate workflow. **Prefer it over piping**: `text entities --url X` is one command, hits the same cache, and gives each document the URL as its `id` instead of `0`.

A `Page` has `url` (after redirects), `requested_url` (only when it differs), `title`, `description`, `language` (the page's *declared* language — a hint, not an answer), `content` (markdown), `links` (with `--links`), `status_code`, `fetcher`, `credits`.

- **`--output text` prints only the page content**, which is what makes `text fetch X --output text | text …` safe. Any other format wraps it in the envelope.
- **Check `status_code`.** A scrape can succeed at fetching a 404 page; the fetch worked, the page is an error page.
- **A page with no text is `empty_input` (exit 5) with a hint**, not an empty result. If the hint suggests `--main-content=false`, that means the boilerplate extractor probably ate the body — retry with it.
- **`--refresh` bypasses both caches**, this CLI's and Firecrawl's, so you actually get a fresh page.
- Multiple URLs are fetched concurrently. A dead link in a batch is a stderr warning; a missing API key or a rate limit aborts, because it would repeat for every remaining URL.

## Research — scientific literature

```bash
text research papers <query...>   # search
text research paper <id>          # one paper, optionally with passages
text research similar <id>        # related work
```

No credentials required. Cached 24h. arXiv, PubMed, and DOI-addressed work behind one id scheme.

**Ids are namespaced**: `arxiv:1706.03762`, `doi:10.1145/3442188`, `pmid:18027780`, `pmcid:PMC1431743`. A bare number is an `invalid_args` error, not a guess — it is as likely a PMID as an arXiv id. Get an id from the `id` / `primary_id` field of a search result; never invent one.

A `Paper` has `id`, `primary_id`, `ids` (every external identifier, by namespace), `title`, `abstract`, `authors` (one string, not a list), `categories`, `published`, `updated`, `score`, `url`.

- **The query is embedded, not keyword-matched.** A question retrieves better than a keyword list.
- **`score` is comparable within one result set and meaningless across two.** Never threshold on it, and never compare a search score to a `similar` score.
- `papers` filters: `--limit` (default 10, capped at 100), `--authors`, `--categories` (e.g. `cs.LG`), `--from` / `--to` (`YYYY-MM-DD`).
- `paper --query "<question>"` also returns the passages of the full text most relevant to that question — this is what makes it usable as a source rather than just a citation. Without `--query` you get the record alone.
- `similar --intent "<what makes a neighbour interesting>"` is **required**. "Papers like this" is ambiguous until you say whether you mean the method, the application, or the dataset. `--relation similar|citers|references` picks the direction: same subject, later work citing it, or its own bibliography.

Abstracts are prose, so they feed the rest of the CLI:

```bash
text research papers "readability formulas" --limit 20 --output ndjson |
  jq -r .abstract | text entities --input-format lines --aggregate --top 20
```

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
  | xargs -0 -n1 sh -c 'jq -Rnc --arg id "$0" --rawfile t "$0" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson
```

## Credentials

For `entities`, `sentiment`, and `classify` only, highest precedence first: `--service-account`, `TEXT_SERVICE_ACCOUNT`, `GOOGLE_APPLICATION_CREDENTIALS`, config `entities.service_account_path`, then Application Default Credentials. On exit 2, read the hint — it names the console page. The Cloud Natural Language API must be enabled on the project.

For `fetch` and `--url`: `FIRECRAWL_API_KEY`, then config `firecrawl.api_key`. There is no `--api-key` flag by design. On exit 2 the hint names the dashboard page — stop and tell the user, do not retry.

`readability`, `lint`, `diff`, `metrics`, `kb`, and `research` need no credentials at all.

## Rules of thumb

- The user wants the text *better*, not just scored → `text lint`, then edit at the byte offsets, then `text diff` to prove it. `readability` alone answers "how bad", never "what to change".
- Leave `--strip` alone. The default is right, and turning it off on a Markdown file produces a wrong number.
- Use `--metrics auto` unless the caller named a formula. It picks the right one per document, even in a mixed-language batch.
- Report the `level` label alongside the number, and say which way the metric runs. The raw score means little on its own and is easy to invert.
- Never quote a Flesch score for German text, or an Amstad score for English.
- Set `--lang` explicitly when you know the language; detection is a heuristic and `meta.language_detected` tells you when it was used.
- Rank entities by `salience`, never by `probability`, and keep thresholds in the 0.01–0.1 range.
- Reach for `entities` / `sentiment` / `classify` only when the task actually needs them — they are the only commands that spend money. Readability and lint questions never do.
- Given a URL, use `--url` on the command you actually want rather than `text fetch | …`. Same cache, fewer moving parts, and the document id becomes the URL.
- Do not re-fetch a page you already fetched in this session — it is cached for 24h, so a second command against the same URL is free. Do not add `--refresh` unless the page is expected to have changed.
- For "what does the research say about X", `text research papers` first, then `text research paper <id> --query "<the actual question>"` on the best hits. Quote the abstract or a passage, and cite the `url`.
- Never invent a paper id or a DOI. Take it from a search result's `primary_id`.
- `--output toon` when the result goes into a prompt, `--output json` when it goes into `jq`.
- On exit 5, run `text metrics list` or `text lint rules` before retrying; on exit 2, stop and tell the user which credential is missing rather than retrying.
- Large batches: write NDJSON to a file and query it with `jq`, do not read every line into context.
