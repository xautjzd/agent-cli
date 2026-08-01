# Spec: extensible provider authentication and subscription usage

## Objective

Add one provider-agnostic authentication system to `agent-cli`. The first
adapter signs in to OpenAI with ChatGPT and uses the account's Codex
entitlement, but the public commands, credential store, registry, and usage
model must also support future subscription adapters such as Claude and Kimi.

Authentication is a provider capability, not a new provider name and not an
OpenAI-specific CLI namespace. Model wire adapters remain separate from login,
credential refresh, and subscription-usage adapters.

## User-facing commands

```text
agent auth login [provider]       choose a provider/method when omitted, then authenticate
  --method <id>                   select a provider-advertised method non-interactively
agent auth logout [provider]      choose a stored provider when omitted
agent auth list                   list available methods without exposing secrets
agent auth status [provider]      show credential source and safe account metadata
agent auth usage [provider]       fetch current subscription limits when supported
```

Interactive aliases use the same registry and command handlers:

```text
/login [provider]
/logout [provider]
/auth
/usage
```

`/auth` lists safe authentication status. `/usage` keeps the existing local
per-project token history and, when the active provider exposes live
subscription usage, appends a separately labelled live section.

With only OpenAI subscription auth implemented initially, an omitted provider
selects OpenAI directly when there is no meaningful provider choice, then asks
for browser or device-code login. The provider selector appears automatically
when more adapters are registered. `--method browser` and
`--method device_code` make the initial OpenAI flow scriptable without adding
vendor-specific flags to the generic command.

## Reference design

The design follows the useful common boundary in pi and opencode:

- pi stores credentials by provider ID in one `AuthStorage`. Providers register
  OAuth login/refresh/auth-derivation behavior. Expired tokens are refreshed
  under a cross-process lock with an authoritative second expiry check.
- opencode exposes one `auth login/list/logout` command family rather than
  vendor-specific login commands.
- Both keep provider authentication separate from model selection and make
  provider-specific behavior discoverable through registration.

This project will use the same shape without adopting either project's runtime
or adding a third-party dependency.

## Public behavior and precedence

`agent provider use <name> [model]` keeps its existing meaning. Authentication
resolution for a request is:

1. Invocation/runtime override, when one exists.
2. Explicit existing config (`api_key` or saved `api_keys[provider]`).
3. A stored credential from the new auth store, including refresh when required.
4. The provider's existing environment variables.
5. Ambient provider auth, if a future adapter explicitly implements it.

A configured credential owns the attempt. If refresh or validation fails, the
request reports re-authentication instead of silently falling through to a
different account or billing source.

Existing API-key users and all non-OpenAI providers retain their current
behavior. The first release does not migrate or delete keys already stored in
`config.json`.

## Internal contracts

The auth package owns storage and orchestration. Provider adapters register a
descriptor and only the optional capabilities they implement.

```go
type Adapter interface {
	ID() string
	DisplayName() string
	Methods() []LoginMethod
	Login(context.Context, LoginRequest, LoginUI) (Credential, error)
	Resolve(context.Context, Credential) (ResolvedAuth, error)
}

type Refresher interface {
	Refresh(context.Context, Credential) (Credential, error)
}

type UsageReader interface {
	Usage(context.Context, ResolvedAuth) (UsageSnapshot, error)
}
```

Optional interfaces keep capabilities honest: a provider without refresh or
live usage does not implement or advertise them. The registry rejects duplicate
or empty provider IDs and returns adapters in deterministic display order.

`Credential` is a versioned, discriminated envelope:

```go
type Credential struct {
	Version   int             `json:"version"`
	Type      CredentialType  `json:"type"` // api_key or oauth
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
	Data      json.RawMessage `json:"data"`
}
```

Only the owning adapter decodes `Data`; this avoids forcing Claude, OpenAI, and
Kimi into one brittle token schema. `ResolvedAuth` is ephemeral and never
serialized. It contains the request secret plus non-secret provider properties
needed by the model adapter, such as an account/workspace ID.

`UsageSnapshot` is provider-neutral display data:

```go
type UsageSnapshot struct {
	Provider  string
	Plan      string
	FetchedAt time.Time
	Limits    []UsageLimit
}

type UsageLimit struct {
	Name          string
	UsedPercent   *int
	Used          string
	Limit         string
	Remaining     string
	Window        time.Duration
	ResetsAt      *time.Time
}
```

Fields are optional because providers expose different combinations of rolling
windows, credits, balances, and spend controls. Rendering is centralized and
never receives raw provider responses.

## First adapter: OpenAI subscription

- Login methods: browser authorization-code flow with PKCE S256 and optional
  device-code flow for headless hosts.
- Provider ID: `openai`, matching the existing model provider. OAuth is an auth
  mode, not an `openai-codex` provider visible to users.
- Access tokens refresh before expiry; refresh-token rotation is persisted.
- ChatGPT-authenticated requests use the Codex Responses streaming protocol and
  authenticated account ID. OpenAI API-key requests continue using Chat
  Completions unchanged.
- Live usage reads current ChatGPT/Codex rate-limit windows on every explicit
  request and exposes plan, used percentage, duration, and reset time when the
  backend provides them.
- Subscription limits and local token history are displayed separately. No
  dollar cost is inferred for subscription usage.

## Storage and concurrency

Credentials live at `<agent-home>/auth.json`, separate from behavioral config:

