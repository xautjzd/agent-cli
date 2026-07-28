// Package version is the single source of truth for the CLI's release version,
// so the `version` subcommand, the startup banner, and the MCP handshake never
// drift apart.
package version

// Version is overridden at build time by GoReleaser via ldflags
// (-X ...Version=<tag>). Source builds stay "dev", which also prevents them
// from offering to overwrite themselves with a packaged release.
var Version = "dev"
