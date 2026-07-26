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
agent -provider anthropic -model claude-sonnet-5    # at launch
```

```
> /provider openai gpt-4o-mini    # mid-session, keeps the conversation
> /model deepseek-reasoner        # switch model only
```

`/provider` with no argument lists your config profiles and the built-ins
**separately** — a profile named like a preset (e.g. `zai`, or one of its aliases)
shadows it and shows
once, tagged "overrides built-in", so there are no duplicates. Each built-in shows
its default model and whether its credential is exported.

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

| Preset | Vendor | Wire format | Credential |
|---|---|---|---|
| `anthropic` | Anthropic | Anthropic | `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | OpenAI | `OPENAI_API_KEY` |
| `deepseek` | DeepSeek | OpenAI + Anthropic | `DEEPSEEK_API_KEY` |
| `zai` (`glm`, `zhipu`, `bigmodel`) | Z.AI (GLM models) | OpenAI + Anthropic | `ZHIPUAI_API_KEY` |
| `kimi` (`moonshot`) | Moonshot Kimi | OpenAI + Anthropic | `MOONSHOT_API_KEY` |
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

### Named provider profiles

Address any OpenAI-compatible endpoint by name (codex/opencode style). `env_key`
keeps secrets out of the file:

```json
{
  "provider": "deepseek",
  "model": "deepseek-chat",
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
  "provider": "glm",
  "providers": {
    "glm":  {"format": "anthropic", "auth": "bearer",
             "base_url": "https://open.bigmodel.cn/api/anthropic",
             "model": "glm-4.6", "env_key": "ANTHROPIC_AUTH_TOKEN"},
    "qwen": {"format": "anthropic", "auth": "bearer",
             "base_url": "https://dashscope.aliyuncs.com/api/v2/apps/claude-code-proxy",
             "model": "qwen3-coder-plus", "env_key": "DASHSCOPE_API_KEY"}
  }
}
```

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

Complete `config.json` files for common setups — drop one into `~/.agent/config.json`
(global) or `<project>/.agent/config.json` (project).

### DeepSeek (default), with a local Ollama fallback

```json
{
  "provider": "deepseek",
  "model": "deepseek-chat",
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
    "deepseek": {"model": "deepseek-chat", "env_key": "DEEPSEEK_API_KEY"}
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

### Anthropic-compatible gateways (GLM + Qwen)

```json
{
  "provider": "glm",
  "providers": {
    "glm": {
      "format": "anthropic", "auth": "bearer",
      "base_url": "https://open.bigmodel.cn/api/anthropic",
      "model": "glm-4.6", "env_key": "ANTHROPIC_AUTH_TOKEN"
    },
    "qwen": {
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
  "model": "deepseek-chat",
  "vision_provider": "openai",
  "vision_model": "gpt-4o-mini"
}
```

Now an image `@ref` or `Ctrl+V` paste is described by `gpt-4o-mini` and the text fed
to DeepSeek. See [File references & vision](file-references-and-vision.md).
