// Package openai implements ChatGPT/Codex subscription authentication.
package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

const (
	providerID       = "openai"
	clientID         = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultAuthBase  = "https://auth.openai.com"
	defaultChatBase  = "https://chatgpt.com/backend-api"
	defaultCallback  = "127.0.0.1:1455"
	jwtAuthClaimPath = "https://api.openai.com/auth"
	maxResponseBytes = 1 << 20
)

// Adapter owns OpenAI-specific login, refresh, resolution, and live usage.
// Endpoint and clock fields are injectable so tests never contact OpenAI.
type Adapter struct {
	Client        *http.Client
	AuthBaseURL   string
	ChatBaseURL   string
	CallbackAddr  string
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration) error
	LoginDeadline time.Duration
}

func New() *Adapter {
	return &Adapter{
		Client:        &http.Client{Timeout: 30 * time.Second},
		AuthBaseURL:   defaultAuthBase,
		ChatBaseURL:   defaultChatBase,
		CallbackAddr:  defaultCallback,
		Now:           time.Now,
		Sleep:         sleepContext,
		LoginDeadline: 15 * time.Minute,
	}
}

func (a *Adapter) defaults() {
	if a.Client == nil {
		a.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if a.AuthBaseURL == "" {
		a.AuthBaseURL = defaultAuthBase
	}
	if a.ChatBaseURL == "" {
		a.ChatBaseURL = defaultChatBase
	}
	if a.CallbackAddr == "" {
		a.CallbackAddr = defaultCallback
	}
	if a.Now == nil {
		a.Now = time.Now
	}
	if a.Sleep == nil {
		a.Sleep = sleepContext
	}
	if a.LoginDeadline <= 0 {
		a.LoginDeadline = 15 * time.Minute
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *Adapter) ID() string          { return providerID }
func (a *Adapter) DisplayName() string { return "OpenAI (ChatGPT subscription)" }
func (a *Adapter) Methods() []providerAuth.LoginMethod {
	return []providerAuth.LoginMethod{
		{ID: "browser", Label: "Browser login", Description: "Sign in with ChatGPT in a browser"},
		{ID: "device_code", Label: "Device code", Description: "Sign in from a headless or remote host"},
	}
}

type credentialData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email,omitempty"`
	Plan         string `json:"plan,omitempty"`
}

func decodeCredential(c providerAuth.Credential) (credentialData, error) {
	if err := c.Validate(); err != nil {
		return credentialData{}, err
	}
	if c.Type != providerAuth.CredentialOAuth {
		return credentialData{}, fmt.Errorf("OpenAI subscription requires an OAuth credential")
	}
	var data credentialData
	if err := json.Unmarshal(c.Data, &data); err != nil {
		return data, fmt.Errorf("decode OpenAI credential")
	}
	if data.AccessToken == "" || data.RefreshToken == "" || data.AccountID == "" {
		return data, fmt.Errorf("OpenAI credential is incomplete; run auth login openai again")
	}
	return data, nil
}

func makeCredential(token tokenResponse, now time.Time) (providerAuth.Credential, error) {
	claims, err := tokenClaims(token.AccessToken)
	if err != nil {
		return providerAuth.Credential{}, err
	}
	data, err := json.Marshal(credentialData{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		AccountID:    claims.AccountID,
		Email:        claims.Email,
		Plan:         claims.Plan,
	})
	if err != nil {
		return providerAuth.Credential{}, fmt.Errorf("encode OpenAI credential")
	}
	expires := now.Add(time.Duration(token.ExpiresIn) * time.Second)
	c := providerAuth.NewCredential(providerAuth.CredentialOAuth, &expires, data)
	return c, nil
}

func (a *Adapter) Resolve(_ context.Context, c providerAuth.Credential) (providerAuth.ResolvedAuth, error) {
	data, err := decodeCredential(c)
	if err != nil {
		return providerAuth.ResolvedAuth{}, err
	}
	return providerAuth.ResolvedAuth{
		Secret: data.AccessToken,
		Properties: map[string]string{
			"account_id": data.AccountID,
			"email":      data.Email,
			"plan":       data.Plan,
		},
	}, nil
}

func (a *Adapter) Describe(c providerAuth.Credential) providerAuth.Status {
	status := providerAuth.Status{Provider: providerID, Name: a.DisplayName(), Type: c.Type, ExpiresAt: c.ExpiresAt}
	if data, err := decodeCredential(c); err == nil {
		status.Account = data.Email
		status.Plan = data.Plan
	}
	return status
}

type jwtClaims struct{ AccountID, Email, Plan string }

func tokenClaims(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, fmt.Errorf("OpenAI access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode OpenAI access token claims")
	}
	var raw struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
			Plan      string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return jwtClaims{}, fmt.Errorf("decode OpenAI access token claims")
	}
	if raw.Auth.AccountID == "" {
		return jwtClaims{}, fmt.Errorf("OpenAI access token has no ChatGPT account ID")
	}
	return jwtClaims{AccountID: raw.Auth.AccountID, Email: raw.Email, Plan: raw.Auth.Plan}, nil
}
