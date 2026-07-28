# Providers & models

Two wire formats sit behind one interface:

- **Anthropic** — the Messages API via the official `anthropic-sdk-go` (tool use,
  extended thinking, vision, streaming, prompt caching).
- **OpenAI-compatible** — OpenAI, DeepSeek, vLLM, Ollama, Moonshot, Qwen, and any
  custom endpoint via `provider: custom` + `base_url`.

Switch between them mid-session with `/provider`. The agent loop, tools, sessions,
permission gate, plan mode, and goals behave identically across providers.

## Usage

### Switching provider/model

```bash
agent -provider anthropic -model claude-sonnet-5    # at launch, this run only
agent provider list                                 # from the shell: what exists, what has a key
agent provider use kimi --anthropic                 # from the shell: persist the choice
```

```
> /provider openai gpt-4o-mini    # mid-session, keeps the conversation
> /model deepseek-v4-pro        # switch model only
```

`agent provider list` prints the same listing as `/provider` (it is the same
renderer), so you can check a vendor's credential without starting a session, and
`agent provider use <name> [model] [--anthropic|--openai]` writes the choice to
the global config — the non-interactive half of `/provider`.

`/provider` with no argument lists your config profiles and the built-ins
**separately** — a profile named like a preset (e.g. `zai`, or one of its
aliases) shadows it and shows once, tagged "overrides built-in", so there are no
duplicates. Each built-in shows its default model and whether its credential is
exported.

**`/provider <TAB>` and `/model <TAB>` complete from the catalog** (deduplicated
against your profiles). If a credential is missing, `/provider <name>` **prompts
for the API key** (masked) and offers to save it — it does not error out.

### Built-in provider presets (zero config)

Naming a preset is the whole configuration — endpoint, wire format, auth style,
default model, and credential variable are all known:

```bash
export ZHIPUAI_API_KEY=...
agent config set provider zai      # model, base_url and auth resolved automatically
```

The rows are in the order `/provider` lists them, which is the table's own order
in `internal/catalog/catalog.go` rather than alphabetical:

| Preset | Vendor | Wire format | Credential |
|---|---|---|---|
| `openai` | OpenAI | OpenAI | `OPENAI_API_KEY` |
| `anthropic` | Anthropic | Anthropic | `ANTHROPIC_API_KEY` |
| `google` (`gemini`) | Google (Gemini) | OpenAI-compatible surface | `GEMINI_API_KEY` |
| `deepseek` | DeepSeek | OpenAI + Anthropic | `DEEPSEEK_API_KEY` |
| `zai` (`glm`, `zhipu`, `bigmodel`) | Z.AI (GLM models) | OpenAI + Anthropic | `ZHIPUAI_API_KEY` |
| `kimi` (`moonshot`) | Moonshot Kimi | OpenAI + Anthropic | `MOONSHOT_API_KEY` |
| `minimax` | MiniMax | OpenAI | `MINIMAX_API_KEY` |
| `xai` (`grok`) | xAI (Grok) | OpenAI | `XAI_API_KEY` |
| `dashscope` (`qwen`) | Alibaba DashScope | OpenAI | `DASHSCOPE_API_KEY` |
| `dashscope-intl` | DashScope (Singapore) | OpenAI | `DASHSCOPE_API_KEY` |
| `openrouter` | OpenRouter | OpenAI | `OPENROUTER_API_KEY` |
| `siliconflow` | SiliconFlow | OpenAI | `SILICONFLOW_API_KEY` |
| `ollama` | Ollama (local) | OpenAI | — |