```json
{
  "version": 1,
  "providers": {
    "openai": {
      "version": 1,
      "type": "oauth",
      "expires_at": "...",
      "data": {}
    }
  }
}
```

- The agent-home directory is mode `0700` and `auth.json` is mode `0600` where
  supported.
- Writes are atomic and preserve unrelated provider credentials.
- Read-modify-write and token refresh use a cross-process lock. After acquiring
  the lock, refresh reloads the file and checks expiry again so concurrent CLI
  instances refresh only once.
- A failed refresh preserves the stored credential for explicit re-login and
  never falls back to an unrelated ambient credential.
- Storage APIs return safe metadata for listing; raw credentials never reach UI
  or logs.

## Threat model

Assets are API keys, OAuth access/refresh tokens, subscription entitlements, and
account/workspace identifiers. Trust boundaries are provider login UI, loopback
callbacks, device authorization, external token/model/usage endpoints, provider
plugins added in future, and the local credential file.

- Spoofing/tampering: validate OAuth state, use PKCE, bind callbacks only to
  loopback, accept only the expected callback path, require HTTPS for fixed
  remote endpoints, and validate all external response shapes.
- Information disclosure: restrictive permissions, redacted errors, no token or
  authorization-code logging, safe status DTOs, and no credentials in project
  config, sessions, usage history, or model prompts.
- Denial of service: HTTP timeouts, bounded response bodies, bounded polling and
  login deadlines, cancellation, and no unbounded retries.
- Privilege/billing confusion: status identifies credential source and method;
  explicit credentials never silently switch to a subscription or another
  account after failure.
- Malicious/buggy adapters: registration is compiled-in for this phase; adapters
  receive only their own credential and cannot enumerate the store.

## Project structure

```text
internal/auth/             contracts, registry, store, locking, resolution
internal/auth/openai/      OpenAI login, refresh, safe status, live usage
internal/provider/         ChatGPT Responses adapter beside Chat Completions
internal/config/           auth precedence and provider construction
internal/repl/             /login, /logout, /auth, and live /usage rendering
cmd/agent/                 auth subcommands and shell UI
docs/                      provider auth and usage documentation
```

The package name is deliberately generic. Future Claude/Kimi work adds an auth
adapter and, only when required, a matching model-wire variant; it does not add
new top-level commands or a second credential store.

## Commands

```shell
gofmt -w <changed-go-files>
go test ./internal/auth/... ./internal/provider ./internal/config ./internal/repl ./cmd/agent
go vet ./internal/auth/... ./internal/provider ./internal/config ./internal/repl ./cmd/agent
go test ./...
```

No new third-party dependency is planned.

## Testing strategy

- Registry/contract tests cover capability discovery, duplicate IDs,
  deterministic listing, optional usage, and safe errors.
- Store tests cover versioning, `0600` modes, atomic updates, preservation of
  other providers, corruption handling, cross-process refresh locking, and
  secret redaction.
- OpenAI tests use `httptest` for PKCE/state, browser callback, device polling,
  token rotation, JWT claims, bounded response parsing, and usage normalization.
- Provider tests cover Responses text/reasoning/tool streams, tool-result replay,
  usage totals, malformed SSE, HTTP errors, cancellation, and API-key regression.
- CLI/REPL tests cover command discovery, omitted-provider selection, auth
  precedence, login/logout/status, safe listings, live usage, and offline
  degradation.
- Focused tests and vet run after every thin slice; full `go test ./...` is the
  final gate.

## Boundaries

- Always: one auth registry/store; stable provider IDs; optional capabilities;
  validate all external data; redact secrets; use HTTPS, timeouts, atomic writes,
  restrictive modes, and locked refresh.
- Ask first: change command grammar, migrate existing keys, expose third-party
  runtime plugins, add dependencies, or reorder credential precedence.
- Never: add vendor-specific top-level login commands, scrape provider web
  sessions, store browser cookies, print tokens, commit credentials, merge
  subscription limits with API dollar estimates, or silently change accounts.

## Success criteria

1. `agent auth login openai` and `/login openai` complete browser or device login;
   OpenAI model/tool turns can then use the ChatGPT Codex entitlement.
2. `auth list/status/logout/usage` and `/auth` operate through the generic
   registry, with deterministic safe output and omitted-provider selection.
3. Existing OpenAI API-key behavior and every non-OpenAI provider remain
   unchanged.
4. Expired tokens refresh once across concurrent processes and rotated
   credentials persist without corrupting other providers.
5. `/usage` clearly separates local token history from freshly fetched
   subscription limits and degrades gracefully offline.
6. Adding a future Claude or Kimi subscription requires registering an adapter,
   not changing auth command dispatch, storage format, or generic rendering.
7. Focused tests, vet, and the full repository test suite pass.

## Not doing in the first release

- Claude, Kimi, or other subscription protocols: the extension points ship now;
  provider adapters follow separately.
- Migration of API keys out of existing config: compatibility is more valuable
  than a storage cleanup in the first slice.
- Runtime-loaded third-party auth plugins: compiled-in registration keeps the
  initial security boundary reviewable.
- A universal notion of money or remaining tokens: providers expose
  incomparable limits, so the generic model preserves labelled provider data.

## Open question

Approval of the `agent auth ...` plus `/login` and `/logout` command surface,
credential precedence, and generic contracts is required before implementation.
