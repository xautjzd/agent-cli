package config

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

type configAuthAdapter struct{}

func (configAuthAdapter) ID() string          { return "openai" }
func (configAuthAdapter) DisplayName() string { return "OpenAI" }
func (configAuthAdapter) Methods() []providerAuth.LoginMethod {
	return []providerAuth.LoginMethod{{ID: "test", Label: "Test"}}
}
func (configAuthAdapter) Login(context.Context, providerAuth.LoginRequest, providerAuth.LoginUI) (providerAuth.Credential, error) {
	return providerAuth.Credential{}, fmt.Errorf("not used")
}
func (configAuthAdapter) Resolve(context.Context, providerAuth.Credential) (providerAuth.ResolvedAuth, error) {
	return providerAuth.ResolvedAuth{Secret: "subscription-token", Properties: map[string]string{"account_id": "acct"}}, nil
}

func configAuthService(t *testing.T) *providerAuth.Service {
	t.Helper()
	registry := providerAuth.NewRegistry()
	if err := registry.Register(configAuthAdapter{}); err != nil {
		t.Fatal(err)
	}
	store := providerAuth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	data, _ := json.Marshal(map[string]string{"test": "credential"})
	if err := store.Set(context.Background(), "openai", providerAuth.NewCredential(providerAuth.CredentialOAuth, nil, data)); err != nil {
		t.Fatal(err)
	}
	return providerAuth.NewService(registry, store)
}

func TestBuildProviderUsesManagedOpenAILoginAfterExplicitKey(t *testing.T) {
	service := configAuthService(t)
	cfg := &Config{Provider: "openai", Model: "gpt-5", AuthService: service}
	p, err := cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%T", p); !strings.Contains(got, "openAICodex") {
		t.Fatalf("provider type = %s; want subscription transport", got)
	}

	cfg.APIKey = "explicit-api-key"
	p, err = cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%T", p); strings.Contains(got, "openAICodex") {
		t.Fatalf("provider type = %s; explicit API key must win", got)
	}
}

func TestBuildProviderNeverSendsManagedLoginToCustomBaseURL(t *testing.T) {
	cfg := &Config{
		Provider:    "openai",
		Model:       "gpt-5",
		BaseURL:     "https://gateway.example/v1",
		AuthService: configAuthService(t),
	}
	if _, err := cfg.BuildProvider(); err == nil || !strings.Contains(err.Error(), "needs a credential") {
		t.Fatalf("BuildProvider error = %v; want explicit credential requirement", err)
	}
}

func TestBuildGitHubCopilotProviderFromEnvironmentToken(t *testing.T) {
	cfg := &Config{Provider: "copilot", Model: "auto", APIKey: "github-token"}
	p, err := cfg.BuildProvider()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "github-copilot" {
		t.Fatalf("provider name = %q", p.Name())
	}
}

func TestBuildGitHubCopilotRequiresLogin(t *testing.T) {
	registry := providerAuth.NewRegistry()
	store := providerAuth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	cfg := &Config{Provider: "github-copilot", Model: "auto", AuthService: providerAuth.NewService(registry, store)}
	_, err := cfg.BuildProvider()
	if err == nil || !strings.Contains(err.Error(), "auth login github-copilot") {
		t.Fatalf("BuildProvider error = %v", err)
	}
}
