// Package config loads CLI configuration with a layered precedence chain
// modeled on Claude Code / codex / opencode:
//
//	command-line flags > environment variables > project config
//	(<project>/.agent/config.json) > global config (~/.agent/config.json)
//	> defaults
//
// Named provider profiles (the "providers" map) let any OpenAI-compatible
// endpoint be addressed by name, codex-style.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xautjzd/agent-cli/internal/catalog"
	"github.com/xautjzd/agent-cli/internal/home"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
)

// ProviderConfig is one named provider profile, e.g.
//
//	"providers": {
//	  "ollama": {"base_url": "http://localhost:11434/v1", "model": "qwen3", "api_key": "ollama"},
//	  "moonshot": {"base_url": "https://api.moonshot.cn/v1", "env_key": "MOONSHOT_API_KEY"}
//	}
type ProviderConfig struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	// EnvKey names an environment variable to read the API key from,
	// keeping secrets out of the file (codex-style).
	EnvKey string `json:"env_key,omitempty"`
	// Vision marks the profile's models as image-capable when automatic
	// model-name detection cannot recognize them.
	Vision bool `json:"vision,omitempty"`
	// Format selects the wire protocol the endpoint speaks: "openai"
	// (default) or "anthropic" for an Anthropic-compatible gateway.
	Format string `json:"format,omitempty"`
	// Auth selects how the credential is presented: "api_key" sends the
	// x-api-key header (Anthropic's own default), "bearer" sends
	// Authorization: Bearer, which most third-party gateways expect.
	Auth string `json:"auth,omitempty"`
}

