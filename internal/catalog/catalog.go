// Package catalog holds built-in presets for common LLM providers so that
// naming a provider is enough to configure it.
//
// It is pure data plus lookup (SRP): it knows endpoints, wire formats, auth
// styles and credential environment variables, but performs no I/O and
// builds no clients. Configuration consults it to fill in blanks, and a
// user-defined profile with the same name always wins — a preset is a
// default, never a constraint.
//
// The model lists are advisory: they drive completion and the default
// model, and any model string the vendor accepts may be used regardless of
// whether it appears here. Vendors ship models faster than this file is
// updated, so a missing name means "not listed", not "not supported".
package catalog

import (
	"strings"

	"github.com/xautjzd/agent-cli/internal/provider"
)

// Provider is one built-in preset.
type Provider struct {
	// Name is the canonical identifier used in configuration.
	Name string
	// Aliases are alternative names accepted for the same provider.
	Aliases []string
	// Label is the human-readable vendor name.
	Label string
	// BaseURL is the API endpoint. Empty means the SDK/vendor default.
	BaseURL string
	// Format is the wire protocol: provider.FormatOpenAI or
	// provider.FormatAnthropic.
	Format string
	// AnthropicBaseURL is the vendor's optional second endpoint, which serves
	// the same models over the Anthropic wire (the "Claude Code endpoint").
	// It is an alternative wire for the same vendor, not a separate provider:
	// configuration selects it with "format": "anthropic". Empty means the
	// vendor speaks only Format.
	AnthropicBaseURL string
	// Auth selects the credential header; only meaningful for the
	// Anthropic format, where third-party gateways expect a bearer token.
	Auth string
	// EnvKeys are credential environment variables, tried in order.
	EnvKeys []string
	// DefaultModel is used when configuration names no model.
	DefaultModel string
	// Models are known model identifiers, used for completion only.
	Models []string
	// ContextWindow is the documented context window (tokens) shared by most
	// of this provider's models, used as the default ContextLimit when
	// configuration names no explicit limit. Models that diverge from the
	// family window are listed in modelContextWindows. Zero means unknown,
	// which leaves the conservative global default in force.
	ContextWindow int
	// Vision marks providers whose listed models accept image input.
	Vision bool
	// Notes is a short hint shown in listings (where to get a key, etc.).
	Notes string
}

