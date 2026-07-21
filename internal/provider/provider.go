package provider

import (
	"context"
	"fmt"
	"sort"
)

// Provider is the single abstraction the agent core talks to. Implementations
// must be safe for concurrent use.
type Provider interface {
	// Name returns the provider identifier, e.g. "openai" or "deepseek".
	Name() string
	// Chat performs one blocking chat completion round-trip.
	Chat(ctx context.Context, req Request) (*Response, error)
}

// Delta is one increment of a streamed completion.
type Delta struct {
	// Content is a fragment of the visible answer.
	Content string
	// Reasoning is a fragment of chain-of-thought from reasoning models.
	Reasoning string
}

// Streamer is optionally implemented by providers that support incremental
// delivery (SSE). ChatStream invokes onDelta for every fragment as it
// arrives and still returns the fully assembled Response — tool calls,
// usage and all — so the caller's loop logic is identical to Chat.
type Streamer interface {
	ChatStream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
}

// Config carries the connection settings a factory needs to build a Provider.
type Config struct {
	APIKey  string
	BaseURL string // optional override; factories supply vendor defaults
	Model   string
	// Thinking selects extended-thinking behavior for providers that
	// support it ("off" disables; empty means the provider default).
	Thinking string
	// Auth selects the credential header for Anthropic-format endpoints:
	// "bearer" sends Authorization: Bearer, anything else sends x-api-key.
	Auth string
}

// Factory builds a Provider from configuration.
type Factory func(cfg Config) (Provider, error)

// registry maps provider names to factories. Adding a vendor is a pure
// extension: register a factory, no existing code changes (OCP).
var registry = map[string]Factory{}

// Register makes a provider factory available under the given name.
func Register(name string, f Factory) {
	registry[name] = f
}

// New builds a provider by name, returning a descriptive error listing the
// supported names when the lookup fails.
func New(name string, cfg Config) (Provider, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (supported: %v)", name, Names())
	}
	return f(cfg)
}

// Wire formats a named configuration profile can declare.
const (
	FormatOpenAI    = "openai"
	FormatAnthropic = "anthropic"
)

// Credential presentation styles for Anthropic-format endpoints.
const (
	AuthAPIKey = "api_key"
	AuthBearer = "bearer"
)

// NewProfile builds a provider for a named configuration profile. The
// format selects the wire protocol spoken at the profile's endpoint,
// defaulting to the OpenAI-compatible one; this is what lets a profile
// point at a third-party Anthropic-compatible gateway.
func NewProfile(name, format string, cfg Config) (Provider, error) {
	// The format is validated before anything else so a typo reports the
	// typo rather than a downstream missing-base_url error.
	switch format {
	case "", FormatOpenAI, FormatAnthropic:
	default:
		return nil, fmt.Errorf("provider profile %q: unknown format %q (use %q or %q)",
			name, format, FormatOpenAI, FormatAnthropic)
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("provider profile %q requires base_url", name)
	}
	if format == FormatAnthropic {
		return NewAnthropic(name, cfg)
	}
	return NewNamed(name, cfg)
}

// IsRegistered reports whether a name has a built-in client factory.
func IsRegistered(name string) bool {
	_, ok := registry[name]
	return ok
}

// Names returns the sorted list of registered provider names.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