// Config is the resolved runtime configuration.
type Config struct {
	// Provider selects the vendor: "openai", "deepseek", "custom", or the
	// name of an entry in Providers.
	Provider string `json:"provider"`
	// Model is the model identifier, e.g. "gpt-5.6-terra" or "deepseek-v4-flash".
	Model string `json:"model"`
	// APIKey authenticates against the provider. Prefer setting it via
	// environment variable over storing it in the config file.
	APIKey string `json:"api_key,omitempty"`
	// BaseURL overrides the provider's default endpoint (required for
	// provider "custom").
	BaseURL string `json:"base_url,omitempty"`
	// Format selects which wire a built-in provider is addressed over when
	// the vendor offers more than one: "openai" (default) or "anthropic" for
	// its Claude-Code-compatible endpoint. It is ignored for vendors that
	// serve a single wire, and for named profiles, which carry their own
	// Format.
	Format string `json:"format,omitempty"`
	// MaxTurns bounds the tool-use loop per user message.
	MaxTurns int `json:"max_turns,omitempty"`
	// PermissionMode is the startup permission mode: "hitl" (default) or
	// "bypass".
	PermissionMode string `json:"permission_mode,omitempty"`
	// BashPolicy selects command-risk posture: "standard" (robust deny-list,
	// default) or "strict" (also asks about unrecognized commands).
	BashPolicy string `json:"bash_policy,omitempty"`
	// PermissionRules are user-defined approval rules evaluated before the
	// built-in classifier, giving per-tool and per-path/command control.
	PermissionRules []PermissionRule `json:"permissions,omitempty"`
	// Sandbox selects command confinement: "off" (default), "on", or "auto".
	Sandbox string `json:"sandbox,omitempty"`
	// SandboxDenyNetwork asks the sandbox to block outbound network.
	SandboxDenyNetwork bool `json:"sandbox_deny_network,omitempty"`
	// GoalMaxRounds caps /goal work-check rounds per trigger (default 8).
	GoalMaxRounds int `json:"goal_max_rounds,omitempty"`
	// Thinking is the reasoning-effort level for providers that support it:
	// "off", "low", "medium", "high", or "adaptive" (empty uses the default,
	// adaptive). Anthropic maps it to a thinking budget; OpenAI-compatible
	// backends map it to reasoning_effort. Set via the /effort command.
	Thinking string `json:"thinking,omitempty"`
	// PromptCache controls Anthropic prompt caching: "off" disables the
	// cache_control breakpoints; empty leaves them on (the default). Set it
	// off for a compatible gateway that rejects the field.
	PromptCache string `json:"prompt_cache,omitempty"`
	// VisionProvider/VisionModel name a fallback used to describe images
	// when the primary model has no vision support: the image turn is
	// pre-processed by this model and the description is fed to the
	// primary model as text.
	VisionProvider string `json:"vision_provider,omitempty"`
	VisionModel    string `json:"vision_model,omitempty"`
	// AutoCompact controls automatic context compaction: "on" (default)
	// summarizes older turns when the context nears ContextLimit; "off"
	// disables it (manual /compact still works).
	AutoCompact string `json:"auto_compact,omitempty"`
	// ContextLimit is the model's usable context window in tokens; auto
	// compaction triggers once occupancy passes a fraction of it. 0 uses a
	// conservative default.
	ContextLimit int `json:"context_limit,omitempty"`
	// Providers holds named provider profiles for any OpenAI-compatible
	// endpoint.
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
	// LazyTools controls deferred (on-demand) loading of MCP tools. When on
	// (the default), MCP tools are advertised by name+description in the system
	// prompt and their full schemas are pulled in via the search_tools meta-tool
	// only when needed, keeping per-request tool overhead flat as servers add
	// many schema-heavy tools. "off" advertises every MCP tool on every request.
	LazyTools string `json:"lazy_tools,omitempty"`
	// MCPServers declares Model Context Protocol servers whose tools are
	// merged into the agent's tool set at startup, keyed by a short server
	// name (Claude Code's "mcpServers" convention).
	MCPServers map[string]MCPServerConfig `json:"mcpServers,omitempty"`
	// LSPServers declares Language Server Protocol servers backing the code
	// navigation tools (lsp_diagnostics/references/definition/hover), keyed by
	// language name. Entries override or extend the built-in defaults (gopls,
	// typescript-language-server, pyright, rust-analyzer, clangd).
	LSPServers map[string]LSPServerConfig `json:"lspServers,omitempty"`
	// Subagents declares custom subagent types the "task" tool can delegate
	// to, keyed by type name. The built-in general-purpose type is always
	// available.
	Subagents map[string]SubagentConfig `json:"subagents,omitempty"`
	// Hooks declares external commands to run at lifecycle events (see the
	// hook package), keyed by event name. It integrates the agent with
	// third-party systems (linters, notifiers, policy engines).
	Hooks map[string][]HookConfig `json:"hooks,omitempty"`
	// Prices sets per-model token prices (USD per 1M tokens) for /usage cost
	// estimation, keyed by model name. They override the built-in price table,
	// so any model — including ones the built-in table doesn't know — can be
	// priced.
	Prices map[string]PriceConfig `json:"prices,omitempty"`
	// WebSearch selects the backend for the web_search tool.
	WebSearch WebSearchConfig `json:"web_search,omitempty"`
	// Theme names the color theme the interactive UI renders with (see the
	// theme package: dark, light, dracula, …). Empty uses the default.
	Theme string `json:"theme,omitempty"`
}

