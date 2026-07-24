# Web tools

`web_search` and `web_fetch` let the agent look things up on its own — current
library versions, an API reference, a changelog, or the meaning of an error message
— instead of guessing from stale training data. Both tools are available to
[subagents](tools.md#task-delegation--parallel-subagents) too.

## `web_search`

Returns titles, URLs, and snippets. The default backend is **DuckDuckGo, which
needs no API key** (a best-effort HTML scrape).

For higher reliability, configure **Brave** or **Tavily**:

```jsonc
{ "web_search": { "provider": "brave", "env_key": "BRAVE_API_KEY" } }
// or:
{ "web_search": { "provider": "tavily", "api_key": "tvly-..." } }
```

| Field | Meaning |
|---|---|
| `provider` | `duckduckgo` (default), `brave`, or `tavily` |
| `api_key` | Inline key (avoid — prefer `env_key`) |
| `env_key` | Name of the env var holding the key |

Brave/Tavily read the key from `api_key`, the configured `env_key`, or the standard
`BRAVE_API_KEY` / `TAVILY_API_KEY` variable.

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

> **Note:** the DuckDuckGo backend is an HTML scrape and may break if their markup
> changes — configure Brave or Tavily for reliability.
