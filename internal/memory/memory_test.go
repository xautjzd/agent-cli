package memory

import (
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store := &FileStore{Dir: filepath.Join(t.TempDir(), "memory")}

	if entries, err := store.List(); err != nil || len(entries) != 0 {
		t.Fatalf("empty store: entries=%v err=%v", entries, err)
	}

	if err := store.Save("api-style", "APIs return {code, msg, data}."); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("build-cmd", "Use make build."); err != nil {
		t.Fatal(err)
	}

	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "api-style" {
		t.Fatalf("List = %+v", entries)
	}

	e, err := store.Get("api-style")
	if err != nil {
		t.Fatal(err)
	}
	if e.Content != "APIs return {code, msg, data}." {
		t.Errorf("Content = %q", e.Content)
	}

	// Overwrite keeps a single entry.
	if err := store.Save("api-style", "updated"); err != nil {
		t.Fatal(err)
	}
	e, _ = store.Get("api-style")
	if e.Content != "updated" {
		t.Errorf("overwrite failed, got %q", e.Content)
	}

	if err := store.Delete("api-style"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("api-style"); err == nil {
		t.Error("expected error after delete")
	}
	if err := store.Delete("api-style"); err == nil {
		t.Error("double delete should error")
	}
}

func TestSaveValidation(t *testing.T) {
	store := &FileStore{Dir: t.TempDir()}
	if err := store.Save("Bad Name", "x"); err == nil {
		t.Error("non-slug name should be rejected")
	}
	if err := store.Save("../escape", "x"); err == nil {
		t.Error("path traversal name should be rejected")
	}
	if err := store.Save("ok", "   "); err == nil {
		t.Error("empty content should be rejected")
	}
}
