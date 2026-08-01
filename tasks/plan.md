# Plan: extensible provider authentication

1. Define the generic auth contracts, deterministic registry, versioned store,
   atomic writes, and cross-process modification lock.
2. Add the first adapter: OpenAI browser/device OAuth, refresh, safe account
   metadata, and normalized live subscription usage.
3. Add the ChatGPT Codex Responses streaming provider while preserving the
   existing OpenAI API-key Chat Completions path.
4. Resolve auth with explicit-config-first precedence and expose the generic
   `agent auth` command family.
5. Reuse the same service in `/login`, `/logout`, `/auth`, and `/usage`; document,
   security-review, vet, and run the full test suite.

Risks are credential disclosure/corruption, OAuth callback spoofing, concurrent
refresh races, silent billing-source changes, and unstable provider wire
responses. Each boundary is isolated behind a tested contract before the next
slice is connected.