// presets is the built-in provider table. Adding a vendor is a pure
// addition here — no other code changes (OCP).
//
// The order is deliberate and is the order users see: it runs from the
// frontier labs through the vendors this CLI is most often pointed at, with
// aggregators, regional endpoints and local runtimes last. All() and Names()
// preserve it rather than alphabetizing, so a familiar list does not reshuffle
// as vendors are added.
//
// Endpoints and model names change over time; treat this as a convenience
// layer and override any field in configuration when a vendor moves.
var presets = []Provider{
	{
		Name:         "openai",
		Label:        "OpenAI",
		BaseURL:      "https://api.openai.com/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"OPENAI_API_KEY"},
		DefaultModel: "gpt-5.6-terra",
		// gpt-5.6 ships as three variants (sol/terra/luna); gpt-4o was dropped
		// from the current catalog. Per developers.openai.com/api/docs/pricing.
		Models: []string{
			"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
			"gpt-5.5", "gpt-5.5-pro",
			"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.4-pro",
			"gpt-5.3-codex",
		},
		ContextWindow: 1_000_000, // GPT-5 line (gpt-5.6 ~1.05M; others assumed 1M)
		Vision:        true,
		Notes:         "platform.openai.com",
	},
	{
		Name:         "anthropic",
		Label:        "Anthropic",
		Format:       provider.FormatAnthropic,
		Auth:         provider.AuthAPIKey,
		EnvKeys:      []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"},
		DefaultModel: "claude-opus-4-8",
		Models: []string{
			"claude-opus-4-8", "claude-sonnet-5", "claude-fable-5",
			"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5",
		},
		// Claude 5 / Opus 4.x all ship a 1M window; Haiku 4.5 is the
		// exception at 200K (see modelContextWindows).
		ContextWindow: 1_000_000,
		Vision:        true,
		Notes:         "console.anthropic.com",
	},
	{
		Name: "google",
		// Gemini is the model family, Google the vendor — same split as
		// GLM/Z.AI, so the family name stays valid as an alias.
		Aliases: []string{"gemini", "googleai", "aistudio"},
		Label:   "Google (Gemini)",
		// The OpenAI-compatible surface of the Gemini API; the native
		// generateContent API is not spoken here.
		BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		DefaultModel: "gemini-3.6-flash",
		Models: []string{
			"gemini-3.6-flash", "gemini-3.5-flash", "gemini-3.5-flash-lite",
			"gemini-3.1-pro-preview", "gemini-3.1-flash-lite",
			"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite",
		},
		// 1M is the documented window for the 2.5 line; ai.google.dev does not
		// restate it per model for the 3.x line, so this is the family default
		// rather than a per-model fact.
		ContextWindow: 1_000_000,
		Vision:        true,
		Notes:         "aistudio.google.com/apikey",
	},
	{
		Name:             "deepseek",
		Label:            "DeepSeek",
		BaseURL:          "https://api.deepseek.com",
		AnthropicBaseURL: "https://api.deepseek.com/anthropic",
		Format:           provider.FormatOpenAI,
		EnvKeys:          []string{"DEEPSEEK_API_KEY"},
		DefaultModel:     "deepseek-v4-flash",
		// deepseek-chat / deepseek-reasoner were deprecated 2026-07-24 (they
		// map to the non-thinking / thinking modes of deepseek-v4-flash).
		Models:        []string{"deepseek-v4-pro", "deepseek-v4-flash"},
		ContextWindow: 1_000_000, // all V4 models: 1M
		Notes:         "platform.deepseek.com",
	},
	{
		Name: "zai",
		// GLM is the model family, Z.AI (Zhipu) the vendor; the old spellings
		// stay valid so existing configuration keeps working.
		Aliases:          []string{"glm", "zhipu", "bigmodel", "z.ai"},
		Label:            "Z.AI",
		BaseURL:          "https://open.bigmodel.cn/api/paas/v4",
		AnthropicBaseURL: "https://open.bigmodel.cn/api/anthropic",
		Format:           provider.FormatOpenAI,
		EnvKeys:          []string{"ZHIPU_API_KEY", "ZHIPUAI_API_KEY", "GLM_API_KEY"},
		DefaultModel:     "glm-4.6",
		// Per docs.bigmodel.cn model-overview. Most GLM models are 200K;
		// glm-5.2 (1M) and glm-4.5-air (128K) diverge (see modelContextWindows).
		Models:        []string{"glm-5.2", "glm-5.1", "glm-5", "glm-5-turbo", "glm-4.7", "glm-4.7-flashx", "glm-4.7-flash", "glm-4.6", "glm-4.5-air"},
		ContextWindow: 200_000, // GLM family default
		Notes:         "open.bigmodel.cn",
	},
	{
		Name:             "kimi",
		Aliases:          []string{"moonshot"},
		Label:            "Moonshot Kimi",
		BaseURL:          "https://api.moonshot.cn/v1",
		AnthropicBaseURL: "https://api.moonshot.cn/anthropic",
		Format:           provider.FormatOpenAI,
		EnvKeys:          []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"},
		DefaultModel:     "kimi-k3",
		// The kimi-k2-* variants were discontinued 2026-05-25; these are the
		// current models per platform.kimi.com/docs/models. moonshot-v1-128k
		// is kept for existing users (closed to new registrations).
		Models:        []string{"kimi-k3", "kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.6", "kimi-k2.5", "moonshot-v1-128k"},
		ContextWindow: 256_000, // Kimi K2.x family; kimi-k3 and moonshot-v1-128k differ (see modelContextWindows)
		Notes:         "platform.kimi.com",
	},
	{
		Name:         "minimax",
		Aliases:      []string{"minimaxi"},
		Label:        "MiniMax",
		BaseURL:      "https://api.minimaxi.com/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"MINIMAX_API_KEY", "MINIMAXI_API_KEY"},
		DefaultModel: "MiniMax-M3",
		// Per platform.minimaxi.com text-openai-api. The "-highspeed" variants
		// are the same models on a faster (double-priced) tier.
		Models: []string{
			"MiniMax-M3",
			"MiniMax-M2.7", "MiniMax-M2.7-highspeed",
			"MiniMax-M2.5", "MiniMax-M2.5-highspeed",
			"MiniMax-M2.1", "MiniMax-M2.1-highspeed",
			"MiniMax-M2",
		},
		ContextWindow: 204_800, // the M2 line; M3 is 1M (see modelContextWindows)
		// Only M3 takes image input, so vision is decided per model
		// (provider.SupportsVision) rather than claimed for the whole preset.
		Notes: "platform.minimaxi.com",
	},
	{
		Name:    "xai",
		Aliases: []string{"grok", "x.ai"},
		Label:   "xAI (Grok)",
		BaseURL: "https://api.x.ai/v1",
		Format:  provider.FormatOpenAI,
		EnvKeys: []string{"XAI_API_KEY", "GROK_API_KEY"},
		// Per docs.x.ai/developers/models. grok-4.5 is the flagship; the
		// 4.20 line ships as separate reasoning / non-reasoning / multi-agent
		// checkpoints rather than as one switchable model.
		DefaultModel: "grok-4.5",
		Models: []string{
			"grok-4.5", "grok-4.3",
			"grok-4.20-0309-reasoning", "grok-4.20-0309-non-reasoning",
			"grok-4.20-multi-agent-0309", "grok-build-0.1",
		},
		ContextWindow: 500_000, // grok-4.5; the 4.20/4.3 line is 1M (see modelContextWindows)
		// Vision is deliberately not claimed: docs.x.ai documents image-input
		// limits but does not say which text models accept images. Set
		// "vision": true on a profile if yours does.
		Notes: "console.x.ai",
	},
	{
		Name:         "openrouter",
		Label:        "OpenRouter",
		BaseURL:      "https://openrouter.ai/api/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"OPENROUTER_API_KEY"},
		DefaultModel: "openai/gpt-5.6",
		Models: []string{
			"openai/gpt-5.6", "anthropic/claude-opus-4-8", "x-ai/grok-4.5",
			"deepseek/deepseek-chat", "google/gemini-2.5-pro",
		},
		// Aggregator: windows are per-underlying-model, so the divergent
		// ones are pinned in modelContextWindows; this is the fallback.
		ContextWindow: 128_000,
		Vision:        true,
		Notes:         "aggregator; model names are namespaced",
	},
	{
		Name:         "dashscope",
		Aliases:      []string{"qwen", "bailian"},
		Label:        "Alibaba DashScope (Qwen)",
		BaseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"DASHSCOPE_API_KEY"},
		DefaultModel: "qwen-max",
		Models: []string{
			"qwen3.7-max", "qwen3.7-plus", "qwen3-max", "qwen3-coder-plus",
			"qwen-max", "qwen-plus", "qwen-turbo", "qwen3-vl-plus",
		},
		ContextWindow: 256_000, // qwen-max window; 1M and 131K models pinned below
		Vision:        true,
		Notes:         "bailian.console.aliyun.com",
	},
	{
		Name:          "dashscope-intl",
		Label:         "Alibaba DashScope (international)",
		BaseURL:       "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		Format:        provider.FormatOpenAI,
		EnvKeys:       []string{"DASHSCOPE_API_KEY"},
		DefaultModel:  "qwen-max",
		Models:        []string{"qwen3.7-max", "qwen3.7-plus", "qwen3-max", "qwen3-coder-plus", "qwen-max", "qwen-plus"},
		ContextWindow: 256_000, // matches dashscope
		Notes:         "Singapore region",
	},
	{
		Name:          "siliconflow",
		Aliases:       []string{"guiji"},
		Label:         "SiliconFlow",
		BaseURL:       "https://api.siliconflow.cn/v1",
		Format:        provider.FormatOpenAI,
		EnvKeys:       []string{"SILICONFLOW_API_KEY"},
		DefaultModel:  "deepseek-ai/DeepSeek-V3",
		Models:        []string{"deepseek-ai/DeepSeek-V3", "Qwen/Qwen2.5-72B-Instruct"},
		ContextWindow: 131_072, // DeepSeek-V3 / Qwen2.5 family
		Notes:         "cloud.siliconflow.cn",
	},
	{
		Name:          "ollama",
		Label:         "Ollama (local)",
		BaseURL:       "http://localhost:11434/v1",
		Format:        provider.FormatOpenAI,
		EnvKeys:       []string{"OLLAMA_API_KEY"},
		DefaultModel:  "qwen2.5-coder",
		Models:        []string{"qwen2.5-coder", "llama3.3", "qwen2.5-vl"},
		ContextWindow: 32_768, // local models default to a modest window
		Notes:         "no API key required",
	},
}

