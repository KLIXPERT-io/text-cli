# Lint — how to actually improve a text

```bash
text lint [text...]
text lint rules          # every registered rule, with languages and severity
```

**This is the command to reach for when the user wants the text made better, not just scored.** `readability` says a document is hard. `lint` says which sentence, which phrase, and what to write instead.

Free, local, offline. No credentials, no network, `meta.api_calls` is always 0.

Shared input/output rules are in [io.md](io.md); to prove a rewrite worked afterwards, see `diff` in [scoring.md](scoring.md).

---

## Output shape

`data` per document: `id`, `language`, `language_detected`, `findings[]`, `summary` (rule name → count), `total`. A batch is a list of those under `data.documents`.

Each finding:

| field | meaning |
|---|---|
| `rule` | which check fired |
| `severity` | `info` or `warn` |
| `message` | what is wrong |
| `suggestion` | what to write instead (when the rule has one) |
| `sentence` | 1-based sentence index; `0` for a document-level finding |
| `start`, `end` | **byte offsets** into the analysed text |
| `excerpt` | exactly `text[start:end]` |
| `value` | the number the rule measured, when it measured one |

---

## The byte offsets are the point

**`start` and `end` are byte offsets into the analysed text, and `excerpt` is exactly `text[start:end]`.**

This is the mechanism for precise editing: slice the source at the offsets, replace the span, done. No searching, no ambiguity, no risk of hitting the wrong occurrence of a repeated phrase.

**Apply edits back-to-front — highest `start` first — so earlier offsets stay valid.**

Two caveats that will bite you otherwise:

1. **The offsets index the _stripped_ text**, which is what was analysed. If the source is Markdown, either apply edits against the stripped text, or re-run with `--strip none` when you must patch the raw file. (See stripping in [io.md](io.md).)
2. **`text[start:end] == excerpt` holds exactly in `json`, `toon`, and `ndjson`** — the machine-facing formats. `csv`, `table`, and `text` shorten `excerpt` to 80 characters for display. The offsets are exact in every format; only the displayed excerpt is truncated. `--output text` also prefixes findings with `L<n>`, the **line** number, not the sentence index.

Offsets are bytes, not runes. German matters here: `ä` is two bytes. Slice with a byte-aware operation.

---

## Flags

| flag | default | meaning |
|---|---|---|
| `--rules auto\|all\|<names>` | `auto` | `auto` = every rule registered for the document's language |
| `--severity info\|warn` | `info` | minimum severity to report |
| `--worst <n>` | 5 | how many hard sentences to report |
| `--max-sentence-words <n>` | 25 | threshold for `long-sentence` |
| `--max-word-chars <n>` | 20 | threshold for `long-word` |
| `--fail-on-findings` | off | exit 1 if anything was reported (see gates in [scoring.md](scoring.md)) |

---

## Rules

Run `text lint rules` for the live list — rules are added over time, and on an unknown-rule error that command is the fix. Today:

| scope | rules |
|---|---|
| any language | `hard-sentence`, `long-sentence`, `long-word`, `repeated-word`, `repeated-sentence-start`, `sentence-length-variance` |
| German | `passive` (Passiv), `nominalization` (Substantivstil), `bureaucratic` (Behördendeutsch), `filler` (Füllwörter), `modal-hedge` (Konjunktiv-Häufung) |
| English | `passive`, `nominalization`, `filler`, `hedge`, `adverb` |

**A rule name may have per-language variants.** `passive` means the German detector on German text and the English one on English text; the summary key stays `passive` either way. German messages and suggestions are written in German on purpose — that is the vocabulary the style guides use.

---

## The rewrite loop

```bash
text lint --file doc.md --output text        # 1. see what to fix, with line numbers
text lint --file doc.md --severity warn      # 2. or only what matters, as JSON with offsets
#    apply the edits at the byte offsets, back-to-front
text diff doc.orig.md doc.md                 # 3. prove it worked
```

More recipes:

```bash
# The ten worst spans as offsets an editor can apply
text lint --file post.md --output json \
  | jq -c '.data.findings | sort_by(-.value) | .[:10] | .[] | {start, end, excerpt, suggestion}'

# One finding per line, for a script
text lint --file post.md --output ndjson

# Only German passive and bureaucratic language
text lint --file post.de.md --rules passive,bureaucratic --severity warn

# Gate a build on it
text lint --file post.md --severity warn --fail-on-findings
```
