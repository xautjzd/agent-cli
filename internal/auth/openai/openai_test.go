package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	providerAuth "github.com/xautjzd/agent-cli/internal/auth"
)

func jwt(t *testing.T, account, email, plan string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"email": email,
		jwtAuthClaimPath: map[string]string{
			"chatgpt_account_id": account,
			"chatgpt_plan_type":  plan,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

type testUI struct {
	selectMethod string
	open         func(string) error
	notices      []string
}

func (u *testUI) Select(context.Context, string, []providerAuth.LoginMethod) (string, error) {
	return u.selectMethod, nil
}
func (u *testUI) OpenURL(_ context.Context, value string) error {
	if u.open != nil {
		return u.open(value)
	}
	return nil
}
func (u *testUI) Prompt(context.Context, string) (string, error) { return "", nil }
func (u *testUI) Notify(message string)                          { u.notices = append(u.notices, message) }

func TestBrowserLoginUsesPKCEStateAndSafeCredential(t *testing.T) {
	access := jwt(t, "acct-123", "user@example.com", "plus")
	var tokenForm url.Values
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		tokenForm = r.PostForm
		_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: access, RefreshToken: "refresh-secret", ExpiresIn: 3600})
	}))
	defer authServer.Close()

	a := New()
	a.AuthBaseURL = authServer.URL
	a.CallbackAddr = "127.0.0.1:0"
	a.Now = func() time.Time { return time.Unix(100, 0) }
	ui := &testUI{open: func(raw string) error {
		authorize, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := authorize.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("code_challenge") == "" {
			return fmt.Errorf("missing PKCE/state")
		}
		callback := q.Get("redirect_uri") + "?code=authorization-secret&state=" + url.QueryEscape(q.Get("state"))
		resp, err := http.Get(callback)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}}
	credential, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "browser"}, ui)
	if err != nil {
		t.Fatal(err)
	}
	if tokenForm.Get("code_verifier") == "" || tokenForm.Get("code") != "authorization-secret" {
		t.Fatalf("bad token exchange form: %v", tokenForm)
	}
	resolved, err := a.Resolve(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Secret != access || resolved.Properties["account_id"] != "acct-123" {
		t.Fatalf("resolved auth = %+v", resolved)
	}
	status := a.Describe(credential)
	if status.Account != "user@example.com" || status.Plan != "plus" {
		t.Fatalf("safe status = %+v", status)
	}
}

func TestBrowserLoginRejectsWrongStateWithoutTokenExchange(t *testing.T) {
	var exchanges int
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		http.Error(w, "no", http.StatusBadRequest)
	}))
	defer authServer.Close()
	a := New()
	a.AuthBaseURL = authServer.URL
	a.CallbackAddr = "127.0.0.1:0"
	ui := &testUI{open: func(raw string) error {
		authorize, _ := url.Parse(raw)
		resp, err := http.Get(authorize.Query().Get("redirect_uri") + "?code=secret&state=wrong")
		if err == nil {
			resp.Body.Close()
		}
		return err
	}}
	_, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "browser"}, ui)
	if err == nil || !strings.Contains(err.Error(), "state mismatch") || exchanges != 0 {
		t.Fatalf("err=%v exchanges=%d", err, exchanges)
	}
}

func TestBrowserLoginRejectsNonLoopbackCallback(t *testing.T) {
	a := New()
	a.CallbackAddr = "0.0.0.0:0"
	_, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "browser"}, &testUI{})
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Login error = %v; want loopback validation", err)
	}
}

func TestDeviceLoginAndRefresh(t *testing.T) {
	firstAccess := jwt(t, "acct-device", "device@example.com", "pro")
	secondAccess := jwt(t, "acct-device", "device@example.com", "pro")
	var polls, refreshes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_auth_id": "dev", "user_code": "ABCD", "interval": 0})
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				http.Error(w, "pending", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"authorization_code": "code", "code_verifier": "verifier"})
		case "/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") == "refresh_token" {
				refreshes++
				_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: secondAccess, RefreshToken: "rotated-refresh", ExpiresIn: 7200})
				return
			}
			_ = json.NewEncoder(w).Encode(tokenResponse{AccessToken: firstAccess, RefreshToken: "initial-refresh", ExpiresIn: 3600})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	a := New()
	a.AuthBaseURL = server.URL
	a.Now = func() time.Time { return time.Unix(100, 0) }
	a.Sleep = func(context.Context, time.Duration) error { return nil }
	credential, err := a.Login(context.Background(), providerAuth.LoginRequest{Method: "device_code"}, &testUI{})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := a.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	data, err := decodeCredential(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 2 || refreshes != 1 || data.RefreshToken != "rotated-refresh" {
		t.Fatalf("polls=%d refreshes=%d data=%+v", polls, refreshes, data)
	}
}

func TestUsageNormalizesWindowsAndNeverLeaksToken(t *testing.T) {
	secret := "access-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("ChatGPT-Account-Id") != "acct" {
			t.Fatalf("bad auth headers")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "team",
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 25, "limit_window_seconds": 18000, "reset_at": 200},
				"secondary_window": map[string]any{"used_percent": 75, "limit_window_seconds": 604800, "reset_at": 300},
			},
			"credits": map[string]any{"has_credits": true, "unlimited": false, "balance": "4.5"},
		})
	}))
	defer server.Close()
	a := New()
	a.ChatBaseURL = server.URL
	a.Now = func() time.Time { return time.Unix(150, 0) }
	snapshot, err := a.Usage(context.Background(), providerAuth.ResolvedAuth{Secret: secret, Properties: map[string]string{"account_id": "acct"}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Plan != "team" || len(snapshot.Limits) != 3 || *snapshot.Limits[0].UsedPercent != 25 || snapshot.Limits[0].Window != 5*time.Hour {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, secret, http.StatusUnauthorized)
	}))
	defer bad.Close()
	a.ChatBaseURL = bad.URL
	_, err = a.Usage(context.Background(), providerAuth.ResolvedAuth{Secret: secret, Properties: map[string]string{"account_id": "acct"}})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe usage error: %v", err)
	}
}
