# Web tools

`web_search` and `web_fetch` let the agent look things up on its own — current
library versions, an API reference, a changelog, or the meaning of an error message
— instead of guessing from stale training data. Both tools are available to
[subagents](tools.md#task-delegation--parallel-subagents) too.

## `web_search`

Returns titles, URLs, and snippets. The default backend is **DuckDuckGo, which
needs no API key** (a best-effort HTML scrape).

### Switching engines

From inside a session, open **`/config`** and pick **Web search engine**. The
change applies to the running session — including subagents, which share the same
backend — and is saved to the global config, so it survives a restart. Selecting an
API backend whose credentials are not reachable is refused up front, naming what is
missing, rather than leaving you with a config whose every search fails.

Or edit the config file directly:

```jsonc
{ "web_search": { "provider": "bing-cn" } }   // keyless
{ "web_search": { "provider": "brave", "env_key": "BRAVE_API_KEY" } }
{ "web_search": { "provider": "tavily", "api_key": "tvly-..." } }
```

| `provider` | Key | Notes |
|---|---|---|
| `duckduckgo` *(default)* | — | Scrapes the no-JS HTML endpoint |
| `bing` | — | `www.bing.com`, English-weighted |
| `bing-cn` | — | `cn.bing.com`, reachable from the mainland, ranks Chinese queries better |
| `baidu` | — | Best Chinese coverage, but blocks aggressively — see below |
| `yahoo` | — | `search.yahoo.com` |
| `google` | `GOOGLE_API_KEY` + engine ID | Official API only — see below |
| `brave` | `BRAVE_API_KEY` | Official API |
| `tavily` | `TAVILY_API_KEY` | Official API |

Names are matched loosely, so `cn.bing.com`, `bing_cn`, `百度`, and `BAIDU` all
resolve; an unrecognized name falls back to DuckDuckGo.

| Field | Meaning |
|---|---|
| `provider` | Backend name from the table above (config key: `web_search_provider`) |
| `api_key` | Inline key (avoid — prefer `env_key`) |
| `env_key` | Name of the env var holding the key |
| `engine_id` | Google's Programmable Search engine ID (`cx`); ignored by the others |

Google/Brave/Tavily read the key from `api_key`, the configured `env_key`, or the
standard `GOOGLE_API_KEY` / `BRAVE_API_KEY` / `TAVILY_API_KEY` variable. The keyless
engines ignore all of them.

### Google

Google has **no keyless path**: its result page is rendered by JavaScript and
contains no results in the HTML, and the old no-JS SERP now answers *"update your
browser"*. So this backend uses the official **Programmable Search (Custom Search
JSON) API**, which needs two values — a key, and an engine ID (`cx`) naming a
search engine you create:

1. Create an engine at <https://programmablesearchengine.google.com/> and turn on
   **"Search the entire web"** (otherwise it only searches the sites you list).
   Copy its **Search engine ID**.
2. Enable the **Custom Search API** in a Google Cloud project and create an API key.

```jsonc
{ "web_search": { "provider": "google", "engine_id": "a1b2c3d4e5f6g7h8i" } }
```

| Value | Config field | Environment |
|---|---|---|
| API key | `api_key` / `env_key` | `GOOGLE_API_KEY`, `GOOGLE_SEARCH_API_KEY` |
| Engine ID | `engine_id` | `GOOGLE_SEARCH_ENGINE_ID`, `GOOGLE_CSE_ID` |

> **Free tier is 100 queries/day.** Past that the API returns 429 and the tool
> reports Google's own message ("Quota exceeded…"), so the cause is visible rather
> than looking like an empty web.

### On the keyless engines

They scrape a result page, so treat them as best-effort:

- **Baidu** serves a captcha to anything that does not look like a browser. The
  backend sends browser headers and picks up the session cookie from Baidu's home
  page, retrying once — but Baidu still rate-limits by IP after a handful of
  queries. When that happens the tool reports *"served a bot check"* rather than
  pretending the web is empty, and you should switch providers.
- **Baidu snippets** come from embedded JSON, and its result URLs stay as
  `baidu.com/link?url=…` redirects — `web_fetch` follows them to the real page.
- Any engine can change its markup and quietly return nothing. The scrapers have
  a live smoke test:
  `LIVE=1 go test ./internal/webtool/ -run TestLiveEngines -v`.

For search that just works, configure **Brave** or **Tavily**.

## `web_fetch`

GETs an http(s) URL and returns its content as **Markdown** — headings, links
(`[text](url)`), lists, and code survive, so the model can cite a section or follow
a link (plain text/JSON pass through).

- An optional **`prompt`** argument returns only the parts relevant to it, distilled
  by the model, instead of the whole page — keeping the conversation lean.
- Fetches are **cached ~15 min**, bounded by a size cap and a **30 s timeout**.
- Refuses non-http(s) schemes and cloud-metadata / link-local addresses (SSRF
  guard); loopback/private addresses are allowed for local doc servers.

## A typical loop

```
web_search "deepseek api streaming 2026"
  → pick a result
web_fetch(url, prompt: "streaming request example")
  → use the extracted snippet
```

> **Note:** the keyless backends are HTML scrapes and may break if an engine
> changes its markup — configure Brave or Tavily for reliability.
