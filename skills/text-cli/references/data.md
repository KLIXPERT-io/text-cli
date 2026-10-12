# Data — records, not pages

```bash
text data find <query...>             # which tools fit, with their prices — free
text data tool <provider/capability>  # one tool: price, required inputs, options — free
text data run <provider/capability>   # run it — billed
```

Firecrawl's Alexandria catalogue: well over a thousand tools from a few hundred providers — company profiles and financials, people and their careers, public records (mostly US registries), economic series, news, podcasts, website traffic.

**`find` and `tool` are free. `run` spends Firecrawl credits** at the price the catalogue lists — usually 5. All three need `FIRECRAWL_API_KEY`. Answers are cached 24h.

This answers "what does a database hold on X?". For "what does this page say?", read [web.md](web.md); for "what has been published about X?", read [research.md](research.md).

---

## find — describe the data, not the entity

The catalogue matches by meaning, and loosely. Say what the record holds, in the words a database would use:

| query | finds |
|---|---|
| `"a person's profile and career history"` | the people tools |
| `"a company's profile from its domain"` | the company lookups |
| `"a listed company's income statement"` | the financials tools |
| `"Ada Example"` — a bare name | tools that search social posts for that string |
| `"current employer and job history"` | job boards, because that is what "employer" and "job" are about |

When the listing is not what you meant, reword it the way the record would be described — do not run the nearest tool.

| flag | meaning |
|---|---|
| `--site <url>` (repeatable) | the tools of the site that address is on — a LinkedIn profile, an X post |
| `--provider <id>` | one provider's tools, e.g. `--provider fred --limit 50` |
| `--limit <n>` | default 10, capped at 100 |
| `--detail` | include every tool's options (large) |

A tool: `id` (`provider/capability` — the form `tool` and `run` take), `name`, `description`, `credits`, `per_record`, `requires_one_of`, and `refused` when the CLI will not run it.

---

## tool — read it before you run it

`text data tool fullenrich/people/lookup` gives the price, the inputs the tool insists on (`requires_one_of` — one option from every group), every option with type, default and bounds, `returns` (what the answer holds) and `records_key` (where its records are).

**Never invent an option name or a tool id.** Take both from `tool` and `find`. A wrong option is `invalid_args` (exit 5) after a round trip; a guessed one that happens to exist is worse.

**Prefer an identifier to a name.** A profile URL, a company domain, a ticker resolve to one record. A name is shared by several people — a web search for one turned up a tax adviser, a footballer and the founder that was meant — and the tool answers for whichever it matched.

---

## run — the one that costs money

```bash
text data run fullenrich/people/lookup --opt person_professional_network_url=https://www.linkedin.com/in/someone
text data run fiscal-ai/financials/income-statement --opt ticker=MSFT --output text
text data run fred/series/search --options '{"search_text":"unemployment rate","limit":5}' --output ndjson
```

`--opt key=value` is read as JSON when it parses as JSON (`limit=3` is a number, `domains=["a.com"]` a list) and as text otherwise; quote a string that looks like a number: `--opt zip='"01234"'`. `--options '<json object>'` sets them all at once; `--opt` overrides its keys.

**The price is checked before anything is spent.** A tool listed above the ceiling — 30 credits unless `data.max_credits` says otherwise — is refused with `quota_exceeded` (exit 3) and nothing is spent. `--max-credits <price>` raises it for one run. **Do not raise it on your own:** name the tool, its price and what it would answer, and let whoever pays decide. A per-record price multiplies by what comes back — a search for six people at 5 per record is 30.

**`this environment caps a data run` is final.** `TEXT_DATA_CREDIT_CAP` is set where you run, and no flag can raise it. Say what the tool costs and stop.

**Contact data is refused** — tools that find a person's email address or phone number, and `reveal_*` options. `invalid_args`, nothing spent. Do not look for a tool that does it anyway.

| result | meaning | what you do |
|---|---|---|
| exit 0 | the answer, verbatim, in `data` | read it — see below |
| `not_found` (4) | the provider has nothing for these options; per-record tools charge nothing | check the spelling, or pass an identifier instead of a name |
| `quota_exceeded` (3) | above the ceiling or the cap — nothing spent; or the account is out of credits | report the price; never raise the ceiling unasked |
| `auth_denied` (2), "data terms" | an admin of the Firecrawl account has not accepted this provider's terms | tell the user the page in the hint; **never accept terms yourself** |
| retriable, 6 | timed out or failed upstream — it may still have been billed | rerun once with the `--request-id` the hint names: a completed first attempt is answered, not charged again |

`data`: `tool`, `options`, `credits`, `records`, `records_key`, `request_id`, and `data` — the provider's answer verbatim, in its own key order. With `--output ndjson` a tool that returns a list prints one record per line, split on `records_key`. `--output text` is the answer pretty-printed.

---

## What an answer is worth

**A data tool answers with the provider's record, not the company's or the person's own statement.** Providers assemble records from wherever they gather them; they go stale and are sometimes plainly wrong — one company record listed a sixteen-person agency as a public company. Use an answer as a lead, and as the source for a figure nobody else publishes; confirm anything that carries weight against a first-party source — the filing, the imprint, the person's own page.

**Cite the call, not a URL.** An answer has no address. Record the tool id, the options, the date and the `request_id` — that is what lets someone run the same question again.

**Never copy an email address or a phone number out of an answer**, even when a provider included one.

---

## Composing

```bash
# The people a search found, one per line
text data run fullenrich/people/search \
  --options '{"current_company_domains":["example.com"],"limit":3}' --output ndjson \
  | jq -r '[.full_name, .employment.current.title] | @tsv'

# Is the company's own description as readable as its record says it is big?
text data run fullenrich/companies/lookup --opt domain=example.com --output ndjson \
  | jq -r .description | text readability
```
