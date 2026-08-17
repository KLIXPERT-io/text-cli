# Research — scientific literature

```bash
text research papers <query...>   # search the index
text research paper <id>          # one paper, optionally with passages
text research similar <id>        # related work
```

Searches arXiv, PubMed, and DOI-addressed literature behind one identifier scheme.

**Network but no credentials, and no billing** — the index answers unauthenticated requests. A Firecrawl key only raises the rate limit. Cached 24h.

This answers "what has been published about X?". For "what *is* X?", read [knowledge.md](knowledge.md).

---

## Identifiers are namespaced — never invent one

`arxiv:1706.03762` · `doi:10.1145/3442188` · `pmid:18027780` · `pmcid:PMC1431743`

**A bare number is an `invalid_args` error (exit 5), not a guess** — `1706.03762` is as plausibly a PMID as an arXiv id, and guessing would be wrong about half the time.

Always take an id from the `id` / `primary_id` field of a search result. Never construct one, and never cite a DOI you did not get back from the tool.

`url` is a landing page derived from the identifier. For a namespace the CLI does not recognise it is empty rather than guessed — a broken link in cited output is worse than no link.

---

## Output shape

A paper: `id`, `primary_id`, `ids` (every external identifier, keyed by namespace), `title`, `abstract`, `authors`, `categories`, `published`, `updated`, `score`, `url`.

- **`authors` is one string, not a list.** Splitting it would invent a structure the index does not guarantee. Split on `", "` yourself if you need names.
- **`published` / `updated` are strings, verbatim.** The formats are not uniform across the indexes behind one source — an RFC 1123 arXiv timestamp can sit next to a bare `YYYY-MM-DD`. Do not assume a parse.
- **`score` is comparable within one result set and meaningless across two.** Never threshold on it, and never compare a `papers` score to a `similar` score — they are different scales.
- Search hits carry `title`, `abstract`, and `score`; `authors`, `categories`, and dates are filled in by `paper`, not by `papers`.

---

## papers — search

**The query is embedded, not keyword-matched.** A question retrieves better than a keyword list: `"how is text readability measured?"` beats `"readability metrics"`.

| flag | default | meaning |
|---|---|---|
| `--limit <n>` | 10 | capped at 100 |
| `--authors <substring>` | — | filter by a substring of the byline |
| `--categories <cat>` | — | subject taxonomy, e.g. `cs.LG` |
| `--from` / `--to` | — | publication date bounds, `YYYY-MM-DD` |

---

## paper — read one

Without `--query` you get the record alone. **With `--query "<question>"` you also get the passages of the full text most relevant to that question** — this is what makes it usable as a *source* rather than just a citation.

`data`: `paper` plus `passages[]` (`text`, `score`).

`--limit` controls the passage count and is only meaningful alongside `--query`.

---

## similar — related work

**`--intent` is required, and it is the point of the command.** "Papers like this one" is ambiguous until you say what makes them alike — the method, the application, or the dataset. The ranking is built from that sentence, so write a real one.

`--relation` picks the direction:

| value | meaning |
|---|---|
| `similar` (default) | work on the same subject, by content |
| `citers` | later work citing the seed — reading forward in time |
| `references` | the seed's own bibliography — reading backward |

---

## The workflow that actually answers a question

For "what does the research say about X":

1. `text research papers "<the question>"` — find candidates.
2. `text research paper <primary_id> --query "<the same question>"` on the best two or three — get passages that actually address it.
3. Quote the abstract or a passage, and **cite the `url`**.

Do not answer from titles alone, and do not quote an abstract as though it were a finding from the full text.

---

## Composing — abstracts are prose

```bash
# How readable is the literature on readability?
text research papers "readability formulas" --limit 20 --output ndjson \
  | jq -r .abstract | text readability --metrics flesch --output csv

# What entities does a field talk about?
text research papers "sepsis biomarkers" --limit 25 --output ndjson \
  | jq -r .abstract | text entities --input-format lines --aggregate --top 20

# A reading list, as a spreadsheet
text research papers "diffusion image synthesis" --limit 20 --categories cs.LG --output csv

# Papers from one year
text research papers "attention" --from 2017-01-01 --to 2018-01-01

# Forward from a seminal paper
text research similar arxiv:1706.03762 --intent "vision applications" --relation citers
```
