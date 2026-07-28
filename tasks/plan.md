# Plan: automatic release update prompt

1. Build a pure update client that validates versions and bounded GitHub
   responses.
2. Add verified archive installation with platform-specific replacement
   support.
3. Add the interactive Codex-style choice screen.
4. Gate only interactive release startup in `cmd/agent`.
5. Document, run focused/full verification, security review, and commit.

Risks are untrusted release metadata/assets, partial binary replacement, slow or
unavailable networks, accidental prompts in automation, and terminal residue.
Each is verified before the next slice.
