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
	"sort"
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
	// Auth selects the credential header; only meaningful for the
	// Anthropic format, where third-party gateways expect a bearer token.
	Auth string
	// EnvKeys are credential environment variables, tried in order.
	EnvKeys []string
	// DefaultModel is used when configuration names no model.
	DefaultModel string
	// Models are known model identifiers, used for completion only.
	Models []string
	// Vision marks providers whose listed models accept image input.
	Vision bool
	// Notes is a short hint shown in listings (where to get a key, etc.).
	Notes string
}

// presets is the built-in provider table. Adding a vendor is a pure
// addition here — no other code changes (OCP).
//
// Endpoints and model names change over time; treat this as a convenience
// layer and override any field in configuration when a vendor moves.
var presets = []Provider{
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
		Vision: true,
		Notes:  "console.anthropic.com",
	},
	{
		Name:         "openai",
		Label:        "OpenAI",
		BaseURL:      "https://api.openai.com/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"OPENAI_API_KEY"},
		DefaultModel: "gpt-5.6",
		Models:       []string{"gpt-5.6", "gpt-5.5", "gpt-5.5-pro", "gpt-5.4", "gpt-4o", "gpt-4o-mini"},
		Vision:       true,
		Notes:        "platform.openai.com",
	},
	{
		Name:         "deepseek",
		Label:        "DeepSeek",
		BaseURL:      "https://api.deepseek.com",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"DEEPSEEK_API_KEY"},
		DefaultModel: "deepseek-chat",
		Models:       []string{"deepseek-chat", "deepseek-reasoner", "deepseek-v4-pro", "deepseek-v4-flash"},
		Notes:        "platform.deepseek.com",
	},
	{
		Name:         "deepseek-anthropic",
		Label:        "DeepSeek (Anthropic-compatible)",
		BaseURL:      "https://api.deepseek.com/anthropic",
		Format:       provider.FormatAnthropic,
		Auth:         provider.AuthBearer,
		EnvKeys:      []string{"ANTHROPIC_AUTH_TOKEN", "DEEPSEEK_API_KEY"},
		DefaultModel: "deepseek-chat",
		Models:       []string{"deepseek-chat", "deepseek-reasoner", "deepseek-v4-pro"},
		Notes:        "Claude-Code-compatible endpoint",
	},
	{
		Name:         "glm",
		Aliases:      []string{"zhipu", "bigmodel"},
		Label:        "Zhipu GLM",
		BaseURL:      "https://open.bigmodel.cn/api/paas/v4",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"ZHIPU_API_KEY", "ZHIPUAI_API_KEY", "GLM_API_KEY"},
		DefaultModel: "glm-4.6",
		Models:       []string{"glm-5.2", "glm-5.1", "glm-5", "glm-4.7", "glm-4.6", "glm-4.6v", "glm-4.5-air"},
		Notes:        "open.bigmodel.cn",
	},
	{
		Name:         "glm-anthropic",
		Label:        "Zhipu GLM (Anthropic-compatible)",
		BaseURL:      "https://open.bigmodel.cn/api/anthropic",
		Format:       provider.FormatAnthropic,
		Auth:         provider.AuthBearer,
		EnvKeys:      []string{"ANTHROPIC_AUTH_TOKEN", "ZHIPU_API_KEY", "ZHIPUAI_API_KEY", "GLM_API_KEY"},
		DefaultModel: "glm-4.6",
		Models:       []string{"glm-5.2", "glm-5.1", "glm-4.7", "glm-4.6"},
		Notes:        "Claude-Code-compatible endpoint",
	},
	{
		Name:         "kimi",
		Aliases:      []string{"moonshot"},
		Label:        "Moonshot Kimi",
		BaseURL:      "https://api.moonshot.cn/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"},
		DefaultModel: "kimi-k2-turbo-preview",
		Models:       []string{"kimi-k3", "kimi-k2.6", "kimi-k2-thinking", "kimi-k2-turbo-preview", "moonshot-v1-128k"},
		Notes:        "platform.moonshot.cn",
	},
	{
		Name:         "kimi-anthropic",
		Label:        "Moonshot Kimi (Anthropic-compatible)",
		BaseURL:      "https://api.moonshot.cn/anthropic",
		Format:       provider.FormatAnthropic,
		Auth:         provider.AuthBearer,
		EnvKeys:      []string{"ANTHROPIC_AUTH_TOKEN", "MOONSHOT_API_KEY"},
		DefaultModel: "kimi-k2-turbo-preview",
		Models:       []string{"kimi-k3", "kimi-k2-thinking", "kimi-k2-turbo-preview"},
		Notes:        "Claude-Code-compatible endpoint",
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
		Vision: true,
		Notes:  "bailian.console.aliyun.com",
	},
	{
		Name:         "dashscope-intl",
		Label:        "Alibaba DashScope (international)",
		BaseURL:      "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"DASHSCOPE_API_KEY"},
		DefaultModel: "qwen-max",
		Models:       []string{"qwen3.7-max", "qwen3.7-plus", "qwen3-max", "qwen3-coder-plus", "qwen-max", "qwen-plus"},
		Notes:        "Singapore region",
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
		Vision: true,
		Notes:  "aggregator; model names are namespaced",
	},
	{
		Name:         "siliconflow",
		Aliases:      []string{"guiji"},
		Label:        "SiliconFlow",
		BaseURL:      "https://api.siliconflow.cn/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"SILICONFLOW_API_KEY"},
		DefaultModel: "deepseek-ai/DeepSeek-V3",
		Models:       []string{"deepseek-ai/DeepSeek-V3", "Qwen/Qwen2.5-72B-Instruct"},
		Notes:        "cloud.siliconflow.cn",
	},
	{
		Name:         "ollama",
		Label:        "Ollama (local)",
		BaseURL:      "http://localhost:11434/v1",
		Format:       provider.FormatOpenAI,
		EnvKeys:      []string{"OLLAMA_API_KEY"},
		DefaultModel: "qwen2.5-coder",
		Models:       []string{"qwen2.5-coder", "llama3.3", "qwen2.5-vl"},
		Notes:        "no API key required",
	},
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

// Lookup returns the preset for a provider name or alias.
func Lookup(name string) (*Provider, bool) {
	p, ok := index[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// All returns every preset, sorted by name.
func All() []Provider {
	out := make([]Provider, len(presets))
	copy(out, presets)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns all canonical preset names, sorted.
func Names() []string {
	names := make([]string, 0, len(presets))
	for _, p := range presets {
		names = append(names, p.Name)
	}
	sort.Strings(names)
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