// anthropicSuffix is the legacy spelling of a vendor's Anthropic-compatible
// endpoint, which used to be a separate preset ("deepseek-anthropic"). Both
// endpoints serve the same models, so they are now one provider with two
// wires; the old names still resolve (see Resolve) and keep configuration
// written against them working.
const anthropicSuffix = "-anthropic"

// anthropicTokenEnv is the credential variable Claude-Code-compatible
// gateways conventionally read, tried before a vendor's own key on the
// Anthropic wire.
const anthropicTokenEnv = "ANTHROPIC_AUTH_TOKEN"

// Endpoint is one vendor endpoint: where to connect, what wire it speaks,
// how the credential is presented and where to read it from.
type Endpoint struct {
	BaseURL string
	Format  string
	Auth    string
	EnvKeys []string
}

// Endpoint resolves one wire format to the endpoint serving it. An empty
// format means the vendor's primary wire. It reports false when the vendor
// does not offer the requested format, leaving the caller to fall back.
func (p *Provider) Endpoint(format string) (Endpoint, bool) {
	primary := Endpoint{BaseURL: p.BaseURL, Format: p.Format, Auth: p.Auth, EnvKeys: p.EnvKeys}
	if format == "" || format == p.Format {
		return primary, true
	}
	if format == provider.FormatAnthropic && p.AnthropicBaseURL != "" {
		// Third-party Anthropic gateways authenticate with a bearer token and
		// accept either the Claude-Code variable or the vendor's own key.
		return Endpoint{
			BaseURL: p.AnthropicBaseURL,
			Format:  provider.FormatAnthropic,
			Auth:    provider.AuthBearer,
			EnvKeys: append([]string{anthropicTokenEnv}, p.EnvKeys...),
		}, true
	}
	return primary, false
}

