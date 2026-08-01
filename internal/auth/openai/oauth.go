package openai

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func randomURLString(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func (a *Adapter) Login(ctx context.Context, req providerAuth.LoginRequest, ui providerAuth.LoginUI) (providerAuth.Credential, error) {
	a.defaults()
	method := req.Method
	if method == "" {
		selected, err := ui.Select(ctx, "Select OpenAI login method", a.Methods())
		if err != nil {
			return providerAuth.Credential{}, err
		}
		method = selected
	}
	switch method {
	case "browser":
		return a.loginBrowser(ctx, ui)
	case "device_code":
		return a.loginDevice(ctx, ui)
	default:
		return providerAuth.Credential{}, fmt.Errorf("OpenAI does not support login method %q", method)
	}
}

func (a *Adapter) loginBrowser(ctx context.Context, ui providerAuth.LoginUI) (providerAuth.Credential, error) {
	listener, err := net.Listen("tcp", a.CallbackAddr)
	if err != nil {
		return providerAuth.Credential{}, fmt.Errorf("start OpenAI login callback: %w", err)
	}
	defer listener.Close()

	verifier, err := randomURLString(32)
	if err != nil {
		return providerAuth.Credential{}, fmt.Errorf("create OpenAI PKCE verifier")
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return providerAuth.Credential{}, fmt.Errorf("create OpenAI OAuth state")
	}
	state := hex.EncodeToString(stateBytes)
	redirectURI := "http://" + listener.Addr().String() + "/auth/callback"
	if a.CallbackAddr == defaultCallback {
		redirectURI = "http://localhost:1455/auth/callback"
	}

	authorize, err := url.Parse(strings.TrimRight(a.AuthBaseURL, "/") + "/oauth/authorize")
	if err != nil {
		return providerAuth.Credential{}, fmt.Errorf("build OpenAI authorization URL")
	}
	query := authorize.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", "openid profile email offline_access")
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", "agent-cli")
	authorize.RawQuery = query.Encode()

	type callbackResult struct {
		code string
		err  error
	}
	result := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid OpenAI login callback.", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: fmt.Errorf("OpenAI login state mismatch")}:
			default:
			}
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code.", http.StatusBadRequest)
			select {
			case result <- callbackResult{err: fmt.Errorf("OpenAI login callback has no code")}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body>OpenAI login complete. You can close this window.</body></html>")
		select {
		case result <- callbackResult{code: code}:
		default:
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	ui.Notify("Waiting for OpenAI browser login…")
	if err := ui.OpenURL(ctx, authorize.String()); err != nil {
		ui.Notify("Open this URL to continue: " + authorize.String())
	}
	loginCtx, cancel := context.WithTimeout(ctx, a.LoginDeadline)
	defer cancel()
	select {
	case <-loginCtx.Done():
		return providerAuth.Credential{}, fmt.Errorf("OpenAI browser login: %w", loginCtx.Err())
	case callback := <-result:
		if callback.err != nil {
			return providerAuth.Credential{}, callback.err
		}
		token, err := a.exchange(loginCtx, url.Values{
			"grant_type":    {"authorization_code"},
			"client_id":     {clientID},
			"code":          {callback.code},
			"code_verifier": {verifier},
			"redirect_uri":  {redirectURI},
		})
		if err != nil {
			return providerAuth.Credential{}, err
		}
		return makeCredential(token, a.Now())
	}
}

type deviceCode struct {
	DeviceAuthID string          `json:"device_auth_id"`
	UserCode     string          `json:"user_code"`
	Interval     json.RawMessage `json:"interval"`
}

func (d deviceCode) interval() (time.Duration, error) {
	var seconds float64
	if err := json.Unmarshal(d.Interval, &seconds); err != nil {
		var text string
		if json.Unmarshal(d.Interval, &text) != nil {
			return 0, fmt.Errorf("invalid device login interval")
		}
		if _, err := fmt.Sscanf(text, "%f", &seconds); err != nil {
			return 0, fmt.Errorf("invalid device login interval")
		}
	}
	if seconds < 0 || seconds > 60 {
		return 0, fmt.Errorf("invalid device login interval")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func (a *Adapter) loginDevice(ctx context.Context, ui providerAuth.LoginUI) (providerAuth.Credential, error) {
	deviceURL := strings.TrimRight(a.AuthBaseURL, "/") + "/api/accounts/deviceauth/usercode"
	body, _ := json.Marshal(map[string]string{"client_id": clientID})
	var device deviceCode
	if err := a.doJSON(ctx, http.MethodPost, deviceURL, strings.NewReader(string(body)), &device); err != nil {
		return providerAuth.Credential{}, fmt.Errorf("start OpenAI device login: %w", err)
	}
	interval, err := device.interval()
	if err != nil || device.DeviceAuthID == "" || device.UserCode == "" {
		return providerAuth.Credential{}, fmt.Errorf("OpenAI device login returned an invalid response")
	}
	verification := strings.TrimRight(a.AuthBaseURL, "/") + "/codex/device"
	ui.Notify("Open " + verification + " and enter code " + device.UserCode)
	_ = ui.OpenURL(ctx, verification)

	loginCtx, cancel := context.WithTimeout(ctx, a.LoginDeadline)
	defer cancel()
	pollURL := strings.TrimRight(a.AuthBaseURL, "/") + "/api/accounts/deviceauth/token"
	for {
		// Never busy-poll an authentication endpoint even if a malformed or
		// compromised server advertises zero delay.
		delay := interval
		if delay < time.Second {
			delay = time.Second
		}
		if err := a.Sleep(loginCtx, delay); err != nil {
			return providerAuth.Credential{}, fmt.Errorf("OpenAI device login: %w", err)
		}
		payload, _ := json.Marshal(map[string]string{"device_auth_id": device.DeviceAuthID, "user_code": device.UserCode})
		var complete struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		status, err := a.doJSONStatus(loginCtx, http.MethodPost, pollURL, strings.NewReader(string(payload)), &complete)
		if err == nil && status >= 200 && status < 300 && complete.AuthorizationCode != "" && complete.CodeVerifier != "" {
			token, err := a.exchange(loginCtx, url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {clientID},
				"code":          {complete.AuthorizationCode},
				"code_verifier": {complete.CodeVerifier},
				"redirect_uri":  {strings.TrimRight(a.AuthBaseURL, "/") + "/deviceauth/callback"},
			})
			if err != nil {
				return providerAuth.Credential{}, err
			}
			return makeCredential(token, a.Now())
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			if err != nil {
				return providerAuth.Credential{}, fmt.Errorf("poll OpenAI device login: %w", err)
			}
			return providerAuth.Credential{}, fmt.Errorf("poll OpenAI device login: HTTP %d", status)
		}
	}
}

func (a *Adapter) Refresh(ctx context.Context, credential providerAuth.Credential) (providerAuth.Credential, error) {
	a.defaults()
	data, err := decodeCredential(credential)
	if err != nil {
		return providerAuth.Credential{}, err
	}
	token, err := a.exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {data.RefreshToken},
		"client_id":     {clientID},
	})
	if err != nil {
		return providerAuth.Credential{}, fmt.Errorf("refresh OpenAI login: %w", err)
	}
	return makeCredential(token, a.Now())
}

func (a *Adapter) exchange(ctx context.Context, form url.Values) (tokenResponse, error) {
	var token tokenResponse
	endpoint := strings.TrimRight(a.AuthBaseURL, "/") + "/oauth/token"
	status, err := a.doJSONStatus(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()), &token, "application/x-www-form-urlencoded")
	if err != nil {
		return token, fmt.Errorf("OpenAI token exchange failed")
	}
	if status < 200 || status >= 300 {
		return token, fmt.Errorf("OpenAI token exchange failed (HTTP %d)", status)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresIn <= 0 {
		return token, fmt.Errorf("OpenAI token response is incomplete")
	}
	return token, nil
}

func (a *Adapter) doJSON(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	status, err := a.doJSONStatus(ctx, method, endpoint, body, out)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("HTTP %d", status)
	}
	return nil
}

func (a *Adapter) doJSONStatus(ctx context.Context, method, endpoint string, body io.Reader, out any, contentType ...string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	ct := "application/json"
	if len(contentType) > 0 {
		ct = contentType[0]
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("read JSON response")
	}
	if len(data) > maxResponseBytes {
		return resp.StatusCode, fmt.Errorf("JSON response exceeds %d bytes", maxResponseBytes)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return resp.StatusCode, fmt.Errorf("decode JSON response")
	}
	return resp.StatusCode, nil
}
