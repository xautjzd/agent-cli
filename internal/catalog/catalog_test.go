package catalog

import (
	"strings"
	"testing"

	"github.com/xautjzd/agent-cli/internal/provider"
)

func TestPresetsAreWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, p := range All() {
		if p.Name == "" || p.Label == "" {
			t.Errorf("preset missing name or label: %+v", p)
		}
		if p.DefaultModel == "" {
			t.Errorf("%s: no default model", p.Name)
		}
		if len(p.EnvKeys) == 0 {
			t.Errorf("%s: no credential environment variable", p.Name)
		}
		switch p.Format {
		case provider.FormatOpenAI, provider.FormatAnthropic:
		default:
			t.Errorf("%s: invalid format %q", p.Name, p.Format)
		}
		// Only Anthropic-format presets need an auth style; the OpenAI
		// client always uses a bearer token.
		if p.Auth != "" && p.Format != provider.FormatAnthropic {
			t.Errorf("%s: auth style set on a non-Anthropic preset", p.Name)
		}
		// The default model should appear in the advisory list.
		if len(p.Models) > 0 {
			found := false
			for _, m := range p.Models {
				if m == p.DefaultModel {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: default model %q is not in its own model list", p.Name, p.DefaultModel)
			}
		}
		// Every name and alias must be unique across the catalog.
		for _, key := range append([]string{p.Name}, p.Aliases...) {
			if prev, dup := seen[key]; dup {
				t.Errorf("name %q is claimed by both %s and %s", key, prev, p.Name)
			}
			seen[key] = p.Name
		}
	}
}

func TestAnthropicFormatPresetsHaveEndpoints(t *testing.T) {
	for _, p := range All() {
		// Only first-party Anthropic may rely on the SDK's built-in URL;
		// any other Anthropic-format preset is a gateway and needs one.
		if p.Format == provider.FormatAnthropic && p.Name != "anthropic" && p.BaseURL == "" {
			t.Errorf("%s: Anthropic-compatible gateway without a base URL", p.Name)
		}
		// Third-party gateways expect a bearer token, not x-api-key.
		if p.Format == provider.FormatAnthropic && p.Name != "anthropic" && p.Auth != provider.AuthBearer {
			t.Errorf("%s: gateway should use bearer auth, got %q", p.Name, p.Auth)
		}
	}
}

// A vendor's second wire is an endpoint of the same provider, so it must
// serve the vendor's own models over a bearer-authenticated gateway.
func TestAnthropicWireIsAnEndpointOfTheSameVendor(t *testing.T) {
	for _, p := range All() {
		if p.AnthropicBaseURL == "" {
			if _, ok := p.Endpoint(provider.FormatAnthropic); ok && p.Format != provider.FormatAnthropic {
				t.Errorf("%s: reports an anthropic wire it does not serve", p.Name)
			}
			continue
		}
		ep, ok := p.Endpoint(provider.FormatAnthropic)
		if !ok {
			t.Fatalf("%s: anthropic wire not served", p.Name)
		}
		if ep.BaseURL != p.AnthropicBaseURL || ep.Format != provider.FormatAnthropic {
			t.Errorf("%s: wrong anthropic endpoint %+v", p.Name, ep)
		}
		if ep.Auth != provider.AuthBearer {
			t.Errorf("%s: gateway should use bearer auth, got %q", p.Name, ep.Auth)
		}
		if len(ep.EnvKeys) == 0 || ep.EnvKeys[0] != anthropicTokenEnv {
			t.Errorf("%s: anthropic wire should try %s first, got %v", p.Name, anthropicTokenEnv, ep.EnvKeys)
		}
		// The primary wire is unchanged by asking for the other one.
		if def, _ := p.Endpoint(""); def.BaseURL != p.BaseURL || def.Format != p.Format {
			t.Errorf("%s: primary endpoint changed: %+v", p.Name, def)
		}
	}
}

