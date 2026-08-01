package auth

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type serviceAdapter struct {
	refreshes  int
	refreshErr error
}

func (*serviceAdapter) ID() string          { return "test" }
func (*serviceAdapter) DisplayName() string { return "Test Provider" }
func (*serviceAdapter) Methods() []LoginMethod {
	return []LoginMethod{{ID: "browser", Label: "Browser"}}
}
func (*serviceAdapter) Login(context.Context, LoginRequest, LoginUI) (Credential, error) {
	data, _ := json.Marshal(map[string]string{"token": "login-token"})
	return NewCredential(CredentialOAuth, nil, data), nil
}
func (*serviceAdapter) Resolve(_ context.Context, credential Credential) (ResolvedAuth, error) {
	var data map[string]string
	if err := json.Unmarshal(credential.Data, &data); err != nil {
		return ResolvedAuth{}, err
	}
	return ResolvedAuth{Secret: data["token"]}, nil
}
func (a *serviceAdapter) Refresh(_ context.Context, credential Credential) (Credential, error) {
	a.refreshes++
	if a.refreshErr != nil {
		return Credential{}, a.refreshErr
	}
	data, _ := json.Marshal(map[string]string{"token": "fresh-token"})
	expires := time.Now().Add(time.Hour)
	return NewCredential(CredentialOAuth, &expires, data), nil
}
func (*serviceAdapter) Usage(_ context.Context, auth ResolvedAuth) (UsageSnapshot, error) {
	used := 25
	return UsageSnapshot{Plan: auth.Secret, Limits: []UsageLimit{{Name: "primary", UsedPercent: &used}}}, nil
}

func testService(t *testing.T) (*Service, *serviceAdapter) {
	t.Helper()
	adapter := &serviceAdapter{}
	registry := NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	service := NewService(registry, NewStore(filepath.Join(t.TempDir(), "auth.json")))
	return service, adapter
}

func TestServiceResolveRefreshesAndUsageUsesResolvedAuth(t *testing.T) {
	service, adapter := testService(t)
	expires := time.Now().Add(-time.Minute)
	data, _ := json.Marshal(map[string]string{"token": "old-token"})
	if err := service.store.Set(context.Background(), "test", NewCredential(CredentialOAuth, &expires, data)); err != nil {
		t.Fatal(err)
	}

	resolved, err := service.Resolve(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Secret != "fresh-token" || adapter.refreshes != 1 {
		t.Fatalf("resolved = %#v, refreshes = %d", resolved, adapter.refreshes)
	}
	snapshot, err := service.Usage(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider != "test" || snapshot.Plan != "fresh-token" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestServiceRefreshFailurePreservesCredential(t *testing.T) {
	service, adapter := testService(t)
	adapter.refreshErr = errors.New("denied")
	expires := time.Now().Add(-time.Minute)
	data, _ := json.Marshal(map[string]string{"token": "old-token"})
	original := NewCredential(CredentialOAuth, &expires, data)
	if err := service.store.Set(context.Background(), "test", original); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Resolve(context.Background(), "test"); err == nil {
		t.Fatal("Resolve succeeded; want refresh error")
	}
	stored, ok, err := service.store.Get("test")
	if err != nil || !ok {
		t.Fatalf("Get = %#v, %v, %v", stored, ok, err)
	}
	var storedData map[string]string
	if err := json.Unmarshal(stored.Data, &storedData); err != nil || storedData["token"] != "old-token" {
		t.Fatalf("credential changed after failed refresh: %s", stored.Data)
	}
}

func TestServiceMissingCredentialDoesNotFallThrough(t *testing.T) {
	service, _ := testService(t)
	if _, err := service.Resolve(context.Background(), "test"); err == nil {
		t.Fatal("Resolve succeeded without stored credential")
	}
}
