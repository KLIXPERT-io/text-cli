# Knowledge base — what *is* this thing?

```bash
text kb lookup [title...]    # alias: knowledge
text kb search <query...>
```

Wikipedia. **Network but no credentials, and no billing.** Cached 24h.

This answers "what is this named thing?". For "what has been published about it?", read [research.md](research.md). For pulling the names out of a document in the first place, read [entities.md](entities.md).

---

## lookup

Takes titles as arguments, or one per line on stdin — so it sits at the end of a pipe.

An article: `title` (the **resolved** one — a redirect lands on the canonical page), `description`, `extract` (the lead paragraph, plain text), `url`, `lang`, `thumbnail_url`, `aliases`, `disambiguation`.

**Check `disambiguation`.** `true` means the title resolved to an "X may refer to:" page, not a thing — its `extract` is a list of unrelated items and quoting it would be wrong. Treat it as a miss and run `text kb search` for a better title.

### Misses behave differently in a batch than alone

- **In a batch**, a missing title is reported on stderr and left out of the results. A list of entity names always contains a few things no encyclopedia has, and throwing the whole batch away for one of them would be the wrong trade.
- **A single missing title is a `not_found` error (exit 4)**, so a scripted one-shot lookup still fails loudly instead of printing nothing.

A rate limit or a network failure aborts even a batch, because it would repeat for every remaining title.

### Language

`--lang de` reads `de.wikipedia.org`. Unlike the analysis commands there is nothing to detect from — a title is not a document — so `auto` resolves to `en` rather than meaning "figure it out".

Titles are case-sensitive after the first character (`iPhone`, `eBay`). Spaces and underscores are equivalent.

---

## search

The recovery path for a failed lookup. **Article titles are exact**: "Ada Lovelace" is a page, "ada lovelace biography" is not.

Returns candidates best-first: `title`, `description`, `url`, `score`. `--limit <n>` (default 10).

`score` is usually 0 — Wikipedia's search API stopped reporting it, so **rank by list position**, not by that field.

---

## Composing with entities

Two ways, and the second is better:

```bash
# Pipe: works, but resolves titles by name
text entities --file post.md --top 10 --output ndjson | jq -r .name | text kb lookup

# Better: --enrich uses the wikipedia_url the entity provider already returned,
# so it gets the right article AND the right language edition
text entities --file post.md --top 10 --enrich
```

The pipe looks up "Paris" and may land on the wrong Paris. `--enrich` follows the identifier the provider resolved, which is unambiguous. Prefer `--enrich` unless you specifically want a name-based lookup.

---

## Flags

`--source <name>` (default `wikipedia`), `--lang <code>`, `--limit <n>` (search only), `--timeout <duration>`.

---

## Recipes

```bash
# One thing
text kb lookup "Ada Lovelace"

# Several, as a table
text kb lookup "Ada Lovelace" "Charles Babbage" --output table

# German Wikipedia
text kb lookup --lang de "Große Koalition"

# I do not know the exact title
text kb search "analytical engine" --limit 5

# Full article text for a briefing
text kb lookup "Ada Lovelace" --output text
```
