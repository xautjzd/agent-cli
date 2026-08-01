package repl

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

type replAuthAdapter struct{}

func (replAuthAdapter) ID() string          { return "test" }
func (replAuthAdapter) DisplayName() string { return "Test Subscription" }
func (replAuthAdapter) Methods() []providerAuth.LoginMethod {
	return []providerAuth.LoginMethod{{ID: "automatic", Label: "Automatic", Description: "Test login"}}
}
func (replAuthAdapter) Login(context.Context, providerAuth.LoginRequest, providerAuth.LoginUI) (providerAuth.Credential, error) {
	data, _ := json.Marshal(map[string]string{"token": "test-token"})
	return providerAuth.NewCredential(providerAuth.CredentialOAuth, nil, data), nil
}
func (replAuthAdapter) Resolve(context.Context, providerAuth.Credential) (providerAuth.ResolvedAuth, error) {
	return providerAuth.ResolvedAuth{Secret: "test-token"}, nil
}
func (replAuthAdapter) Describe(credential providerAuth.Credential) providerAuth.Status {
	return providerAuth.Status{Type: credential.Type, Account: "person@example.com", Plan: "pro"}
}
func (replAuthAdapter) Usage(context.Context, providerAuth.ResolvedAuth) (providerAuth.UsageSnapshot, error) {
	used := 42
	return providerAuth.UsageSnapshot{Plan: "pro", Limits: []providerAuth.UsageLimit{{Name: "primary", UsedPercent: &used}}}, nil
}

func TestInteractiveAuthCommandsShareService(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	registry := providerAuth.NewRegistry()
	if err := registry.Register(replAuthAdapter{}); err != nil {
		t.Fatal(err)
	}
	r.Cfg.AuthService = providerAuth.NewService(registry, providerAuth.NewStore(filepath.Join(t.TempDir(), "auth.json")))
	r.Cfg.Provider = "test"

	if err := r.dispatch(context.Background(), "/auth test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not logged in") {
		t.Fatalf("initial auth output = %q", out.String())
	}

	out.Reset()
	if err := r.dispatch(context.Background(), "/login test automatic"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Logged in") {
		t.Fatalf("login output = %q", out.String())
	}

	out.Reset()
	if err := r.dispatch(context.Background(), "/usage test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Subscription · test · pro") || !strings.Contains(out.String(), "42% used") {
		t.Fatalf("usage output = %q", out.String())
	}

	out.Reset()
	if err := r.dispatch(context.Background(), "/logout test"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Logged out") {
		t.Fatalf("logout output = %q", out.String())
	}
}

func TestLoginWithoutProviderAlwaysShowsProviderPicker(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	registry := providerAuth.NewRegistry()
	if err := registry.Register(replAuthAdapter{}); err != nil {
		t.Fatal(err)
	}
	r.Cfg.AuthService = providerAuth.NewService(registry, providerAuth.NewStore(filepath.Join(t.TempDir(), "auth.json")))

	selected := false
	r.tuiSelect = func(title string, items []pickerItem) (int, bool) {
		selected = true
		if title != "Select provider" || len(items) != 1 || !strings.Contains(items[0].label, "Test Subscription") {
			t.Fatalf("provider picker = %q, %#v", title, items)
		}
		return 0, true
	}
	if err := r.dispatch(context.Background(), "/login"); err != nil {
		t.Fatal(err)
	}
	if !selected || !strings.Contains(out.String(), "Logged in") {
		t.Fatalf("selected=%v output=%q", selected, out.String())
	}
}

func TestUsageKeepsLocalOutputWhenManagedAuthUnavailable(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Cfg.AuthService = nil
	if err := r.dispatch(context.Background(), "/usage"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "This session") || strings.Contains(out.String(), "unavailable") {
		t.Fatalf("usage output = %q", out.String())
	}
}
