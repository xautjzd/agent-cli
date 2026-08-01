package auth

import (
	"context"
	"fmt"
	"time"
)

const defaultRefreshBefore = 5 * time.Minute

// Service coordinates registered provider adapters with the credential store.
// It is the provider-neutral entry point used by both CLI and interactive UI.
type Service struct {
	registry      *Registry
	store         *Store
	now           func() time.Time
	refreshBefore time.Duration
}

func NewService(registry *Registry, store *Store) *Service {
	return &Service{
		registry:      registry,
		store:         store,
		now:           time.Now,
		refreshBefore: defaultRefreshBefore,
	}
}

func (s *Service) Registry() *Registry { return s.registry }
func (s *Service) Store() *Store       { return s.store }

func (s *Service) adapter(providerID string) (Adapter, error) {
	if s == nil || s.registry == nil || s.store == nil {
		return nil, fmt.Errorf("auth service is not configured")
	}
	adapter, ok := s.registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support managed login", providerID)
	}
	return adapter, nil
}

// Login runs one provider-owned flow and persists the returned credential only
// after the flow succeeds completely.
func (s *Service) Login(ctx context.Context, providerID string, request LoginRequest, ui LoginUI) (Status, error) {
	adapter, err := s.adapter(providerID)
	if err != nil {
		return Status{}, err
	}
	credential, err := adapter.Login(ctx, request, ui)
	if err != nil {
		return Status{}, fmt.Errorf("login %s: %w", providerID, err)
	}
	if err := credential.Validate(); err != nil {
		return Status{}, fmt.Errorf("login %s returned an invalid credential: %w", providerID, err)
	}
	if err := s.store.Set(ctx, providerID, credential); err != nil {
		return Status{}, err
	}
	return describe(adapter, providerID, credential), nil
}

func (s *Service) Logout(ctx context.Context, providerID string) error {
	if _, err := s.adapter(providerID); err != nil {
		return err
	}
	return s.store.Delete(ctx, providerID)
}

func (s *Service) HasCredential(providerID string) (bool, error) {
	_, ok, err := s.store.Get(providerID)
	return ok, err
}

func (s *Service) Status(providerID string) (Status, bool, error) {
	adapter, err := s.adapter(providerID)
	if err != nil {
		return Status{}, false, err
	}
	credential, ok, err := s.store.Get(providerID)
	if err != nil || !ok {
		return Status{}, ok, err
	}
	return describe(adapter, providerID, credential), true, nil
}

func describe(adapter Adapter, providerID string, credential Credential) Status {
	if d, ok := adapter.(Describer); ok {
		status := d.Describe(credential)
		status.Provider = providerID
		if status.Name == "" {
			status.Name = adapter.DisplayName()
		}
		return status
	}
	return Status{Provider: providerID, Name: adapter.DisplayName(), Type: credential.Type, ExpiresAt: credential.ExpiresAt}
}

func (s *Service) needsRefresh(credential Credential) bool {
	return credential.ExpiresAt != nil && !credential.ExpiresAt.After(s.now().Add(s.refreshBefore))
}

// Resolve returns request-ready authentication, refreshing expiring OAuth
// credentials under the store's cross-process lock. A bad stored credential is
// reported and never silently downgraded to another auth source.
func (s *Service) Resolve(ctx context.Context, providerID string) (ResolvedAuth, error) {
	adapter, err := s.adapter(providerID)
	if err != nil {
		return ResolvedAuth{}, err
	}
	credential, ok, err := s.store.Get(providerID)
	if err != nil {
		return ResolvedAuth{}, err
	}
	if !ok {
		return ResolvedAuth{}, fmt.Errorf("provider %s is not logged in; run \"agent auth login %s\"", providerID, providerID)
	}
	if s.needsRefresh(credential) {
		refresher, ok := adapter.(Refresher)
		if !ok {
			return ResolvedAuth{}, fmt.Errorf("stored credential for %s has expired and cannot be refreshed", providerID)
		}
		updated, err := s.store.Modify(ctx, providerID, func(current *Credential) (*Credential, error) {
			if current == nil {
				return nil, fmt.Errorf("provider %s was logged out while refreshing", providerID)
			}
			if !s.needsRefresh(*current) {
				copy := cloneCredential(*current)
				return &copy, nil
			}
			refreshed, err := refresher.Refresh(ctx, *current)
			if err != nil {
				return nil, fmt.Errorf("refresh %s login: %w", providerID, err)
			}
			return &refreshed, nil
		})
		if err != nil {
			return ResolvedAuth{}, err
		}
		credential = *updated
	}
	resolved, err := adapter.Resolve(ctx, credential)
	if err != nil {
		return ResolvedAuth{}, fmt.Errorf("resolve %s login: %w", providerID, err)
	}
	if resolved.Secret == "" {
		return ResolvedAuth{}, fmt.Errorf("resolve %s login: adapter returned an empty credential", providerID)
	}
	return resolved, nil
}

func (s *Service) Usage(ctx context.Context, providerID string) (UsageSnapshot, error) {
	adapter, err := s.adapter(providerID)
	if err != nil {
		return UsageSnapshot{}, err
	}
	reader, ok := adapter.(UsageReader)
	if !ok {
		return UsageSnapshot{}, fmt.Errorf("provider %s does not expose subscription usage", providerID)
	}
	resolved, err := s.Resolve(ctx, providerID)
	if err != nil {
		return UsageSnapshot{}, err
	}
	snapshot, err := reader.Usage(ctx, resolved)
	if err != nil {
		return UsageSnapshot{}, fmt.Errorf("read %s usage: %w", providerID, err)
	}
	if snapshot.Provider == "" {
		snapshot.Provider = providerID
	}
	return snapshot, nil
}