// Resolve maps a configured provider name to its preset and the wire format
// that name implies (empty = the preset's primary wire). It accepts canonical
// names, aliases, and the legacy "<vendor>-anthropic" spelling, which now
// means "this vendor on its Anthropic wire".
func Resolve(name string) (*Provider, string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if p, ok := index[name]; ok {
		return p, "", true
	}
	if base, ok := index[strings.TrimSuffix(name, anthropicSuffix)]; ok &&
		strings.HasSuffix(name, anthropicSuffix) && base.AnthropicBaseURL != "" {
		return base, provider.FormatAnthropic, true
	}
	return nil, "", false
}

// modelContextWindows pins the context window (tokens) for individual models
// whose window differs from their provider's family default. Keyed by the
// exact model identifier. Entries here win over the provider ContextWindow.
var modelContextWindows = map[string]int{
	"claude-haiku-4-5": 200_000, // 200K while the rest of the Claude line is 1M

	// GLM: family default is 200K; glm-5.2 / glm-4-long are 1M, the
	// glm-4.5-air tier is 128K.
	"glm-5.2":      1_000_000,
	"glm-4-long":   1_000_000,
	"glm-4.5-air":  128_000,
	"glm-4.5-airx": 128_000,

	// Kimi: K2 family is 256K (the provider default); K3 is 1M and the
	// legacy moonshot-v1-128k is narrower.
	"kimi-k3":          1_000_000,
	"moonshot-v1-128k": 128_000,

	// Qwen: family default is 256K (qwen-max); the qwen3 flagships and
	// qwen-plus are 1M, qwen-turbo is 131K.
	"qwen3.7-max":  1_000_000,
	"qwen3.7-plus": 1_000_000,
	"qwen3-max":    1_000_000,
	"qwen-plus":    1_000_000,
	"qwen-turbo":   131_072,

	// MiniMax: the M2 line is 204,800 (the provider default); M3 is 1M.
	"MiniMax-M3": 1_000_000,

	// xAI: grok-4.5 is 500K (the provider default); the 4.3 / 4.20 line is
	// 1M and the build preview is narrower.
	"grok-4.3":                     1_000_000,
	"grok-4.20-0309-reasoning":     1_000_000,
	"grok-4.20-0309-non-reasoning": 1_000_000,
	"grok-4.20-multi-agent-0309":   1_000_000,
	"grok-build-0.1":               256_000,

	// OpenRouter models are namespaced and each carries its underlying
	// vendor's window, not the aggregator's conservative default.
	"anthropic/claude-opus-4-8": 1_000_000,
	"google/gemini-2.5-pro":     1_000_000,
	"x-ai/grok-4.5":             500_000,
}

