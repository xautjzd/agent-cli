# Usage & cost tracking

`/usage` reports token consumption and **estimated cost** the way Claude Code's
Usage panel does — **all-time for the project** (persisted across sessions), broken
down **by model** and **by provider**, plus the current session.

```
Usage · this project · all time

  Total cost    $401.23
  Tokens        1.1m  (27.1k in · 1.0m out)
  Requests      72
  Model time    4h30m36s

  By model
    claude-opus-4-8   738.9k tok   17.2k → 721.7k   $287.69
    claude-fable-5    307.0k tok    4.9k → 302.1k   $113.54
    glm-4.6            13.0k tok    5.0k →   8.0k         —
  By provider
    anthropic    1.0m tok   22.1k → 1.0m   $401.23
    deepseek    13.0k tok    5.0k → 8.0k         —

  This session   15.2k tok · 12.3s · context 15.2k
```

## Where totals live

Totals accumulate to `<agent-home>/projects/<encoded>/usage.json`, so "total
consumed" survives restarts. **Subagent** turns count toward the totals too (shared
recorder). It's per-project, matching where [sessions](sessions.md) and the audit
log live.

## Where prices come from

Priority order: **models.dev → config `prices` overrides → built-in table**.

- **[models.dev](https://models.dev)** is the primary source, kept current. The
  catalog is cached at `<agent-home>/models-dev-prices.json`, loaded instantly on
  startup and refreshed in the background (24 h TTL) — so it never blocks startup
  and works offline from the last cache.
- Because the same model id is listed under many providers (first-party + gateways)
  at different prices, the price is matched by **your provider**; when the provider
  is unknown, the model's **most-common** (first-party) price is used, not an
  inflated gateway rate.
- A small **built-in table** covers offline first runs (Anthropic, OpenAI,
  DeepSeek, Gemini, GLM, MiniMax, Grok). Vendors with a long-context or
  high-speed tier are priced at their **base** tier, so a very long session is
  under-counted rather than over-counted — override it with `prices` if that
  matters.

A model with no price anywhere shows **`—`** — its tokens still count. When a model
is unpriced, `/usage` lists it with a copy-pasteable `"prices"` snippet.

## Configuration: price overrides

For models models.dev doesn't cover, set `prices` (USD **per 1M tokens**), keyed by
model:

```jsonc
{
  "prices": {
    "deepseek-v4-pro": { "input": 0.28, "output": 1.14 },
    "glm-4.6":         { "input": 0.60, "output": 2.20 }
  }
}
```

Because cost is **derived from stored tokens on every read**, a newly available
price **retroactively costs already-accumulated usage** — set a price and your
existing history is priced immediately.