Model lists are seeded from [models.dev](https://models.dev) and refreshed
periodically; they are **advisory** — any model the vendor accepts works, listed
or not.

Presets are defaults, never constraints:

- Any field you set explicitly (`model`, `base_url`, `api_key`) wins over the
  preset — including a model newer than the catalog knows about.
- A named profile with the same name shadows the preset entirely.
- Endpoints and model lists change over time; override anything a vendor moves.

### Two wires, one vendor

DeepSeek, Z.AI and Moonshot each publish two endpoints for the same models: their
OpenAI-compatible API and a Claude-Code-compatible ("Anthropic") one. That is a
wire choice, not a second vendor, so each is **one preset** addressed over either
wire — the OpenAI one by default:

```bash
> /provider deepseek --anthropic   # switch to the Claude-Code endpoint
> /provider deepseek               # back to the OpenAI endpoint
agent -provider zai -format anthropic
agent config set format anthropic  # persist the wire
```

On the Anthropic wire the credential is presented as a bearer token and
`ANTHROPIC_AUTH_TOKEN` is tried before the vendor's own key.

The old `deepseek-anthropic` / `glm-anthropic` / `kimi-anthropic` names still
resolve — they now mean "this vendor, on its Anthropic wire" and are normalized
to `provider` + `format` when written back to the config file. `glm`, `zhipu` and
`bigmodel` remain aliases of `zai` (GLM is the model family; Z.AI is the vendor).

## Configuration

### Custom providers

Any endpoint that speaks the OpenAI or Anthropic API can be added as a provider
of its own, from the shell or mid-session:

```bash
agent provider add my-gw --base-url https://llm.internal/v1 --model internal-v2
agent provider add gw2 --base-url https://gw2.example/anthropic --model claude-x --anthropic
agent provider remove my-gw
```

Both forms ask for each field when given no flags — name, base URL, API style,
model, key — so nothing has to be memorized:

```bash
agent provider add             # guided
```

In a session, **`custom` is offered in the `/provider` list itself**, so defining
one is found the same way as selecting one:

```
> /provider <TAB>
  my-gw      openai · internal-v2 · …
  openai     OpenAI · gpt-5.6-terra · …
  …
  custom     define a custom endpoint — asks for each field
> /provider custom             # guided
> /provider custom my-gw --base-url https://llm.internal/v1 --model internal-v2
> /provider remove my-gw
```

| Flag | Meaning |
|---|---|
| `--base-url <url>` | required — the endpoint, including any `/v1` |
| `--model <id>` | the model it serves |
| `--anthropic` / `--openai` | which API style it speaks (default `openai`) |
| `--api-key <key>` | store the key in the config |
| `--env-key <VAR>` | read the key from a named variable instead |
| `--vision` | its models accept image input |

**Leave `--api-key` out and the key comes from `<NAME>_API_KEY`** — the provider
name upper-cased, with punctuation turned into `_` (`my-gw` → `MY_GW_API_KEY`).
Export it in your shell or `~/.zshrc` and the definition itself stays free of
secrets. An `--anthropic` endpoint defaults to bearer auth, which is what
third-party gateways expect.

Custom providers are written to `providers` in the global config and listed
**ahead of the built-ins**, with their model, endpoint and where their key comes
from.

### Named provider profiles

Address any OpenAI-compatible endpoint by name (codex/opencode style). `env_key`
keeps secrets out of the file:

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "providers": {
    "ollama":   {"base_url": "http://localhost:11434/v1", "model": "qwen2.5-coder:32b", "api_key": "ollama"},
    "moonshot": {"base_url": "https://api.moonshot.cn/v1", "model": "kimi-k2", "env_key": "MOONSHOT_API_KEY"}
  }
}
```

With profiles defined, `"provider": "ollama"` in config or `/provider ollama`
in-session just works. Profiles merge by name across config layers, so a project
can pin its own model without repeating everything.

**Profile fields:**

| Field | Meaning |
|---|---|
| `base_url` | Endpoint URL |
| `model` | Default model for this profile |
| `api_key` | Inline key (avoid — prefer `env_key`) |
| `env_key` | Name of the env var holding the key |
| `format` | `openai` (default) or `anthropic` |
| `auth` | `api_key` (default, sends `x-api-key`) or `bearer` (`Authorization: Bearer`) |
| `vision` | `true` to mark an unrecognized model as vision-capable |

## The Anthropic provider

`provider: anthropic` speaks the Messages API through the official SDK, not the
OpenAI-compatible format. The adapter translates in both directions:

| Concern | OpenAI-compatible | Anthropic |
|---|---|---|
| System prompt | `role: system` message | top-level `system` field |
| Tool calls | `tool_calls[].function.arguments` (JSON **string**) | `tool_use` block, `input` (JSON **object**) |
| Tool results | one message per result | `tool_result` blocks, **all in one user message** |
| Images | `image_url` data URL | `image` block with `media_type` + base64 |
| Thinking | `reasoning_content` (display-only) | signed `thinking` blocks, replayed verbatim |
| `max_tokens` | optional | **required** (defaults supplied) |

Notes:

- **Extended thinking is on by default** (adaptive, summarized) and renders through
  the `✻ Thinking…` display. Disable with `agent config set thinking off`.
  Thinking blocks carry signatures and are replayed unchanged on later turns — the
  API rejects altered ones — so reasoning survives multi-turn tool loops.

### Reasoning effort is per model, not per vendor

`/effort` offers the levels the **active model** accepts. The ladder is
`off · minimal · low · medium · high · xhigh · max · adaptive`, but no vendor
exposes all of it, and models within one vendor disagree:

| Model | Can be turned off | Strength levels |
|---|---|---|
| `deepseek-v4-*` | yes (`thinking.type`) | low, medium, high, xhigh, max |
| `glm-5.2`+ | yes (`thinking.type`) | minimal … max (`reasoning_effort`) |
| `glm-4.5` … `glm-5.1` | yes | none — toggle only |
| `kimi-k3` | **no** — always thinks | low, high, max |
| `kimi-k2.7-code` | **no** — `disabled` is an error | none |
| `kimi-k2.5` / `k2.6` | yes | none — toggle only |
| `gpt-5*` | yes (`reasoning_effort: none`) | minimal … max (exact set is per model) |
| `gemini-*` | **no** | minimal, low, medium, high |
| `MiniMax-M3` | yes (`thinking.type`) | none — toggle only |
| `MiniMax-M2*` | **no** — `disabled` is ignored | none |
| `grok-4.5`, `grok-4.20-*-reasoning` | **no** — reasoning cannot be disabled | low, medium, high |
| `grok-4.20-multi-agent-*` | **no** | low, medium, high, xhigh (agent count) |
| `claude-*` | yes | mapped to thinking budgets |

Levels a model does not accept are hidden from the menu and rejected with an
explanation if named outright; a level left in the config from a previous model
is clamped down to the nearest supported one rather than sent (DeepSeek answers
an unknown `reasoning_effort` with HTTP 400). `adaptive` always means "send no
strength and take the vendor default". Unknown models — a custom gateway, or one
newer than the table in `internal/provider/thinking.go` — get low/medium/high and
no disable switch, since guessing a field name would fail the whole request.
- **Parallel tool calls** are preserved: every result for one assistant turn is
  sent in a single user message.
- **Credentials** come from `ANTHROPIC_API_KEY`, the config file, or — since the
  SDK resolves them itself — an `ant auth login` profile on disk.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
agent -provider anthropic                       # claude-opus-4-8 by default
agent -provider anthropic -model claude-sonnet-5
```

