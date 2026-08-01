# Provider authentication tasks

- [x] Build generic auth contracts, registry, and store.
  - Acceptance: provider capabilities are discoverable by stable ID; credentials
    are versioned, atomically stored with restrictive modes, and modified under
    a bounded cross-process lock without exposing secrets.
  - Verify: `go test ./internal/auth`
  - Files: `internal/auth/types.go`, `internal/auth/registry.go`,
    `internal/auth/store.go`, `internal/auth/auth_test.go`

- [ ] Implement the OpenAI auth and subscription-usage adapter.
  - Acceptance: browser PKCE/state and device-code flows work through injected
    endpoints/UI; token refresh rotates safely; account claims and live limits
    are validated and normalized; errors redact credentials.
  - Verify: `go test ./internal/auth/openai`
  - Files: `internal/auth/openai/adapter.go`, `internal/auth/openai/oauth.go`,
    `internal/auth/openai/usage.go`, `internal/auth/openai/openai_test.go`

- [ ] Implement the ChatGPT Codex Responses provider.
  - Acceptance: OAuth-resolved OpenAI requests support streamed text, reasoning,
    tools, tool results, images, usage, cancellation, and bounded errors while
    the API-key Chat Completions path remains unchanged.
  - Verify: `go test ./internal/provider`
  - Files: `internal/provider/auth.go`, `internal/provider/openai_codex.go`,
    `internal/provider/openai_codex_test.go`, `internal/provider/types.go`

- [ ] Add auth resolution and the shell command surface.
  - Acceptance: explicit config wins over stored auth, failed stored auth does
    not silently fall through, and `agent auth login/list/status/usage/logout`
    uses the registry with safe deterministic output.
  - Verify: `go test ./internal/config ./cmd/agent`
  - Files: `internal/auth/service.go`, `internal/config/config.go`,
    `internal/config/config_test.go`, `cmd/agent/main.go`, `cmd/agent/main_test.go`

- [ ] Add interactive auth commands, live usage, docs, and final review.
  - Acceptance: `/login`, `/logout`, `/auth`, and `/usage` share the service;
    subscription limits stay separate from local costs; offline behavior is
    graceful; docs match behavior; no credential reaches logs/session/config.
  - Verify: `go test ./internal/repl && go vet ./internal/auth/... ./internal/provider ./internal/config ./internal/repl ./cmd/agent && go test ./...`
  - Files: `internal/repl/auth.go`, `internal/repl/repl.go`,
    `internal/repl/repl_test.go`, `docs/providers.md`, `docs/usage-and-cost.md`
