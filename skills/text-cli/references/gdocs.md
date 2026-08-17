# Google Docs — `text docs`

```bash
text docs whoami                     # the address a document must be shared with
text docs read <doc>                 # the document as markdown
text docs comments <doc>             # open review threads, with the quoted passage
text docs comment <doc> [text]       # post a comment (document-level)
text docs reply <doc> <id> [text]    # reply, optionally --resolve
text docs replace <doc> --find X --replace Y
text docs insert <doc> [text]        # append a paragraph
```

`<doc>` is a document URL or the bare id. **Google Docs only** — a Sheets, Slides, or Forms URL is rejected by name.

**Free** (no per-call billing) but **credentialed**: a service account key, and the document shared with it. **Nothing here is cached** — the document may be being edited right now.

**This is the only part of the CLI that writes.** Everything else prints and changes nothing.

---

## Before anything else: access

A service account cannot request access and has no consent screen. The **only** thing that grants it is a human sharing the document with its address.

```bash
text docs whoami            # → text-cli@project.iam.gserviceaccount.com
```

**Google answers a document you cannot see with 404, not 403.** So `not_found` (exit 4) from any `docs` command, or from `--url` on a doc, almost always means *not shared yet* — not a wrong id. The hint already carries the address. **Stop and tell the user to share the document with it** (Viewer to read, Editor to write); retrying cannot fix it, and neither can you.

Exit 2 (`auth_missing` / `auth_denied`) is the other stop-and-report case: no key, a dead key, or the Docs/Drive APIs not enabled on its project. The hint says which.

---

## `--url` is the same document, in every other command

A Google Docs URL routes to this backend automatically, so **prefer `--url` over piping `docs read`**:

```bash
text lint --url <doc-url>            # what to fix, at byte offsets
text readability --url <doc-url> --lang de
text diff --url <doc-before> --url <doc-after>
```

The document arrives as markdown and goes through the normal strip pass, so a doc and a saved copy of it score identically. `docs read --output text` exists for the pipe, but `--url` is one process and keeps the document id in the output.

---

## The loop this exists for

`lint.excerpt` is *exactly* the source text at the offsets it reports, and a comment's `quoted` is a literal substring of the document. Both are therefore valid `--find` strings, and applying a fix needs no index arithmetic:

```bash
# 1. what to fix — from the linter, or from the humans
text lint --url <doc-url> --output ndjson | jq -r 'select(.severity=="warn") | .excerpt'
text docs comments <doc-url> --output ndjson | jq -r '{id, quoted, content}'

# 2. fix it (dry run first if the string might be ambiguous)
text docs replace <doc-url> --find "<excerpt>" --replace "<rewrite>" --dry-run
text docs replace <doc-url> --find "<excerpt>" --replace "<rewrite>"

# 3. prove it helped, and close the thread
text diff --url <doc-url> <(cat original.md)      # or diff before/after drafts
text docs reply <doc-url> <comment-id> "Umformuliert — Amstad 31 → 58." --resolve
```

---

## Rules for writing

- **Never invent a `--find` string.** Take it from `lint.excerpt`, from a comment's `quoted`, or from `docs read` output. A guessed string fails on autocorrect alone: the document may hold a curly quote or an em dash where your source had a straight one.
- **An ambiguous match is refused** (`invalid_args`, exit 5) with the count in the message. Do **not** reach for `--all` — extend `--find` with surrounding words until it is unique. `--all` is for a genuine global rename.
- **No match is `not_found`** (exit 4), never a silent no-op. If you get it, re-read the document rather than retrying with a variation.
- **`--find` cannot span a paragraph break.** Replace one paragraph's text at a time.
- **`--replace` must be given explicitly**, including `--replace ""` to delete.
- **Every write pins the revision it read.** If someone edited the document in between, the write is refused with a retriable `invalid_args` — re-run the command, do not force it.
- **`--dry-run`** on `replace` and `insert` validates and counts without writing. Use it when you are acting on something you did not read yourself.
- **Insert is verbatim.** Markdown is not converted to Docs formatting; `## Rollout` arrives as two hashes and a space. `--at end` (default) appends a new paragraph, `--at start` prepends one, `--inline` joins the neighbouring paragraph.
- **A multi-tab document refuses to guess** on insert: pass `--tab <id>` (ids come from `docs read`).

---

## Comments

`docs comments` returns, per thread: `id`, `author`, `content`, `quoted`, `resolved`, `replies[]`, `reply_count`, timestamps.

- **`quoted` is the point** — the passage the reviewer selected, verbatim.
- **Resolved threads are excluded by default**; `--include-resolved` includes them.
- **`docs reply --resolve` is one call** — the message and the state change together. `--reopen` is the inverse.
- **A new comment cannot be anchored** to a passage (no API can mint the anchor). `docs comment --quote "<passage>" --text "<note>"` quotes it into the comment body instead. Say so if the user expected a margin anchor.

---

## Flags

| flag | where | meaning |
|---|---|---|
| `--service-account <path>` | all | key file (overrides config and env) |
| `--timeout <duration>` | all | per-request deadline (default 30s) |
| `--tab <id>` | `read`, `replace`, `insert` | one tab; required for `insert` on a multi-tab doc |
| `--suggestions clean\|accepted\|inline` | `read` | default `clean`: pending suggestions left out |
| `--include-resolved`, `--limit` | `comments` | |
| `--quote`, `--text` | `comment` | `--text -` reads stdin |
| `--resolve`, `--reopen`, `--text` | `reply` | |
| `--find`, `--replace`, `--all`, `--match-case`, `--dry-run` | `replace` | |
| `--at end\|start\|<index>`, `--inline`, `--text`, `--dry-run` | `insert` | |

---

## What a document loses on the way in

Say so rather than assuming the text is complete:

- Images, equations, footnote markers, and page numbers contribute **no prose**.
- A table of contents is **skipped** (its headings are already in the text).
- Tables become markdown pipe tables; the first row is treated as the header.
- Pending suggestions are excluded by default — `--suggestions accepted` reads the document as if all were accepted.
- Multi-tab documents are concatenated with each tab's title as an `#` heading.
