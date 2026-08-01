package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/xautjzd/agent-cli/internal/home"
)

const (
	storeVersion = 1
	maxStoreSize = 4 << 20
	lockPoll     = 25 * time.Millisecond
	staleLockAge = 15 * time.Minute
)

type storeFile struct {
	Version   int                   `json:"version"`
	Providers map[string]Credential `json:"providers"`
}

// Store persists provider credentials in one atomic, versioned file.
type Store struct{ path string }

func NewStore(path string) *Store { return &Store{path: path} }
func DefaultStore() *Store        { return NewStore(home.Path("auth.json")) }
func (s *Store) Path() string     { return s.path }

func (s *Store) read() (storeFile, error) {
	state := storeFile{Version: storeVersion, Providers: map[string]Credential{}}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open auth store: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxStoreSize+1))
	if err != nil {
		return state, fmt.Errorf("read auth store: %w", err)
	}
	if len(data) > maxStoreSize {
		return state, fmt.Errorf("auth store exceeds %d bytes", maxStoreSize)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("parse auth store: %w", err)
	}
	if state.Version != storeVersion {
		return state, fmt.Errorf("unsupported auth store version %d", state.Version)
	}
	if state.Providers == nil {
		state.Providers = map[string]Credential{}
	}
	for id, credential := range state.Providers {
		if id == "" {
			return state, fmt.Errorf("auth store contains an empty provider ID")
		}
		if err := credential.Validate(); err != nil {
			return state, fmt.Errorf("auth store credential for %q: %w", id, err)
		}
	}
	return state, nil
}

func cloneCredential(c Credential) Credential {
	out := c
	out.Data = append(json.RawMessage(nil), c.Data...)
	if c.ExpiresAt != nil {
		expires := *c.ExpiresAt
		out.ExpiresAt = &expires
	}
	return out
}

func (s *Store) Get(providerID string) (Credential, bool, error) {
	state, err := s.read()
	if err != nil {
		return Credential{}, false, err
	}
	c, ok := state.Providers[providerID]
	return cloneCredential(c), ok, nil
}

func (s *Store) List() ([]string, error) {
	state, err := s.read()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(state.Providers))
	for id := range state.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) Set(ctx context.Context, providerID string, credential Credential) error {
	_, err := s.Modify(ctx, providerID, func(*Credential) (*Credential, error) { return &credential, nil })
	return err
}

func (s *Store) Delete(ctx context.Context, providerID string) error {
	_, err := s.Modify(ctx, providerID, func(*Credential) (*Credential, error) { return nil, nil })
	return err
}

// Modify locks the entire file, reloads authoritative state, and atomically
// writes one provider change while preserving every unrelated credential.
func (s *Store) Modify(ctx context.Context, providerID string, fn func(*Credential) (*Credential, error)) (*Credential, error) {
	if providerID == "" {
		return nil, fmt.Errorf("provider ID is required")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	state, err := s.read()
	if err != nil {
		return nil, err
	}
	var current *Credential
	if c, ok := state.Providers[providerID]; ok {
		copy := cloneCredential(c)
		current = &copy
	}
	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if next == nil {
		delete(state.Providers, providerID)
	} else {
		if err := next.Validate(); err != nil {
			return nil, fmt.Errorf("credential for %q: %w", providerID, err)
		}
		state.Providers[providerID] = cloneCredential(*next)
	}
	if err := s.write(state); err != nil {
		return nil, err
	}
	if next == nil {
		return nil, nil
	}
	copy := cloneCredential(*next)
	return &copy, nil
}

func (s *Store) write(state storeFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth store: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	tmp, err := os.CreateTemp(dir, ".auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create auth temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure auth temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write auth temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync auth temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close auth temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace auth store: %w", err)
	}
	return os.Chmod(s.path, 0o600)
}

func (s *Store) lock(ctx context.Context) (func(), error) {
	lockPath := s.path + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create auth directory: %w", err)
	}
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock auth store: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			_ = os.Remove(lockPath)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("lock auth store: %w", ctx.Err())
		case <-time.After(lockPoll):
		}
	}
}