// WebSearchConfig selects the web-search backend. Provider is "duckduckgo"
// (keyless, default), "brave", or "tavily"; the API-key backends read the key
// from APIKey or the EnvKey environment variable (defaulting to the provider's
// standard variable).
type WebSearchConfig struct {
	Provider string `json:"provider,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
	EnvKey   string `json:"env_key,omitempty"`
}

// PriceConfig is a model's price in USD per one million tokens.
type PriceConfig struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// HookConfig is one configured hook command for an event. Matcher, when set,
// is a regular expression on the tool name for PreToolUse/PostToolUse events.
type HookConfig struct {
	Matcher        string `json:"matcher,omitempty"`
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// PermissionRule is one user-defined approval rule (see the permission
// package's Rule). action is "allow", "ask", or "deny"; tool "*" or empty
// matches any tool; command is a regex matched against bash commands; path is
// a glob matched against file paths.
type PermissionRule struct {
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
	Path    string `json:"path,omitempty"`
	Action  string `json:"action"`
}

// SubagentConfig defines one custom subagent type (see the subagent package).
type SubagentConfig struct {
	// Description tells the model when to use this subagent type.
	Description string `json:"description,omitempty"`
	// Prompt is the subagent's system prompt (its role and instructions).
	Prompt string `json:"prompt"`
	// Tools optionally restricts the subagent to these tool names; empty
	// means all available tools (except "task").
	Tools []string `json:"tools,omitempty"`
}

// MCPServerConfig describes one Model Context Protocol server. Two transports
// are supported, distinguished by Type (or inferred from the fields present):
//
//	stdio: {"command": "npx", "args": ["-y", "@mcp/server-fs", "/p"], "env": {"TOKEN": "x"}}
//	http:  {"type": "http", "url": "https://mcp.notion.com/mcp", "headers": {"Authorization": "Bearer x"}}
type MCPServerConfig struct {
	// Type is "stdio" or "http". When empty it is inferred: a Command
	// implies stdio, a URL implies http.
	Type string `json:"type,omitempty"`
	// Command and Args launch a stdio server as a child process.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env sets extra environment variables for a stdio child (merged over
	// the inherited environment).
	Env map[string]string `json:"env,omitempty"`
	// URL is the endpoint of an http (Streamable HTTP) server.
	URL string `json:"url,omitempty"`
	// Headers are extra HTTP headers sent with every request to an http
	// server (e.g. an Authorization bearer token).
	Headers map[string]string `json:"headers,omitempty"`
	// Disabled skips the server without removing its entry.
	Disabled bool `json:"disabled,omitempty"`
}

// Transport returns the resolved transport kind: the explicit Type, or an
// inference from which fields are set ("stdio" for a command, "http" for a
// URL). It returns "" when neither is present.
func (m MCPServerConfig) Transport() string {
	switch strings.ToLower(m.Type) {
	case "stdio", "http":
		return strings.ToLower(m.Type)
	case "sse":
		// Legacy SSE servers are addressed through the HTTP transport.
		return "http"
	}
	if m.Command != "" {
		return "stdio"
	}
	if m.URL != "" {
		return "http"
	}
	return ""
}

// LSPServerConfig describes one Language Server Protocol server for the code
// navigation tools. Fields left empty fall back to the built-in default for
// that language, so `{"command": "gopls"}` is enough to override just the
// binary, and a new language needs only command + extensions.
//
//	"lspServers": {"go": {"command": "gopls"},
//	               "zig": {"command": "zls", "extensions": [".zig"]}}
type LSPServerConfig struct {
	// Command and Args launch the server over stdio.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env adds environment variables for the server process.
	Env map[string]string `json:"env,omitempty"`
	// Extensions are the file suffixes (with dot) this server handles; empty
	// keeps the built-in default's extensions for that language.
	Extensions []string `json:"extensions,omitempty"`
	// Disabled skips the language without removing its entry.
	Disabled bool `json:"disabled,omitempty"`
}

// Scope selects which config file a write targets.
type Scope string

const (
	// ScopeGlobal targets ~/.agent/config.json.
	ScopeGlobal Scope = "global"
	// ScopeProject targets <project>/.agent/config.json, which overrides
	// the global file for that project.
	ScopeProject Scope = "project"
)

// defaultModel returns the preset model for a provider, if one is known.
func defaultModel(providerName string) string {
	if p, ok := catalog.Lookup(providerName); ok {
		return p.DefaultModel
	}
	return ""
}

// Path returns the global config file location, e.g.
// ~/.agent/config.json (see the home package for directory resolution).
func Path() (string, error) {
	return home.Path("config.json"), nil
}

// ProjectPath returns the project config file location.
func ProjectPath(projectDir string) string {
	return filepath.Join(projectDir, ".agent", "config.json")
}

// pathFor maps a scope to its file path.
func pathFor(scope Scope, projectDir string) (string, error) {
	if scope == ScopeProject {
		if projectDir == "" {
			return "", fmt.Errorf("project scope requires a project directory")
		}
		return ProjectPath(projectDir), nil
	}
	return Path()
}

// Load resolves configuration for the current working directory. It never
// fails on missing files — first-run works with pure env configuration.
func Load() (*Config, error) {
	wd, _ := os.Getwd()
	return LoadIn(wd)
}

// LoadIn resolves configuration with projectDir's config layered on top of
// the global one.
//
// Key flow: defaults → global file → project file (scalar fields override
// when set; provider profiles merge by name) → environment → named-profile
// resolution → model default.
func LoadIn(projectDir string) (*Config, error) {
	cfg := &Config{Provider: "deepseek", MaxTurns: 40}

	if path, err := Path(); err == nil {
		if err := mergeFile(cfg, path); err != nil {
			return nil, err
		}
	}
	if projectDir != "" {
		if err := mergeFile(cfg, ProjectPath(projectDir)); err != nil {
			return nil, err
		}
	}

	applyEnv(cfg)
	resolveProfile(cfg)
	applyPreset(cfg)

	if cfg.Model == "" {
		cfg.Model = defaultModel(cfg.Provider)
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 40
	}
	if cfg.AutoCompact == "" {
		cfg.AutoCompact = "on"
	}
	if cfg.ContextLimit <= 0 {
		if w := catalog.ContextWindow(cfg.Provider, cfg.Model); w > 0 {
			cfg.ContextLimit = w
		} else {
			cfg.ContextLimit = DefaultContextLimit
		}
	}
	if cfg.Theme == "" {
		cfg.Theme = theme.Default()
	}
	return cfg, nil
}

// DefaultContextLimit is the fallback context window (tokens) used when the
// config names no explicit limit and the model's window is unknown to the
// catalog. Known models are seeded from catalog.ContextWindow instead. It is
// deliberately conservative so compaction engages before a small-window model
// rejects the request; override it via "context_limit" when needed.
const DefaultContextLimit = 128000

// mergeFile overlays one config file onto cfg. Scalars replace only when
// present in the file; provider profiles merge by name so a project can add
// or override individual profiles without repeating the global map.
func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // a missing layer is not an error
	}
	var layer Config
	if err := json.Unmarshal(data, &layer); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if layer.Provider != "" {
		cfg.Provider = layer.Provider
	}
	if layer.Model != "" {
		cfg.Model = layer.Model
	}
	if layer.APIKey != "" {
		cfg.APIKey = layer.APIKey
	}
	if layer.BaseURL != "" {
		cfg.BaseURL = layer.BaseURL
	}
	if layer.Format != "" {
		cfg.Format = layer.Format
	}
	if layer.MaxTurns > 0 {
		cfg.MaxTurns = layer.MaxTurns
	}
	if layer.PermissionMode != "" {
		cfg.PermissionMode = layer.PermissionMode
	}
	if layer.BashPolicy != "" {
		cfg.BashPolicy = layer.BashPolicy
	}
	if layer.Sandbox != "" {
		cfg.Sandbox = layer.Sandbox
	}
	if layer.SandboxDenyNetwork {
		cfg.SandboxDenyNetwork = true
	}
	// Permission rules accumulate across layers (project rules extend global
	// ones), project appended after global so they take later precedence when
	// the gate wants project-specific overrides first (it prepends session
	// choices separately).
	cfg.PermissionRules = append(cfg.PermissionRules, layer.PermissionRules...)
	if layer.GoalMaxRounds > 0 {
		cfg.GoalMaxRounds = layer.GoalMaxRounds
	}
	if layer.Thinking != "" {
		cfg.Thinking = layer.Thinking
	}
	if layer.PromptCache != "" {
		cfg.PromptCache = layer.PromptCache
	}
	if layer.VisionProvider != "" {
		cfg.VisionProvider = layer.VisionProvider
	}
	if layer.VisionModel != "" {
		cfg.VisionModel = layer.VisionModel
	}
	if layer.AutoCompact != "" {
		cfg.AutoCompact = layer.AutoCompact
	}
	if layer.Theme != "" {
		cfg.Theme = layer.Theme
	}
	if layer.ContextLimit > 0 {
		cfg.ContextLimit = layer.ContextLimit
	}
	for name, p := range layer.Providers {
		if cfg.Providers == nil {
			cfg.Providers = map[string]ProviderConfig{}
		}
		cfg.Providers[name] = p
	}
	for name, s := range layer.MCPServers {
		if cfg.MCPServers == nil {
			cfg.MCPServers = map[string]MCPServerConfig{}
		}
		cfg.MCPServers[name] = s
	}
	for name, s := range layer.LSPServers {
		if cfg.LSPServers == nil {
			cfg.LSPServers = map[string]LSPServerConfig{}
		}
		cfg.LSPServers[name] = s
	}
	for name, s := range layer.Subagents {
		if cfg.Subagents == nil {
			cfg.Subagents = map[string]SubagentConfig{}
		}
		cfg.Subagents[name] = s
	}
	// Hooks accumulate across layers per event, so a project can add hooks on
	// top of the global ones.
	for event, hooks := range layer.Hooks {
		if cfg.Hooks == nil {
			cfg.Hooks = map[string][]HookConfig{}
		}
		cfg.Hooks[event] = append(cfg.Hooks[event], hooks...)
	}
	for model, price := range layer.Prices {
		if cfg.Prices == nil {
			cfg.Prices = map[string]PriceConfig{}
		}
		cfg.Prices[model] = price
	}
	if layer.WebSearch.Provider != "" {
		cfg.WebSearch.Provider = layer.WebSearch.Provider
	}
	if layer.WebSearch.APIKey != "" {
		cfg.WebSearch.APIKey = layer.WebSearch.APIKey
	}
	if layer.WebSearch.EnvKey != "" {
		cfg.WebSearch.EnvKey = layer.WebSearch.EnvKey
	}
	return nil
}

// WebSearchKey resolves the web-search API key from the config or environment.
// It reads the explicit key, then the configured EnvKey, then the provider's
// standard variable (BRAVE_API_KEY / TAVILY_API_KEY).
func (c *Config) WebSearchKey() string {
	if c.WebSearch.APIKey != "" {
		return c.WebSearch.APIKey
	}
	if c.WebSearch.EnvKey != "" {
		if v := os.Getenv(c.WebSearch.EnvKey); v != "" {
			return v
		}
	}
	switch strings.ToLower(c.WebSearch.Provider) {
	case "brave":
		return os.Getenv("BRAVE_API_KEY")
	case "tavily":
		return os.Getenv("TAVILY_API_KEY")
	}
	return ""
}

// applyEnv overlays environment variables. AGENT_* wins over
// vendor-specific key variables.
func applyEnv(cfg *Config) {
	if v := os.Getenv("AGENT_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("AGENT_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("AGENT_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("AGENT_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("AGENT_FORMAT"); v != "" {
		cfg.Format = v
	}
	if cfg.APIKey == "" {
		cfg.APIKey = envKeyFor(cfg.Provider, cfg.Format)
	}
}

// envKeyFor reads the first non-empty credential environment variable the
// provider's endpoint declares, so a vendor's conventional variable works
// with no configuration at all. The format selects the endpoint: a vendor's
// Anthropic wire looks at ANTHROPIC_AUTH_TOKEN before the vendor's own key.
func envKeyFor(providerName, format string) string {
	p, ok := catalog.Lookup(providerName)
	if !ok {
		return ""
	}
	ep, _ := p.Endpoint(format)
	for _, key := range ep.EnvKeys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

// resolveProfile fills connection settings from a named provider profile
// when cfg.Provider matches one; explicit top-level values keep precedence.
// staleForProvider reports whether a top-level model belongs to a *different*
// provider than the active one — i.e. it is left over from a previous provider
// and would be rejected here. A model is stale when it is another configured
// profile's model, or a known model of a different built-in provider that this
// provider does not list.
func staleForProvider(cfg *Config, model string) bool {
	if model == "" {
		return false
	}
	for name, prof := range cfg.Providers {
		if name != cfg.Provider && prof.Model == model {
			return true
		}
	}
	own := catalog.ModelsFor(cfg.Provider)
	for _, m := range own {
		if m == model {
			return false // it is one of this provider's own models
		}
	}
	if p, ok := catalog.ProviderForModel(model); ok && p != cfg.Provider && len(own) > 0 {
		return true
	}
	return false
}

func resolveProfile(cfg *Config) {
	p, ok := cfg.Providers[cfg.Provider]
	if !ok {
		return
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = p.BaseURL
	}
	// A stale top-level model from a previous provider is replaced by this
	// profile's own model so the endpoint isn't sent a foreign model id.
	if p.Model != "" && staleForProvider(cfg, cfg.Model) {
		cfg.Model = p.Model
	}
	if cfg.Model == "" {
		cfg.Model = p.Model
	}
	if cfg.APIKey == "" {
		cfg.APIKey = p.APIKey
	}
	if cfg.APIKey == "" && p.EnvKey != "" {
		cfg.APIKey = os.Getenv(p.EnvKey)
	}
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// applyPreset fills unset connection settings from the built-in catalog.
//
// Key flow: it runs after the user's own profile so explicit configuration
// always wins; the preset only supplies what is still blank. This is what
// lets `provider: glm` work with nothing else configured.
func applyPreset(cfg *Config) {
	// A user-defined profile of the same name shadows the preset entirely.
	if _, shadowed := cfg.Providers[cfg.Provider]; shadowed {
		return
	}
	p, wire, ok := catalog.Resolve(cfg.Provider)
	if !ok {
		return
	}
	// Canonicalize aliases ("glm" → "zai") so downstream lookups agree. A
	// legacy "<vendor>-anthropic" name resolves to the vendor plus its
	// Anthropic wire, which is what that name used to mean.
	cfg.Provider = p.Name
	if wire != "" {
		cfg.Format = wire
	}
	// A format the vendor does not serve (e.g. "anthropic" left over from a
	// previous provider) falls back to its primary wire rather than failing.
	ep, served := p.Endpoint(cfg.Format)
	if !served {
		cfg.Format = ""
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = ep.BaseURL
	}
	// Drop a stale model left over from a different provider.
	if staleForProvider(cfg, cfg.Model) {
		cfg.Model = ""
	}
	if cfg.Model == "" {
		cfg.Model = p.DefaultModel
	}
	if cfg.APIKey == "" {
		cfg.APIKey = envKeyFor(p.Name, cfg.Format)
	}
}

// IsNamedProfile reports whether name refers to a providers-map entry
// rather than a built-in vendor.
func (c *Config) IsNamedProfile(name string) bool {
	_, ok := c.Providers[name]
	return ok
}

// LoadFor resolves configuration like Load, then re-targets it at the given
// provider. Used for mid-session provider switching: file/env settings
// bound to the previous provider are dropped and re-resolved so a stale key
// or endpoint is never sent to the new vendor.
func LoadFor(providerName string) (*Config, error) {
	return LoadForWire(providerName, "")
}

// LoadForWire is LoadFor with an explicit wire format for vendors that serve
// more than one ("anthropic" selects the Claude-Code endpoint). An empty
// format uses the vendor's primary wire.
func LoadForWire(providerName, format string) (*Config, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	if providerName == "" || (providerName == cfg.Provider && format == cfg.Format) {
		return cfg, nil
	}
	cfg.Provider = providerName
	cfg.Format = format
	cfg.Model = defaultModel(providerName)
	cfg.BaseURL = os.Getenv("AGENT_BASE_URL")
	cfg.APIKey = os.Getenv("AGENT_API_KEY")
	if cfg.APIKey == "" {
		cfg.APIKey = envKeyFor(providerName, format)
	}
	resolveProfile(cfg)
	applyPreset(cfg)
	return cfg, nil
}

// BuildProvider constructs the LLM client for this configuration. Named
// profiles (providers-map entries) become OpenAI-compatible clients under
// their profile name; everything else goes through the built-in registry.
func (c *Config) BuildProvider() (provider.Provider, error) {
	pc := provider.Config{APIKey: c.APIKey, BaseURL: c.BaseURL, Model: c.Model, Thinking: c.Thinking, PromptCache: c.PromptCache}
	if p, ok := c.Providers[c.Provider]; ok {
		pc.Auth = p.Auth
		return provider.NewProfile(c.Provider, p.Format, pc)
	}
	if p, wire, ok := catalog.Resolve(c.Provider); ok {
		// A legacy "<vendor>-anthropic" name carries its own wire, so it
		// still selects the Anthropic endpoint without a "format" key.
		ep, _ := p.Endpoint(orDefault(wire, c.Format))
		// Naming the exact variable to export turns the most common
		// setup failure into a one-line fix. The genuine Anthropic provider
		// (AuthAPIKey) is exempt because it has its own credential paths, but
		// third-party Anthropic-wire endpoints that authenticate with a bearer
		// token still require one.
		if c.APIKey == "" && (ep.Format != provider.FormatAnthropic || ep.Auth == provider.AuthBearer) {
			return nil, fmt.Errorf("provider %s needs a credential: export %s (get one at %s)",
				p.Name, strings.Join(ep.EnvKeys, " or "), orDefault(p.Notes, "the vendor console"))
		}
		// A preset addressed over a non-primary wire, or one that is not a
		// registered built-in, is built like a profile from its endpoint's
		// wire format and auth style.
		if ep.Format != p.Format || !provider.IsRegistered(p.Name) {
			pc.Auth = ep.Auth
			if pc.BaseURL == "" {
				pc.BaseURL = ep.BaseURL
			}
			return provider.NewProfile(p.Name, ep.Format, pc)
		}
	}
	return provider.New(c.Provider, pc)
}

// ModelSupportsVision reports whether the active provider/model can accept
// image input: either the model name is recognized as a vision family, or
// the active named profile is explicitly marked "vision": true.
func (c *Config) ModelSupportsVision() bool {
	if p, ok := c.Providers[c.Provider]; ok && p.Vision {
		return true
	}
	if p, ok := catalog.Lookup(c.Provider); ok && p.Vision {
		return true
	}
	return provider.SupportsVision(c.Model)
}

// validKeys documents the keys accepted by SetScoped, with validators where
// needed.
var validKeys = map[string]func(string) error{
	"provider": nil,
	"model":    nil,
	"api_key":  nil,
	"base_url": nil,
	"format": func(v string) error {
		if v != "" && v != provider.FormatOpenAI && v != provider.FormatAnthropic {
			return fmt.Errorf("must be %s or %s, got %q", provider.FormatOpenAI, provider.FormatAnthropic, v)
		}
		return nil
	},
	"max_turns":       validatePositiveInt,
	"goal_max_rounds": validatePositiveInt,
	"vision_provider": nil,
	"vision_model":    nil,
	"context_limit":   validatePositiveInt,
	"bash_policy": func(v string) error {
		if v != "standard" && v != "strict" {
			return fmt.Errorf("must be standard or strict, got %q", v)
		}
		return nil
	},
	"sandbox": func(v string) error {
		if v != "off" && v != "on" && v != "auto" {
			return fmt.Errorf("must be off, on, or auto, got %q", v)
		}
		return nil
	},
	"auto_compact": func(v string) error {
		if v != "on" && v != "off" {
			return fmt.Errorf("must be on or off, got %q", v)
		}
		return nil
	},
	"thinking": func(v string) error {
		if _, ok := provider.ParseEffort(v); !ok {
			return fmt.Errorf("must be one of off, low, medium, high, adaptive, got %q", v)
		}
		return nil
	},
	"permission_mode": func(v string) error {
		if v != "hitl" && v != "bypass" {
			return fmt.Errorf("must be hitl or bypass, got %q", v)
		}
		return nil
	},
	"theme": func(v string) error {
		if !theme.Has(v) {
			return fmt.Errorf("unknown theme %q; choose one of %s", v, strings.Join(theme.Names(), ", "))
		}
		return nil
	},
}

// Keys returns the settable configuration keys in display order.
func Keys() []string {
	return []string{"provider", "model", "api_key", "base_url", "max_turns", "permission_mode", "goal_max_rounds", "vision_provider", "vision_model", "thinking", "auto_compact", "context_limit", "bash_policy", "sandbox", "theme"}
}

// Set persists one field to the global config file (backwards-compatible
// wrapper around SetScoped).
func Set(key, value string) error {
	return SetScoped(ScopeGlobal, "", key, value)
}

// SetScoped persists one field to the chosen scope's file. Only file-backed
// values are touched — never environment-resolved ones — so a set cannot
// accidentally freeze a value that came from the shell.
func SetScoped(scope Scope, projectDir, key, value string) error {
	validate, ok := validKeys[key]
	if !ok {
		return fmt.Errorf("unknown config key %q (valid: %v)", key, Keys())
	}
	if validate != nil {
		if err := validate(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}

	path, err := pathFor(scope, projectDir)
	if err != nil {
		return err
	}
	cfg := &Config{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("parse existing config: %w", err)
		}
	}
	switch key {
	case "provider":
		cfg.Provider = value
	case "model":
		cfg.Model = value
	case "api_key":
		cfg.APIKey = value
	case "base_url":
		cfg.BaseURL = value
	case "format":
		cfg.Format = value
	case "max_turns":
		fmt.Sscanf(value, "%d", &cfg.MaxTurns)
	case "goal_max_rounds":
		fmt.Sscanf(value, "%d", &cfg.GoalMaxRounds)
	case "permission_mode":
		cfg.PermissionMode = value
	case "vision_provider":
		cfg.VisionProvider = value
	case "vision_model":
		cfg.VisionModel = value
	case "thinking":
		cfg.Thinking = value
	case "auto_compact":
		cfg.AutoCompact = value
	case "context_limit":
		fmt.Sscanf(value, "%d", &cfg.ContextLimit)
	case "bash_policy":
		cfg.BashPolicy = value
	case "sandbox":
		cfg.Sandbox = value
	case "theme":
		cfg.Theme = value
	}
	return cfg.saveTo(path)
}

func validatePositiveInt(s string) error {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return fmt.Errorf("must be a positive integer, got %q", s)
	}
	return nil
}

// Save writes the global config file, creating ~/.agent if needed.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	return c.saveTo(path)
}

// SaveProviderKey persists an API key for a provider as a named profile in the
// chosen scope, filling the connection details (base URL, wire format, auth)
// from the built-in catalog so a bare key is enough to reconnect next time.
// The format selects which of a vendor's endpoints the profile is pinned to
// (empty = its primary wire). Existing profile fields are preserved.
func SaveProviderKey(scope Scope, projectDir, name, format, key string) error {
	path, err := pathFor(scope, projectDir)
	if err != nil {
		return err
	}
	cfg := &Config{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("parse existing config: %w", err)
		}
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	prof := cfg.Providers[name]
	prof.APIKey = key
	if p, wire, ok := catalog.Resolve(name); ok {
		ep, _ := p.Endpoint(orDefault(wire, format))
		if prof.BaseURL == "" {
			prof.BaseURL = ep.BaseURL
		}
		if prof.Format == "" {
			prof.Format = ep.Format
		}
		if prof.Auth == "" {
			prof.Auth = ep.Auth
		}
		if prof.Model == "" {
			prof.Model = p.DefaultModel
		}
	}
	cfg.Providers[name] = prof
	return cfg.saveTo(path)
}

func (c *Config) saveTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file may contain an API key.
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
