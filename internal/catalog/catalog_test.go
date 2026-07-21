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

func TestLookupByNameAndAlias(t *testing.T) {
	p, ok := Lookup("glm")
	if !ok || p.Name != "glm" {
		t.Fatalf("Lookup(glm) = %+v, %v", p, ok)
	}
	// Aliases resolve to the same canonical preset.
	for _, alias := range []string{"zhipu", "bigmodel", "ZHIPU", "  glm  "} {
		got, ok := Lookup(alias)
		if !ok || got.Name != "glm" {
			t.Errorf("Lookup(%q) = %+v, %v; want the glm preset", alias, got, ok)
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
	for _, want := range []string{"openai", "anthropic", "deepseek", "glm", "kimi", "dashscope"} {
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
