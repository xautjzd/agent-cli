// Package version is the single source of truth for the CLI's release version,
// so the `version` subcommand, the startup banner, and the MCP handshake never
// drift apart.
package version

// Version is the current release. Overridden at build time by GoReleaser
// via ldflags (-X ...Version=<tag>); the literal is a fallback for
// development builds without the injection.
var Version = "0.1.0"
