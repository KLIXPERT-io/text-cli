---
name: text-cli
description: Analyse and improve text from the command line with the `text` CLI — readability scoring (Flesch, Amstad, WSTF, LIX, Flesch-Kincaid, Gunning Fog, SMOG, Coleman-Liau, ARI), a lint engine that reports what to fix with exact byte offsets, before/after diffing of drafts, named-entity extraction with salience and Wikipedia identifiers, sentiment, content classification, Wikipedia lookups, reading a PDF, Word, PowerPoint, OpenDocument, EPUB or RTF file directly without converting it first, turning any of those into markdown on stdout or into a .md file, scraping any web page to clean prose, working with Google Docs (reading a document, reading and answering its comments, replacing or inserting text), and scientific-literature search. Use whenever the task involves how readable, hard, or complex a piece of writing is, making a text simpler or clearer, comparing drafts, checking prose against a reading level in CI, counting words/sentences/syllables, pulling people, places, organizations and products out of text, ranking what a document or a corpus is about, how a text feels, what category it belongs to, looking a named thing up in an encyclopedia, getting the text or markdown out of a document file, converting a PDF or Word file to markdown, reading or analysing a web page by URL, or finding scientific papers on a topic. Triggers include "how readable is this", "reading level", "Flesch score", "Lesbarkeit", "Lesbarkeitsindex", "simplify this text", "make this easier to read", "is this too hard to read", "rewrite this more clearly", "Passiv vermeiden", "Behördendeutsch", "Substantivstil", "lint my prose", "did my rewrite help", "compare these two drafts", "extract entities", "who and what is mentioned", "named entities", "what is this text about", "main topics", "salience", "sentiment of this review", "is this positive or negative", "classify this article", "look this up on Wikipedia", "word count of this article", "how readable is this PDF", "analyse this Word document", "lint this docx", "extract the text from this PDF", "what does this deck say", "convert this PDF to markdown", "turn this docx into markdown", "pdf to markdown", "docx to md", "save this document as markdown", "read this PDF to me", "what is in this file", "read this URL", "scrape this page", "analyse this web page", "how readable is this article <url>", "read this Google Doc", "what are the comments on this doc", "reply to this comment", "resolve this comment", "fix this in the doc", "replace this text in the document", "add a paragraph to the doc", "review this Google Doc", "find papers about", "what does the research say", "related work", "cite a paper".
---

# text — text analysis CLI

`text` reads prose from stdin, a file, an argument, or a URL and prints structured data. It never writes anything but its output. A file may be a PDF, DOCX, PPTX, ODT, ODS, EPUB or RTF — pass it to `--file` directly and **never convert it first**.

## Read the one file that matches the task

This page is the map plus the contract that holds for every command. Each capability has its own reference. **Read the one the task needs — not all of them.**

| the task is… | commands | read |
|---|---|---|
| how readable / how hard is this? compare two drafts, gate a reading level in CI | `readability`, `metrics`, `diff` | [references/scoring.md](references/scoring.md) |
| **make this text better** — what to change and exactly where | `lint` | [references/lint.md](references/lint.md) |
| who and what is mentioned, how it feels, what category it is | `entities`, `sentiment`, `classify` | [references/entities.md](references/entities.md) |
| what *is* this named thing | `kb` | [references/knowledge.md](references/knowledge.md) |
| read or analyse a web page | `fetch`, `--url` | [references/web.md](references/web.md) |
| analyse a PDF, Word, PowerPoint, ODF, EPUB or RTF file | any command, `--file`, `--from` | [references/io.md](references/io.md) |
| **turn a document into markdown** — read it, save it, hand it on | `extract` | [references/io.md](references/io.md) |
| **read, review, or edit a Google Doc** — including its comments | `docs`, `--url` | [references/gdocs.md](references/gdocs.md) |
| what has been published about this | `research` | [references/research.md](references/research.md) |
| output formats, batching, errors, exit codes, credentials | *all* | [references/io.md](references/io.md) |

Full command list: `readability`, `lint` (+ `lint rules`), `diff`, `metrics list|show`, `entities`, `sentiment`, `classify`, `kb lookup|search`, `extract`, `fetch`, `docs read|comments|comment|reply|replace|insert|whoami`, `research papers|paper|similar`, `config`, `update`.

## What each command costs

Check this before reaching for one — two of these groups spend money and four do not.