### Anthropic-compatible third-party gateways

Several vendors expose an Anthropic-compatible endpoint. For preset vendors
(DeepSeek, Z.AI, Kimi) it is built in — use `/provider <name> --anthropic` (see
[Two wires, one vendor](#two-wires-one-vendor)) and skip the profile. For any
other gateway, declare a named profile with `"format": "anthropic"`; both wire
formats can coexist:

```json
{
  "provider": "qwen-cc",
  "providers": {
    "qwen-cc": {"format": "anthropic", "auth": "bearer",
                "base_url": "https://dashscope.aliyuncs.com/api/v2/apps/claude-code-proxy",
                "model": "qwen3-coder-plus", "env_key": "DASHSCOPE_API_KEY"}
  }
}
```

Give it a name of its own rather than a preset's: a profile named `zai` or
`deepseek` **takes over** that name, and the built-in vendor is then only
reachable through one of its aliases.

**Confirm the exact base URL and model names against your vendor's console** —
these paths differ per provider and change over time.

Troubleshooting a rejecting gateway — three settings usually explain it:

- **`401 InvalidApiKey`** — the credential is not exported (check `agent config
  show` for `api_key: (not set)`), or the auth style is wrong; try flipping
  `"auth"` between `bearer` and `api_key`.
- **Extended thinking** — not every gateway implements it: `agent config set
  thinking off`.
- **Streaming** — gateways that only implement the blocking API report
  `no stream events`; the endpoint isn't fully compatible.

## Prompt caching (Anthropic)

For Anthropic-format endpoints, the adapter places `cache_control` breakpoints so
the growing conversation is re-read from cache each turn (tools → system → recent
messages). It is **on by default** for all Anthropic-format endpoints including
gateways; disable it for a gateway that rejects the field:

```jsonc
{ "prompt_cache": "off" }    // or per-profile: { "prompt_cache": "off" }
```

Anthropic reports `input_tokens` as the uncached portion only; the adapter folds
cache read/write back into the reported prompt tokens so `/usage` accounting stays
correct.

## Example configurations

Complete `config.json` files for common setups — drop one into `~/.agents/config.json`
(global) or `<project>/.agent/config.json` (project).

### DeepSeek (default), with a local Ollama fallback

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "providers": {
    "ollama": {
      "base_url": "http://localhost:11434/v1",
      "model": "qwen2.5-coder:32b",
      "api_key": "ollama"
    }
  }
}
```

`DEEPSEEK_API_KEY` in the environment covers the default; `/provider ollama` hops to
the local model with no key needed.

### Anthropic as the primary provider

```json
{
  "provider": "anthropic",
  "model": "claude-opus-4-8",
  "thinking": "adaptive",
  "prompt_cache": "on"
}
```

Credentials come from `ANTHROPIC_API_KEY` (or an `ant auth login` profile) — no
`api_key` needed in the file.

### OpenAI plus DeepSeek, switchable mid-session

```json
{
  "provider": "openai",
  "model": "gpt-4o",
  "providers": {
    "openai":   {"model": "gpt-4o",        "env_key": "OPENAI_API_KEY"},
    "deepseek": {"model": "deepseek-v4-flash", "env_key": "DEEPSEEK_API_KEY"}
  }
}
```

Both are preset vendors, so `base_url`/`format`/`auth` are filled automatically —
the profiles only pin a model and point at the credential. `/provider deepseek`
switches without losing context.

### A custom OpenAI-compatible gateway

```json
{
  "provider": "work",
  "providers": {
    "work": {
      "base_url": "https://llm.internal.example/v1",
      "model": "internal-coder-v2",
      "env_key": "WORK_LLM_TOKEN"
    }
  }
}
```

Any endpoint that speaks the OpenAI chat-completions format works this way — no
`format` field means OpenAI-compatible.

### An Anthropic-compatible gateway

For a preset vendor this is built in — `/provider deepseek --anthropic`. For
anything else, define it:

```bash
agent provider add qwen-cc --anthropic \
  --base-url https://dashscope.aliyuncs.com/api/v2/apps/claude-code-proxy \
  --model qwen3-coder-plus --env-key DASHSCOPE_API_KEY
```

```json
{
  "provider": "qwen-cc",
  "providers": {
    "qwen-cc": {
      "format": "anthropic", "auth": "bearer",
      "base_url": "https://dashscope.aliyuncs.com/api/v2/apps/claude-code-proxy",
      "model": "qwen3-coder-plus", "env_key": "DASHSCOPE_API_KEY"
    }
  }
}
```

If a gateway rejects extended thinking, add `"thinking": "off"` (top-level or, if
supported per profile, in the profile). Confirm each `base_url`/`model` against the
vendor's own console.

### Vision fallback for a text-only primary model

```json
{
  "provider": "deepseek",
  "model": "deepseek-v4-flash",
  "vision_provider": "openai",
  "vision_model": "gpt-4o-mini"
}
```

Now an image `@ref` or `Ctrl+V` paste is described by `gpt-4o-mini` and the text fed
to DeepSeek. See [File references & vision](file-references-and-vision.md).
