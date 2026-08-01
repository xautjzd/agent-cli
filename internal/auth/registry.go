package auth

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry holds compiled-in auth adapters keyed by stable provider ID.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

// Register adds one adapter. Duplicate IDs are errors rather than silent
// replacement so provider ownership cannot change with init order.
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return fmt.Errorf("register auth adapter: nil adapter")
	}
	id := strings.TrimSpace(a.ID())
	if id == "" {
		return fmt.Errorf("register auth adapter: empty provider ID")
	}
	if strings.TrimSpace(a.DisplayName()) == "" {
		return fmt.Errorf("register auth adapter %q: empty display name", id)
	}
	methods := a.Methods()
	if len(methods) == 0 {
		return fmt.Errorf("register auth adapter %q: no login methods", id)
	}
	seen := map[string]bool{}
	for _, method := range methods {
		if strings.TrimSpace(method.ID) == "" || strings.TrimSpace(method.Label) == "" {
			return fmt.Errorf("register auth adapter %q: login method needs ID and label", id)
		}
		if seen[method.ID] {
			return fmt.Errorf("register auth adapter %q: duplicate login method %q", id, method.ID)
		}
		seen[method.ID] = true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; exists {
		return fmt.Errorf("auth adapter %q is already registered", id)
	}
	r.adapters[id] = a
	return nil
}

func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}

// List returns a deterministic display order independent of registration.
func (r *Registry) List() []Adapter {
	r.mu.RLock()
	out := make([]Adapter, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		left, right := strings.ToLower(out[i].DisplayName()), strings.ToLower(out[j].DisplayName())
		if left == right {
			return out[i].ID() < out[j].ID()
		}
		return left < right
	})
	return out
}