// index maps every canonical name and alias to its preset.
var index = func() map[string]*Provider {
	m := make(map[string]*Provider, len(presets)*2)
	for i := range presets {
		p := &presets[i]
		m[p.Name] = p
		for _, a := range p.Aliases {
			m[a] = p
		}
	}
	return m
}()

// Lookup returns the preset for a provider name, alias or legacy
// "<vendor>-anthropic" spelling, discarding the wire format that name
// implies. Use Resolve when the wire matters.
func Lookup(name string) (*Provider, bool) {
	p, _, ok := Resolve(name)
	return p, ok
}

// All returns every preset, in the table's own order (see presets).
func All() []Provider {
	out := make([]Provider, len(presets))
	copy(out, presets)
	return out
}

// Names returns all canonical preset names, in the same order as All.
func Names() []string {
	names := make([]string, 0, len(presets))
	for _, p := range presets {
		names = append(names, p.Name)
	}
	return names
}

// ModelsFor returns the known models for a provider, or nil when the
// provider is unknown. The list is advisory — any model the vendor accepts
// works whether or not it is listed.
func ModelsFor(name string) []string {
	if p, ok := Lookup(name); ok {
		return p.Models
	}
	return nil
}

// ContextWindow returns the documented context window (tokens) for a model,
// or 0 when it cannot be determined. It is used to seed ContextLimit when
// configuration names no explicit limit; an explicit "context_limit" always
// wins. Resolution order: a per-model override, then the family window of the
// provider the model belongs to, then the family window of the configured
// provider (which covers custom model names on a known provider).
func ContextWindow(providerName, model string) int {
	if w, ok := modelContextWindows[model]; ok {
		return w
	}
	// The model itself is the strongest signal of its family.
	if name, ok := ProviderForModel(model); ok {
		if p, ok := Lookup(name); ok && p.ContextWindow > 0 {
			return p.ContextWindow
		}
	}
	if p, ok := Lookup(providerName); ok {
		return p.ContextWindow
	}
	return 0
}

// ProviderForModel returns the canonical name of the built-in provider whose
// model list contains the given model, if any. It is used to detect when a
// configured model belongs to a different provider than the one selected.
func ProviderForModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	for _, p := range presets {
		for _, m := range p.Models {
			if m == model {
				return p.Name, true
			}
		}
	}
	return "", false
}
