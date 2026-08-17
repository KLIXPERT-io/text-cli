# Scoring — readability, metrics, diff, CI gates

Measuring how hard a text is, comparing two drafts, and failing a build on either. These belong together because they share one thing: **every metric declares which direction is better**, and getting that wrong inverts every conclusion.

**This answers "how hard is it", never "what should I change".** For that, read [lint.md](lint.md). Shared input/output rules are in [io.md](io.md).

---

## readability

```bash
text readability [text...]        # aliases: read, rd, flesch, amstad
```

`data` for a single document is one object: `id`, `language`, `language_detected`, `stats`, `metrics[]`. A batch is a list of those under `data.documents`.

`stats`: `sentences`, `words`, `syllables`, `characters`, `polysyllabic_words`, `monosyllabic_words`, `long_words`, `avg_sentence_length`, `avg_syllables_per_word`, `avg_word_length`.

Each entry of `metrics[]`: `metric`, `title`, `score`, `level`, `grade`, `scale`, `language`, `extra` (the counts that produced the score).

| flag | meaning |
|---|---|
| `--metrics auto\|all\|<names>` | default `auto` — every metric valid for the document's language |
| `--stats` | on by default; `--stats=false` drops the token counts |
| `--fail-under <n>` / `--fail-over <n>` | CI gates, see below |

---

## The direction split — get this right or every number inverts

| direction | metrics | scale |
|---|---|---|
| **higher is easier** | `flesch` (en), `amstad` (de) | 0–100 reading ease |
| **lower is easier** | `wstf1`–`wstf4` (de), `lix` (any), `flesch-kincaid`, `gunning-fog`, `smog`, `coleman-liau`, `ari` (en) | school grade level |

A rising Flesch is an improvement. A rising Gunning Fog is a regression.

**Never summarise a grade-level score as "scored 12, quite good"** — 12 is twelfth grade, which is hard. Always report the `level` label next to the number; it already encodes the direction.

### The two reading-ease formulas are language-locked

- **English → `flesch`** (`206.835 − 1.015×ASL − 84.6×ASW`). Aliases `fre`, `flesch-reading-ease`.
- **German → `amstad`** (Amstad 1978, `180 − ASL − 58.5×ASW`). Aliases `flesch-de`, `flesch-amstad`, `fre-de`. It is *the German equivalent of Flesch* on the same 0–100 scale, so `level` labels are comparable across the two.

`--metrics flesch` on German text is an `unsupported_language` error (exit 5), **by design**: the English constants punish German compounds so hard that almost any German prose scores below zero. Use `--metrics auto` and let the language decide. Never quote a Flesch score for German or an Amstad score for English.

### Reading the labels

Level labels are domain vocabulary and stay in the document's language:

- German: `sehr leicht`, `leicht`, `mittelleicht`, `mittel`, `mittelschwer`, `schwer`, `sehr schwer`
- English: `very easy`, `easy`, `fairly easy`, `standard`, `fairly difficult`, `difficult`, `very confusing`

**WSTF reports raw formula output** and labels anything outside Schulstufe 4–15 `unter der Skala` / `über der Skala`. A computed 0.0 is not "sehr leicht" — it is off the scale, and must be reported as such.

**Scores are not clamped.** A negative reading-ease score is real and means the text is punishing.

---

## metrics — discover what exists

```bash
text metrics             # same as `text metrics list`
text metrics list
text metrics show <name> # resolves aliases
```

**Read the registry rather than assuming.** Metrics are added over time; `metrics list` reports each one's `languages` and `description`. On an `unknown_metric` error (exit 5), run this before retrying — never guess a metric name.

---

## diff — did the rewrite help?

```bash
text diff <before> <after>
```

Both arguments are file paths. `-` reads stdin for at most one of them, so a generated draft can be diffed against the original without a temp file.

`data`: `before` and `after` (each `id`, `language`, `language_detected`, `stats`), `metrics[]`, `stats_delta`.

Each metric row: `metric`, `title`, `before`, `after`, `delta`, `improved`, `direction` (`higher-is-easier` | `lower-is-easier`), `level_before`, `level_after`.

**Read `improved`, never the sign of `delta`.** `improved` is derived from the metric's declared direction, so a falling LIX and a rising Flesch are both `true`. This field exists precisely so you do not have to remember the direction table.

Flags: `--metrics`.

---

## CI gates

```bash
text readability --file post.md --metrics flesch --fail-under 60    # reading ease: at least
text readability --file post.de.md --metrics wstf1 --fail-over 10   # grade level: at most
text lint --file post.md --severity warn --fail-on-findings         # see lint.md
```

- **`--fail-under`** is for higher-is-easier metrics (a floor on reading ease).
- **`--fail-over`** is for lower-is-easier metrics (a ceiling on grade level).

Pointing one the wrong way at an explicitly named metric is an `invalid_args` error (exit 5) whose message names the flag you actually wanted. Under `--metrics auto` a gate applies to the metrics it fits and ignores the rest.

**A failed gate still prints the full result to stdout, then exits 1.** Read stdout for the numbers and stderr for the reason — do not treat the non-zero exit as "no output".

---

## Recipes

```bash
# Score a German post, all applicable metrics
text readability --file post.de.md --lang de

# One metric, no --metrics needed
text flesch --file post.md

# Prove a rewrite worked, one row per metric with the direction resolved
text diff draft1.md draft2.md --output table

# Diff a generated draft against the original without a temp file
generate-draft | text diff post.md -

# The hardest pages in a corpus
find content -name '*.md' -print0 \
  | xargs -0 -n1 sh -c 'jq -Rnc --arg id "$0" --rawfile t "$0" "{id: \$id, text: \$t}"' \
  | text readability --input-format jsonl --output ndjson \
  | jq -c 'select(.flesch < 40) | {id, flesch, flesch_level}'
```
