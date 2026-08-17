# Entities, sentiment, classification

```bash
text entities [text...]    # alias: ents  — who and what is mentioned
text sentiment [text...]   # alias: sent  — how it feels
text classify [text...]    # alias: cls   — what category it is
```

These three are one unit: same backend (Google Cloud Natural Language **v1**), same credential chain, same cache, same billing. **They are the only commands that spend money on text you already have** — reach for them only when the task actually needs them. A readability or lint question never does.

Credentials and exit codes are in [io.md](io.md). To attach encyclopedia context to the entities, see [knowledge.md](knowledge.md).

Results are cached per `(provider, language, text)`, so re-running the same document costs nothing and changing a filter re-renders instead of re-billing. `--refresh` forces a fresh call.

---

## entities

`data` for a single document: `id`, `provider`, `language`, `language_supported`, `entities[]`. A batch is a list of those under `data.documents`.

Each entity: `name`, `type`, `salience`, `probability`, `mention_count`, `mentions[]`, `metadata`, and — where the provider knows them — `wikipedia_url` and `mid`.

Types: `PERSON`, `LOCATION`, `ORGANIZATION`, `EVENT`, `WORK_OF_ART`, `CONSUMER_GOOD`, `NUMBER`, `DATE`, `ADDRESS`, `PHONE_NUMBER`, `PRICE`, `OTHER`, `UNKNOWN`. `--types person,organization` is case-insensitive.

### Salience is the ranking signal — and the thresholds are small

- **`salience` is how central the entity is to its document**, in `[0, 1]`. Within one document the salience of all entities sums to about **1.0**. It is a *share of attention*, not a confidence: 0.4 means "this document is largely about this", never "we are 40% sure".
- **Useful thresholds are therefore 0.01–0.1.** 0.05 is already a strong entity. `--min-salience 0.5` or `0.8` returns almost nothing — if you are carrying such a value over from an older script, it is wrong now.
- **`probability` is `0` for every Google entity.** v1 has no per-mention probability. Never rank, filter, or report on it for this backend. The field stays in the contract for backends that do report it.
- `--min-salience` is the canonical flag; `--min-probability` is an alias for the same threshold, kept for old scripts. An explicit `--min-salience` wins when both are given.
- **`language_supported` is always `true` here** — v1 rejects an unsupported language with an error rather than answering best-effort. Only treat `false` as meaningful for providers that report it.

### Flags

`--provider <name>`, `--service-account <key.json>`, `--types <CSV>`, `--min-salience <0..1>`, `--min-probability <0..1>` (alias), `--sort salience|mentions|name` (default `salience`), `--top <n>`, `--aggregate`, `--enrich`, `--timeout <duration>`.

### `--aggregate` — what is a whole corpus about?

Merges the same entity across every input document into one ranked list under `data.entities`, with `data.documents` as the count. Each row: `name`, `type`, `combined_salience`, `avg_salience`, `mentions`, `documents`, `wikipedia_url`, `mid`.

**`combined_salience` is the _sum_ of per-document salience**, so it is bounded by the number of documents, not by 1.0. Read it as "how many documents' worth of attention this owns".

The merge key is the case-folded name **plus** the type, so "Apple" the ORGANIZATION and "apple" the CONSUMER_GOOD stay separate.

### `--enrich`

Attaches a `knowledge` object (`source`, `title`, `description`, `extract`, `url`, `lang`, `disambiguation`) to every entity that has a `wikipedia_url`. It **never fails the command**: an entity with no article simply carries no `knowledge` key. Ignored with `--aggregate`.

Prefer it over piping into `kb lookup` — it uses the `wikipedia_url` the provider already returned, so it resolves the right article *and* the right language edition. See [knowledge.md](knowledge.md).

### Filter composition — this order is why filters are free

1. `--types` and the salience threshold apply **per document, before** any merge.
2. `--sort` and `--top` apply **after** it.
3. `--enrich` runs **after** filtering, so no lookup is paid for an entity `--top` discarded.

All of it happens after the cache, so tightening a filter never costs another API call.

### Flat formats

`--output ndjson` emits one entity per line, tagged with `doc_id` and `provider`, plus any passthrough JSONL fields. `--output csv` gives `doc_id,name,type,salience,probability,mentions,wikipedia_url`; with `--aggregate`, `name,type,combined_salience,avg_salience,mentions,documents,wikipedia_url`.

---

## sentiment

`data` per document: `id`, `provider`, `language`, `score`, `magnitude`, `label`, `sentences[]`.

- **`score`** is −1.0 to +1.0: which way the text leans. It is an *average*, so opposing parts cancel out.
- **`magnitude`** is 0 to +inf: how much feeling there is in total, in either direction. Not normalized and it grows with length, so it is only comparable between documents of similar size.
- **`label`** is derived from both.

**Report the pair, not just the score.** A near-zero score with high magnitude is `mixed` (half furious, half delighted); a near-zero score with low magnitude is `neutral` (no feeling at all). Those mean opposite things and the score alone cannot tell them apart.

`--sentences` is on by default and adds the per-sentence breakdown to JSON. Passing it *explicitly* also switches `csv`, `table`, and `text` to one row per sentence. The provider is always asked for sentences and the full answer is cached, so toggling it never costs another call.

---

## classify

`data` per document: `id`, `provider`, `language`, `categories[]` with `name` (a taxonomy path like `/Computers & Electronics/Software`) and `confidence`.

**Confidences are per category and independent** — unlike entity salience they do not sum to 1.0, because a document really can belong to several categories. Split `name` on `/` for the hierarchy.

**Needs roughly 20+ words.** Shorter input is rejected with `invalid_args` (exit 5) *before* the call is made — do not retry with the same text. Use `entities` or `sentiment` on short input instead.

Flags: `--top <n>`, `--min-confidence <0..1>`.

---

## Recipes

```bash
# The ten things this post is most about
text entities --file post.md --top 10 --min-salience 0.02

# People and organisations only, as a spreadsheet
text entities --file post.md --types PERSON,ORGANIZATION --output csv

# What is the whole corpus about? One ranked list across every document
jq -c '{id: .slug, text: .body}' posts.jsonl \
  | text entities --input-format jsonl --aggregate --top 20

# Entities plus their Wikipedia description, in one call
text entities --file post.md --top 10 --enrich

# Sentiment of a review, per sentence
text sentiment --file review.md --sentences --output table
```
