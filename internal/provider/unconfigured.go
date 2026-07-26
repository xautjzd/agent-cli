package provider

import (
	"context"
	"errors"
)

// unconfigured stands in for a provider that could not be built — almost
// always a missing credential on a fresh install.
//
// Refusing to start in that case is the wrong trade: the session is where the
// problem gets fixed (/provider prompts for a key and stores it), so exiting
// sends the user away from the only place that helps. Instead the session
// opens with this placeholder and the setup error surfaces on the first
// request, unchanged, if it still has not been resolved.
type unconfigured struct {
	err error
}

// Unconfigured returns a provider that carries a setup error instead of a
// connection. Every request fails with that error, so nothing is silently
// swallowed; SetupError reports it for callers that want to show it up front.
func Unconfigured(err error) Provider {
	if err == nil {
		err = errors.New("no provider configured")
	}
	return &unconfigured{err: err}
}

func (u *unconfigured) Name() string { return "unconfigured" }

func (u *unconfigured) Chat(context.Context, Request) (*Response, error) {
	return nil, u.err
}

// SetupError returns the error a provider is standing in for, and whether it
// is a placeholder at all. Callers use it to tell "not configured yet" apart
// from a working provider without type-asserting a private type.
func SetupError(p Provider) (error, bool) {
	if u, ok := p.(*unconfigured); ok {
		return u.err, true
	}
	return nil, false
}
