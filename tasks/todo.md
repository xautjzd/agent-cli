# Automatic update tasks

- [x] Implement release lookup and version comparison.
  - Acceptance: newer stable releases are detected; malformed, equal, older,
    prerelease, oversized, timeout, and unavailable responses do not block.
  - Verify: `go test ./internal/update -run 'Version|Latest'`
  - Files: `internal/update/check.go`, `internal/update/check_test.go`

- [x] Implement checksum-verified atomic installation.
  - Acceptance: the correct platform archive is bounded, checksum verified,
    path-safe, extracted to the executable directory, and atomically installed
    without losing the old binary on failure.
  - Verify: `go test ./internal/update -run 'Install'`
  - Files: `internal/update/install.go`, `internal/update/install_test.go`

- [x] Implement the update choice screen.
  - Acceptance: current and latest versions, release notes, and Update/Skip/Exit
    choices render and work by arrow keys and number keys.
  - Verify: `go test ./internal/update -run 'Prompt|Model'`
  - Files: `internal/update/prompt.go`, `internal/update/prompt_test.go`

- [ ] Integrate the interactive startup gate.
  - Acceptance: only an interactive release launch checks; skip continues, exit
    ends cleanly, disabled/dev/non-interactive paths do no network work.
  - Verify: `go test ./cmd/agent`
  - Files: `cmd/agent/main.go`, `cmd/agent/main_test.go`

- [ ] Complete review and verification.
  - Acceptance: docs match behavior, all checks pass, no secrets or unsafe
    remote execution exists, and the result is committed to `main`.
  - Verify: `go vet ./internal/update ./cmd/agent && go test ./...`
  - Files: `docs/automatic-updates.md`, `tasks/todo.md`
