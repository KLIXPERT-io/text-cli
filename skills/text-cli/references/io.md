# I/O contract — input, output, errors, credentials

Everything shared by every command: where the text comes from, what the output looks like, how failures are reported, and which credential each backend needs. Feature-specific shapes live in the other references.

---

## Input

Precedence is fixed and identical for every command:

1. `--url <url>` — repeatable. The page is fetched and then treated exactly like a file. See [web.md](web.md).
2. `--file <path>` — repeatable. `-` means stdin.
3. **positional arguments**, joined with a space into one document.
4. **piped or redirected stdin.**

A terminal stdin with no arguments is an `empty_input` error (exit 5), never a hang — the CLI does not silently wait for a human to type.

Prefer `--file doc.md` over `cat doc.md | text …`, and `--url X` over `text fetch X | text …`. Both are one process instead of two, and both give the document a meaningful `id`.

### Document formats

`--file` reads PDF, DOCX, PPTX, ODT, ODS, EPUB and RTF directly. **Do not convert a document first** — no `pdftotext`, no `pandoc`, no unzipping a `.docx`, and do not tell the user to export it. Each is decoded to markdown, so headings and lists survive into the strip pass and the scores match the same text pasted in by hand. A converted-to-plain-text file loses the block structure and scores differently, usually worse.

| `--from` | extensions | what it produces |
|---|---|---|
| `pdf` | `.pdf` | paragraphs, headings and lists rebuilt from glyph geometry; running headers and footers dropped; a word hyphenated across a line break rejoined |
| `docx` | `.docx` `.docm` | headings from paragraph styles, lists, tables |
| `pptx` | `.pptx` `.pptm` | one heading per slide title, one item per bullet; speaker notes deliberately not read |
| `odt` | `.odt` `.fodt` | headings, lists, tables |
| `ods` | `.ods` `.fods` | one heading per sheet, one row per row |
| `epub` | `.epub` | chapters in spine order, not archive order |
| `rtf` | `.rtf` | headings from outline levels, bullets recovered from the list text |

Detection is by extension first, then by content, so stdin and a misnamed file both work:

```bash
text readability --file bericht.pdf --lang de
text readability --file a.docx --file b.epub        # two documents, one call
cat report.pdf | text lint --output ndjson
text readability --file odd-name.bin --from docx    # force it
```

`--file` is repeatable — use it instead of a loop, exactly as you would use JSONL for a batch of strings.

`--from text` disables decoding for the whole run — use it for a file that is real text but not valid UTF-8 (a latin-1 export), which `auto` would otherwise refuse as binary. It is the only way past that refusal, and it does not extend to bytes containing a NUL: `--from text --file report.docx` is still `invalid_args`, never a score over the compressed bytes.

### Getting the text out — `extract`

`text extract <file...>` prints the decoded markdown instead of measuring it. Use it when the user wants the *content* of a document, not a number about it: "read this PDF", "convert this docx to markdown", "what does this deck say".

```bash
text extract report.pdf                    # markdown on stdout
text extract report.pdf --out report.md    # write it; refuses to overwrite without --force
text extract a.docx b.pptx --out docs/     # a directory takes one .md per document
text extract report.pdf --strip auto       # plain prose instead of markdown
text extract report.pdf --output json      # envelope: file, format, title, chars, content
```

It is the **only** command that defaults to `--output text` rather than the JSON envelope, so it pipes cleanly. Do not pipe it into another `text` command to analyse it — `--file` does the same thing in one process and gives a better `id`. `--out` is the only file this CLI writes on the user's disk; an existing path is an `invalid_args` error, never a silent overwrite.

Decoded documents add `format` and the document's own `title` as passthrough fields, plus `file` when there is more than one source. Read them from `--output ndjson`. There is **no page count in the output**: the decoders record one for PDF and PPTX, but no command emits it — do not report a page count you did not see.

`--max-bytes` caps the **analysed text**, not the file: a document is decoded whole and then capped, so `meta.truncated` on a PDF means the extracted markdown was long, not that the file was. A container above a fixed 100 MiB ceiling is refused outright, as is one whose parts expand past 64 MiB while being decoded — that one reads as "expands to more than 64 MiB of text" and means the file is a compression bomb or a machine-generated dump, not that the prose is long.

**Failure modes worth recognising.** None are retriable, and none should be answered with a score:

| message | code / exit | meaning |
|---|---|---|
| `… is binary, not text` | `invalid_args`, 5 | no decoder claims it; the hint lists the extensions that work. Say so — do not fall back to a converter |
| `… is a pre-2007 Office file (.doc/.xls/.ppt)` | `invalid_args`, 5 | tell the user to open it and save as `.docx`/`.xlsx`/`.pptx` |
| `unknown input format: "x"` | `invalid_args`, 5 | a bad `--from`; the hint lists the valid names |
| `cannot read PDF: …` (password) | `invalid_args`, 5 | password-protected. Stop; the CLI cannot open it |
| `… decoded as pdf but contains no text` | `empty_input`, 5 | a scanned or image-only document. It needs OCR, which this CLI does not do. Do not retry, and do not report a score |
| `no page of this PDF could be read` | `invalid_args`, 5 | corrupt or an unsupported encoding. A single bad page is skipped silently; this means every page failed |

### Batch input

| flag | meaning |
|---|---|
| `--input-format text` | default — the whole input is one document |
| `--input-format lines` | every non-empty line is its own document |
| `--input-format jsonl` | one JSON object per line |
| `--text-field <name>` | JSONL field holding the text (default `text`) |
| `--id-field <name>` | JSONL field holding the id (default `id`) |
| `--max-bytes <n>` | cap on the analysed text (default 10 MiB); a document is decoded whole first |

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
- **`truncated`** — `--max-bytes` cut the input. Check it before trusting counts on a large file, and on a decoded document read it as "the extracted text was long", not "the file was".

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
| `fetch`, `--url` on a web page | `FIRECRAWL_API_KEY`, then config `firecrawl.api_key` |
| `docs`, `--url` on a Google Doc | `--service-account`, `TEXT_SERVICE_ACCOUNT`, `GOOGLE_APPLICATION_CREDENTIALS`, config `docs.service_account_path`, then config `entities.service_account_path` — **plus the document shared with that service account** |

`readability`, `lint`, `diff`, `metrics`, `kb`, and `research` need **no credentials at all**.

A key alone does not open a Google Doc: the document must also be shared with the service account's address (`text docs whoami`). A `not_found` from a `docs` command means that sharing has not happened — Google returns 404 rather than 403 for a document the account cannot see. See [gdocs.md](gdocs.md).

- The Google path additionally requires the Cloud Natural Language API to be enabled on the project. A configured path that does not exist is `auth_missing` rather than a silent fallback to ADC, so a typo surfaces immediately.
- **There is deliberately no `--api-key` flag for Firecrawl.** A secret on the command line lands in shell history and the process list. `text config list` fingerprints the key (`…017f`); `text config get firecrawl.api_key` returns it in full.
- `text research` uses the Firecrawl account but does **not** require a key — the index answers unauthenticated requests. A key only raises the rate limit.
