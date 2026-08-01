// Package githubcopilot authenticates the GitHub Copilot provider without
// taking ownership of GitHub CLI credentials. Tokens are resolved just in
// time and are never rendered or logged.
package githubcopilot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

const ProviderID = "github-copilot"

type credentialData struct {
	Source  string `json:"source"`
	Token   string `json:"token,omitempty"`
	Account string `json:"account,omitempty"`
}

// Adapter supports credentials already managed by gh and manually supplied
// GitHub tokens. The former remains in the system credential store owned by gh.
type Adapter struct {
	Run func(context.Context, ...string) ([]byte, error)
}

func New() *Adapter { return &Adapter{} }

func (*Adapter) ID() string          { return ProviderID }
func (*Adapter) DisplayName() string { return "GitHub Copilot" }
func (*Adapter) Methods() []providerAuth.LoginMethod {
	return []providerAuth.LoginMethod{
		{ID: "github_cli", Label: "GitHub CLI", Description: "Use the account already signed in with gh"},
		{ID: "token", Label: "GitHub token", Description: "Use a token authorized for Copilot Requests"},
	}
}

func (a *Adapter) run(ctx context.Context, args ...string) ([]byte, error) {
	if a.Run != nil {
		return a.Run(ctx, args...)
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("GitHub CLI (gh) is not installed; install it and run \"gh auth login\", or choose the token method")
	}
	return exec.CommandContext(ctx, "gh", args...).Output()
}

func (a *Adapter) Login(ctx context.Context, req providerAuth.LoginRequest, ui providerAuth.LoginUI) (providerAuth.Credential, error) {
	method := req.Method
	if method == "" {
		selected, err := ui.Select(ctx, "Select GitHub Copilot login method", a.Methods())
		if err != nil {
			return providerAuth.Credential{}, err
		}
		method = selected
	}

	data := credentialData{Source: method}
	switch method {
	case "github_cli":
		token, err := a.run(ctx, "auth", "token", "--hostname", "github.com")
		if err != nil || strings.TrimSpace(string(token)) == "" {
			return providerAuth.Credential{}, fmt.Errorf("GitHub CLI is not logged in; run \"gh auth login --hostname github.com --web\" first")
		}
		account, err := a.run(ctx, "api", "user", "--jq", ".login")
		if err != nil || strings.TrimSpace(string(account)) == "" {
			return providerAuth.Credential{}, fmt.Errorf("GitHub CLI credential is invalid; run \"gh auth login --hostname github.com --web\" again")
		}
		data.Account = strings.TrimSpace(string(account))
	case "token":
		prompter, ok := ui.(providerAuth.SecretPrompter)
		if !ok {
			return providerAuth.Credential{}, fmt.Errorf("this login UI cannot securely read a GitHub token")
		}
		token, err := prompter.PromptSecret(ctx, "GitHub token")
		if err != nil {
			return providerAuth.Credential{}, err
		}
		data.Token = strings.TrimSpace(token)
		if data.Token == "" {
			return providerAuth.Credential{}, fmt.Errorf("GitHub token is empty")
		}
	default:
		return providerAuth.Credential{}, fmt.Errorf("GitHub Copilot does not support login method %q", method)
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return providerAuth.Credential{}, err
	}
	kind := providerAuth.CredentialOAuth
	if method == "token" {
		kind = providerAuth.CredentialAPIKey
	}
	return providerAuth.NewCredential(kind, nil, raw), nil
}

func (a *Adapter) Resolve(ctx context.Context, credential providerAuth.Credential) (providerAuth.ResolvedAuth, error) {
	var data credentialData
	if (credential.Type != providerAuth.CredentialOAuth && credential.Type != providerAuth.CredentialAPIKey) || json.Unmarshal(credential.Data, &data) != nil {
		return providerAuth.ResolvedAuth{}, fmt.Errorf("invalid GitHub Copilot credential")
	}
	var token string
	switch data.Source {
	case "github_cli":
		if credential.Type != providerAuth.CredentialOAuth {
			return providerAuth.ResolvedAuth{}, fmt.Errorf("invalid GitHub CLI credential type")
		}
		out, err := a.run(ctx, "auth", "token", "--hostname", "github.com")
		if err != nil {
			return providerAuth.ResolvedAuth{}, fmt.Errorf("read GitHub CLI credential: %w", err)
		}
		token = strings.TrimSpace(string(out))
	case "token":
		if credential.Type != providerAuth.CredentialAPIKey {
			return providerAuth.ResolvedAuth{}, fmt.Errorf("invalid GitHub token credential type")
		}
		token = data.Token
	default:
		return providerAuth.ResolvedAuth{}, fmt.Errorf("invalid GitHub Copilot credential source %q", data.Source)
	}
	if token == "" {
		return providerAuth.ResolvedAuth{}, fmt.Errorf("GitHub credential is empty")
	}
	return providerAuth.ResolvedAuth{Secret: token, Properties: map[string]string{"source": data.Source, "account": data.Account}}, nil
}

func (*Adapter) Describe(credential providerAuth.Credential) providerAuth.Status {
	var data credentialData
	_ = json.Unmarshal(credential.Data, &data)
	return providerAuth.Status{Name: "GitHub Copilot", Type: credential.Type, Account: data.Account}
}
