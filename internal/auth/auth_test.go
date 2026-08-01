package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAdapter struct{ id, name string }

func (f fakeAdapter) ID() string             { return f.id }
func (f fakeAdapter) DisplayName() string    { return f.name }
func (f fakeAdapter) Methods() []LoginMethod { return []LoginMethod{{ID: "oauth", Label: "OAuth"}} }
func (f fakeAdapter) Login(context.Context, LoginRequest, LoginUI) (Credential, error) {
	return Credential{}, errors.New("unused")
}
func (f fakeAdapter) Resolve(context.Context, Credential) (ResolvedAuth, error) {
	return ResolvedAuth{}, errors.New("unused")
}

func testCredential(value string) Credential {
	return NewCredential(CredentialOAuth, nil, json.RawMessage(`{"value":`+"\""+value+"\"}"))
}

func TestRegistryValidatesAndSorts(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fakeAdapter{id: "z", name: "Zulu"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeAdapter{id: "a", name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeAdapter{id: "a", name: "Again"}); err == nil {
		t.Fatal("duplicate adapter was accepted")
	}
	got := r.List()
	if len(got) != 2 || got[0].ID() != "a" || got[1].ID() != "z" {
		t.Fatalf("unexpected registry order: %#v", got)
	}
	if _, ok := r.Get("a"); !ok {
		t.Fatal("registered adapter not found")
	}
}

func TestStorePreservesProvidersAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	store := NewStore(filepath.Join(dir, "auth.json"))
	ctx := context.Background()
	if err := store.Set(ctx, "openai", testCredential("first-secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "anthropic", testCredential("second-secret")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "openai"); err != nil {
		t.Fatal(err)
	}
	ids, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "anthropic" {
		t.Fatalf("unrelated credential was not preserved: %v", ids)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth file mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("auth dir mode = %o, want 700", got)
	}
}

func TestStoreModifyIsAtomicOnError(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	ctx := context.Background()
	want := testCredential("kept-secret")
	if err := store.Set(ctx, "openai", want); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("refresh failed")
	_, err := store.Modify(ctx, "openai", func(*Credential) (*Credential, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("Modify error = %v", err)
	}
	got, ok, err := store.Get("openai")
	var gotData, wantData map[string]string
	if err == nil {
		err = json.Unmarshal(got.Data, &gotData)
	}
	if decodeErr := json.Unmarshal(want.Data, &wantData); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || !ok || gotData["value"] != wantData["value"] {
		t.Fatalf("credential changed after failed modify: ok=%v err=%v data=%s", ok, err, got.Data)
	}
}

func TestStoreLockHonorsContext(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := os.Mkdir(store.Path()+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := store.Set(ctx, "openai", testCredential("secret-never-rendered"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock error = %v, want deadline exceeded", err)
	}
	if strings.Contains(err.Error(), "secret-never-rendered") {
		t.Fatal("credential leaked in error")
	}
}

func TestStoreRejectsCorruptAndOversizedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	store := NewStore(path)
	if err := os.WriteFile(path, []byte(`{"version":1,"providers":{"openai":{"version":1,"type":"oauth","data":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get("openai"); err == nil || !strings.Contains(err.Error(), "parse auth store") {
		t.Fatalf("corrupt file error = %v", err)
	}
	if err := os.WriteFile(path, make([]byte, maxStoreSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get("openai"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized file error = %v", err)
	}
}
