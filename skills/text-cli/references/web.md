# Web pages — `fetch` and `--url`

```bash
text fetch <url...>              # aliases: scrape, read
text readability --url <url>     # or entities, lint, sentiment, classify — any command
```

Turns a URL into the prose on the page: navigation, cookie banners, and script tags removed, the body returned as markdown.

**Billed per page** (Firecrawl), but **cached 24h** — analysing one page three different ways costs one scrape. Needs `FIRECRAWL_API_KEY` or config `firecrawl.api_key`; see [io.md](io.md).

---

## `--url` is an input source, not a separate workflow

**Prefer `--url` over piping `fetch`.** These do the same work:

```bash
text fetch https://example.com/post --output text | text entities   # two processes
text entities --url https://example.com/post                        # better
```

The second is one command, hits the same cache, and gives the document **the URL as its `id`** instead of `0` — which matters as soon as there is more than one page.

`--url` is repeatable and sits at the top of the input precedence list, ahead of `--file` and stdin. The fetched markdown then goes through the normal strip pass, so a fetched page and a saved copy of the same page produce **identical scores and identical byte offsets**.

---

## Output shape

A page: `url` (after redirects), `requested_url` (present **only when it differs**), `title`, `description`, `language`, `content` (markdown), `links` (with `--links`), `status_code`, `fetcher`, `credits`.

- **`language` is the page's _declared_ language** — an HTML `lang` attribute or meta tag. It is a hint, not an answer: pages that declare `en` and are written in German are common. The analysis commands still detect from the text.
- **Check `status_code`.** A scrape can succeed at fetching a 404 page. The fetch worked; the page is an error page. `status_code` is the page's, not the API's.
- **`--output text` prints only the page content**, which is what makes `text fetch X --output text | text …` safe. Every other format wraps it in the `{data, meta}` envelope.

---

## Flags

| flag | default | meaning |
|---|---|---|
| `--url <url>` | — | repeatable; available on every command |
| `--links` | off | include the page's outbound links (`fetch` only) |
| `--main-content` | on | drop nav, headers, and footers |
| `--fetcher <name>` | `firecrawl` | which backend; a Google Docs URL routes to `gdocs` unless this names one |
| `--timeout <duration>` | 60s | per-page deadline |
| `--refresh` | off | bypass **both** caches — this CLI's and Firecrawl's |

---

## A Google Docs URL is not a scrape

`docs.google.com/document/...` routes automatically to the Google Docs backend: free, never cached, and needing a service account rather than a Firecrawl key. Everything on this page still applies — `--url`, the strip pass, the envelope — but the credential, the cost, and the failure modes are different, and the document can also be edited. See [gdocs.md](gdocs.md).

---

## Failure modes

- **A page with no text is `empty_input` (exit 5) with a hint**, never an empty result that a later command reports as a mystery. If the hint suggests `--main-content=false`, the boilerplate extractor probably ate the body — retry with it. Otherwise the page is likely login-walled or renders no text at all.
- **In a multi-URL batch, a dead link is a stderr warning** and the other pages still return. But a missing API key, a bad key, a rate limit, or exhausted credits **aborts the whole run**, because it would repeat for every remaining URL.
- Multiple URLs are fetched concurrently, four at a time.
- A non-`http(s)` scheme is rejected locally as `invalid_args` before any request is made. A bare host gains `https://`.

---

## Cost discipline

- **Do not re-fetch a page you already fetched in this session.** It is cached 24h; a second command against the same URL is free.
- **Do not add `--refresh` unless the page is expected to have changed.** It bypasses Firecrawl's cache too, so it is a real, billed scrape every time.
- The cache key covers the URL and the options that change the returned text — not your output format or filters. Switching `--output` never re-scrapes.

---

## Recipes

```bash
# The page, as prose
text fetch https://example.com/post --output text

# With its outbound links
text fetch https://example.com/post --links

# Analyse directly — no pipe, no temp file
text readability --url https://example.com/post --lang de
text lint --url https://example.com/post --output text
text entities --url https://example.com/post --min-salience 0.02

# Several pages; each row carries its URL as the id
text readability --url https://a.com/x --url https://b.com/y --output csv
text fetch https://a.com/x https://b.com/y --output ndjson

# A page that came back empty
text fetch https://example.com/app --main-content=false
```
