package provider

import "context"

// RequestAuth is ephemeral provider authentication. It is deliberately absent
// from Config and session serialization so subscription tokens cannot leak into
// config, transcripts, or prompts.
type RequestAuth struct {
	Token     string
	AccountID string
}

// AuthSource resolves and refreshes request authentication on demand.
type AuthSource interface {
	Auth(context.Context) (RequestAuth, error)
}
