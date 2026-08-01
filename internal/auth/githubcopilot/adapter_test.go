package githubcopilot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

type testUI struct{ secret string }

func (testUI) Select(context.Context, string, []providerAuth.LoginMethod) (string, error) {
	return "github_cli", nil
}
func (testUI) OpenURL(context.Context, string) error          { return nil }
func (testUI) Prompt(context.Context, string) (string, error) { return "", nil }
func (testUI) Notify(string)                                  {}
func (u testUI) PromptSecret(context.Context, string) (string, error) {
	return u.secret, nil
}

func TestGitHubCLIIsResolvedJustInTime(t *testing.T) {
	var calls int
	a := New()
	a.Run = func(_ context.Context, args ...string) ([]byte, error) {
		calls++
		if strings.Join(args, " ") == "api user --jq .login" {
			return []byte("octocat\n"), nil
		}
		return []byte("secret-from-gh\n"), nil
	}
	credential, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "github_cli"}, testUI{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(credential.Data), "secret-from-gh") {
		t.Fatal("GitHub CLI token was copied into the agent credential store")
	}
	resolved, err := a.Resolve(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Secret != "secret-from-gh" || resolved.Properties["account"] != "octocat" || calls != 3 {
		t.Fatalf("resolved=%+v calls=%d", resolved, calls)
	}
}

func TestTokenLoginRequiresSecretPromptAndNeverDescribesToken(t *testing.T) {
	a := New()
	credential, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "token"}, testUI{secret: "github_pat_secret"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := a.Resolve(context.Background(), credential)
	if err != nil || resolved.Secret != "github_pat_secret" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	status := a.Describe(credential)
	if strings.Contains(status.Account+status.Plan+status.Name, "github_pat_secret") {
		t.Fatal("status leaked token")
	}
}

func TestResolveRejectsUnknownCredentialSource(t *testing.T) {
	credential := providerAuth.NewCredential(providerAuth.CredentialAPIKey, nil, []byte(`{"source":"other","token":"secret"}`))
	_, err := New().Resolve(context.Background(), credential)
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("err=%v", err)
	}
}

func TestGitHubCLILoginRejectsCredentialThatGitHubCannotValidate(t *testing.T) {
	a := New()
	a.Run = func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Join(args, " ") == "api user --jq .login" {
			return nil, fmt.Errorf("authentication failed")
		}
		return []byte("stale-local-token"), nil
	}
	_, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "github_cli"}, testUI{})
	if err == nil || !strings.Contains(err.Error(), "credential is invalid") {
		t.Fatalf("err=%v", err)
	}
}
