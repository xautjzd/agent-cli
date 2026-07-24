// Package version is the single source of truth for the CLI's release version,
// so the `version` subcommand, the startup banner, and the MCP handshake never
// drift apart.
package version

// Version is the current release. Bump it here on each release.
const Version = "0.1.0"