| commands | cost | credential |
|---|---|---|
| `readability`, `lint`, `diff`, `metrics` | free, local, offline | none |
| `kb`, `research` | network only | none |
| `entities`, `sentiment`, `classify` | **billed per request** (Google Cloud Natural Language) | service account |
| `fetch`, `--url` on a web page | **billed per page** (Firecrawl), cached 24h | `FIRECRAWL_API_KEY` |
| `docs`, `--url` on a Google Doc | free, never cached, **and the only one that writes** | service account, plus the document shared with it |

A readability or lint question never needs a paid command. Analysing one fetched page three different ways costs one scrape, not three. Decoding a `--file` document is free, local, and offline whatever the command — only what the command then does with the text can cost.

**`text docs` writes.** Everything else in this CLI prints and changes nothing; `docs replace`, `docs insert`, `docs comment`, and `docs reply` change somebody's document. Treat them as you would a commit: act on a specific instruction, use `--dry-run` when you did not read the target yourself, and never widen a replacement with `--all` to make an ambiguous match go away.

## True of every command

These five hold everywhere; the details are in [references/io.md](references/io.md).

1. **Input precedence is fixed:** `--url` → `--file` (repeatable; `-` is stdin) → positional args → piped stdin. A terminal stdin with no arguments is an `empty_input` error, never a hang. `--file` decodes PDF/DOCX/PPTX/ODT/ODS/EPUB/RTF itself; a binary it cannot read is refused with `invalid_args` rather than scored as prose.
2. **Markup is stripped to prose first** (`--strip auto`). A fenced code block never counts as a sentence. Leave this alone — turning it off on a Markdown file produces a wrong number, not a "truer" one.
3. **stdout is one envelope:** `{"data": …, "meta": {…}}`. `meta.language_detected: true` means the language was guessed; `meta.cached` means no call was made.
4. **Errors are one JSON line on stderr**, in every format, with a machine-readable `code` and a `hint` that names the command which fixes it. Exit codes are grouped by class (2 = auth, 3 = quota, 4 = not found, 5 = bad input, 6 = network/provider).
5. **Paid results are cached** on the provider's inputs, not on your filters. Changing `--top`, `--types`, or `--output` re-renders a cached answer instead of paying again.

## Rules of thumb that cross every feature

- **"Make this better" is a `lint` job, not a `readability` job.** A score says how bad; only lint says what to change. The loop is `lint` → edit at the byte offsets → `diff` to prove it worked.
- **Always report the `level` label next to a number.** Half the metrics get easier as they rise and half as they fall; the raw score alone is easy to invert. See the direction split in [references/scoring.md](references/scoring.md).
- **Set `--lang` when you know the language.** Detection is a heuristic, and `meta.language_detected` tells you when it was used.
- **Prefer one batched call over a loop.** Build JSONL and pass `--input-format jsonl --output ndjson`, or repeat `--file` for a set of documents; see [references/io.md](references/io.md).
- **Never convert a document before passing it in.** No `pdftotext`, no `pandoc`, no unzipping a `.docx`. `--file report.pdf` decodes it to markdown itself, which is what makes the score match the text pasted in by hand; a flattened conversion scores differently. Two refusals are final: a scanned PDF is `empty_input` and needs OCR this CLI does not do, and a pre-2007 `.doc`/`.xls`/`.ppt` is `invalid_args` and needs re-saving as `.docx`. In both cases say so and stop — do not retry and do not report a score.
- **`--output toon` when the result goes into a prompt, `--output json` when it goes into `jq`.** Nothing in the shell toolchain parses TOON.
- **On exit 5, read the hint and run the discovery command** (`text metrics list`, `text lint rules`) before retrying. On exit 2, stop and tell the user which credential is missing — retrying cannot fix it.
- **Never invent a value the CLI would have given you** — a metric name, a rule name, a paper id, a Wikipedia title. Get it from the relevant discovery or search command.
- **A Google Docs URL works in every command.** `text lint --url <doc-url>` is the way to review a document; there is no need to pipe `docs read`. See [references/gdocs.md](references/gdocs.md).
- **`not_found` on a Google Doc means "not shared yet", not "wrong id".** Google returns 404 for a document the account cannot see. Run `text docs whoami`, tell the user to share the document with that address, and stop — retrying cannot fix it.
- **Large batches: write NDJSON to a file and query it with `jq`.** Do not read every line into context.
