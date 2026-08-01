// Package auth provides provider-agnostic authentication contracts and
// credential storage. Provider-specific OAuth and subscription protocols live
// in subpackages and register adapters here.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// CredentialType identifies the serialized credential variant.
type CredentialType string

const (
	CredentialAPIKey  CredentialType = "api_key"
	CredentialOAuth   CredentialType = "oauth"
	credentialVersion                = 1
)

// Credential is the versioned envelope stored for one provider. Data belongs
// exclusively to that provider's adapter and must never be rendered or logged.
type Credential struct {
	Version   int             `json:"version"`
	Type      CredentialType  `json:"type"`
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
	Data      json.RawMessage `json:"data"`
}

// NewCredential creates a current-version credential envelope.
func NewCredential(kind CredentialType, expiresAt *time.Time, data json.RawMessage) Credential {
	return Credential{Version: credentialVersion, Type: kind, ExpiresAt: expiresAt, Data: data}
}

// Validate rejects malformed or unsupported envelopes at the storage boundary.
func (c Credential) Validate() error {
	if c.Version != credentialVersion {
		return fmt.Errorf("unsupported credential version %d", c.Version)
	}
	switch c.Type {
	case CredentialAPIKey, CredentialOAuth:
	default:
		return fmt.Errorf("unsupported credential type %q", c.Type)
	}
	if len(c.Data) == 0 || !json.Valid(c.Data) {
		return fmt.Errorf("credential data is not valid JSON")
	}
	return nil
}

// LoginMethod is one provider-advertised interactive authentication method.
type LoginMethod struct {
	ID          string
	Label       string
	Description string
}

// LoginRequest selects a method. An empty Method lets the adapter choose its
// safe default or ask through LoginUI.
type LoginRequest struct{ Method string }

// LoginUI is the minimal provider-independent surface needed by browser,
// device-code, and manual login flows.
type LoginUI interface {
	Select(context.Context, string, []LoginMethod) (string, error)
	OpenURL(context.Context, string) error
	Prompt(context.Context, string) (string, error)
	Notify(string)
}

// ResolvedAuth is ephemeral request authentication. Secret is never persisted
// through this type; Properties contains non-secret routing metadata such as a
// workspace or account ID.
type ResolvedAuth struct {
	Secret     string
	Properties map[string]string
}

// Adapter is the required contract for an authenticating provider.
type Adapter interface {
	ID() string
	DisplayName() string
	Methods() []LoginMethod
	Login(context.Context, LoginRequest, LoginUI) (Credential, error)
	Resolve(context.Context, Credential) (ResolvedAuth, error)
}

// Refresher is implemented only when credentials can expire and refresh.
type Refresher interface {
	Refresh(context.Context, Credential) (Credential, error)
}

// Describer optionally returns safe account metadata for auth status displays.
type Describer interface {
	Describe(Credential) Status
}

// UsageReader is implemented only by providers exposing live subscription use.
type UsageReader interface {
	Usage(context.Context, ResolvedAuth) (UsageSnapshot, error)
}

// Status contains display-safe credential metadata only.
type Status struct {
	Provider  string
	Name      string
	Type      CredentialType
	Account   string
	Plan      string
	ExpiresAt *time.Time
}

// UsageSnapshot is a normalized, provider-neutral live-usage response.
type UsageSnapshot struct {
	Provider  string
	Plan      string
	FetchedAt time.Time
	Limits    []UsageLimit
}

// UsageLimit represents either a rolling percentage window or a labelled
// allowance. Provider APIs expose different subsets, so value fields are
// intentionally optional.
type UsageLimit struct {
	Name        string
	UsedPercent *int
	Used        string
	Limit       string
	Remaining   string
	Window      time.Duration
	ResetsAt    *time.Time
}