// The vendor and its Anthropic endpoint used to be two presets
// ("deepseek" / "deepseek-anthropic"). The old names still resolve — to the
// one vendor, on the wire that name meant.
func TestLegacyAnthropicNamesResolveToTheVendorWire(t *testing.T) {
	for _, legacy := range []string{"deepseek-anthropic", "zai-anthropic", "glm-anthropic", "kimi-anthropic"} {
		p, wire, ok := Resolve(legacy)
		if !ok {
			t.Errorf("Resolve(%q) failed; legacy names must keep working", legacy)
			continue
		}
		if wire != provider.FormatAnthropic {
			t.Errorf("Resolve(%q) wire = %q, want anthropic", legacy, wire)
		}
		if p.AnthropicBaseURL == "" {
			t.Errorf("Resolve(%q) → %s, which has no anthropic endpoint", legacy, p.Name)
		}
	}
	// A suffix on a vendor without that endpoint is not a provider.
	if _, _, ok := Resolve("openai-anthropic"); ok {
		t.Error("openai has no anthropic endpoint; the legacy name should not resolve")
	}
	// The vendors are one entry each now, not two.
	for _, name := range Names() {
		if strings.HasSuffix(name, "-anthropic") && name != "anthropic" {
			t.Errorf("%q is still a separate preset; wires belong to one vendor entry", name)
		}
	}
}

func TestLookupByNameAndAlias(t *testing.T) {
	p, ok := Lookup("zai")
	if !ok || p.Name != "zai" {
		t.Fatalf("Lookup(zai) = %+v, %v", p, ok)
	}
	// Aliases — including the pre-rename spellings — resolve to the same
	// canonical preset.
	for _, alias := range []string{"glm", "zhipu", "bigmodel", "z.ai", "ZHIPU", "  glm  "} {
		got, ok := Lookup(alias)
		if !ok || got.Name != "zai" {
			t.Errorf("Lookup(%q) = %+v, %v; want the zai preset", alias, got, ok)
		}
	}
	if _, ok := Lookup("no-such-provider"); ok {
		t.Error("unknown provider should not resolve")
	}
}

func TestNamesAndModels(t *testing.T) {
	names := Names()
	if len(names) < 8 {
		t.Errorf("expected a useful catalog, got %d entries", len(names))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("Names() not sorted at %d: %q > %q", i, names[i-1], names[i])
		}
	}
	// Models are advisory but must be present for completion to help.
	if len(ModelsFor("deepseek")) == 0 {
		t.Error("deepseek has no listed models")
	}
	if ModelsFor("unknown") != nil {
		t.Error("unknown provider should have no models")
	}
}

func TestCatalogCoversRequestedVendors(t *testing.T) {
	// The vendors this CLI is expected to configure out of the box.
	for _, want := range []string{"openai", "anthropic", "deepseek", "zai", "glm", "kimi", "dashscope", "xai", "grok", "google", "gemini"} {
		if _, ok := Lookup(want); !ok {
			t.Errorf("catalog is missing %q", want)
		}
	}
}

func TestAllReturnsACopy(t *testing.T) {
	// Callers must not be able to corrupt the shared table.
	a := All()
	if len(a) == 0 {
		t.Fatal("empty catalog")
	}
	original := a[0].Name
	a[0].Name = "mutated"
	if All()[0].Name != original {
		t.Error("All() exposed the underlying table")
	}
	if strings.Contains(strings.Join(Names(), ","), "mutated") {
		t.Error("mutation leaked into Names()")
	}
}

func TestContextWindowResolution(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		model    string
		want     int
	}{
		{"anthropic family 1M", "anthropic", "claude-opus-4-8", 1_000_000},
		{"haiku override", "anthropic", "claude-haiku-4-5", 200_000},
		{"anthropic-compatible inherits deepseek 1M", "deepseek-anthropic", "deepseek-v4-flash", 1_000_000},
		{"kimi family default", "kimi", "kimi-k2.6", 256_000},
		{"kimi k3 override to 1M", "kimi", "kimi-k3", 1_000_000},
		{"moonshot override narrower", "kimi", "moonshot-v1-128k", 128_000},
		{"openai GPT-5 family 1M", "openai", "gpt-5.6-sol", 1_000_000},
		{"qwen flagship override", "dashscope", "qwen3.7-max", 1_000_000},
		{"qwen-max family default", "dashscope", "qwen-max", 256_000},
		{"grok override", "openrouter", "x-ai/grok-4.5", 500_000},
		{"openrouter namespaced model wins over aggregator default", "openrouter", "anthropic/claude-opus-4-8", 1_000_000},
		{"openrouter falls back to family default", "openrouter", "openai/gpt-5.6", 128_000},
		{"model signals its own family regardless of provider", "openai", "claude-opus-4-8", 1_000_000},
		{"unknown model on known provider uses family", "deepseek", "deepseek-v99", 1_000_000},
		{"unknown provider and model", "mystery-gateway", "mystery-model", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ContextWindow(c.provider, c.model); got != c.want {
				t.Errorf("ContextWindow(%q, %q) = %d, want %d", c.provider, c.model, got, c.want)
			}
		})
	}
}
