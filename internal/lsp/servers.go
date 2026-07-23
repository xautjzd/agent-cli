package lsp

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
)

// ServerDef declares how to launch a language server and which files it owns.
type ServerDef struct {
	// Lang is the registry key and display name, e.g. "go".
	Lang string
	// Command and Args launch the server (stdio transport).
	Command string
	Args    []string
	// Env adds environment variables for the server process.
	Env map[string]string
	// Extensions are the file suffixes (with dot) this server handles.
	Extensions []string
	// Disabled removes this language from routing without deleting its entry.
	Disabled bool
}

// available reports whether the server's command is on PATH.
func (d ServerDef) available() bool {
	_, err := exec.LookPath(d.Command)
	return err == nil
}

// defaultServers are the built-in language servers, tried when the command is
// installed. Config can add to or override these by language name.
var defaultServers = []ServerDef{
	{Lang: "go", Command: "gopls", Extensions: []string{".go"}},
	{Lang: "typescript", Command: "typescript-language-server", Args: []string{"--stdio"},
		Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
	{Lang: "python", Command: "pyright-langserver", Args: []string{"--stdio"}, Extensions: []string{".py", ".pyi"}},
	{Lang: "rust", Command: "rust-analyzer", Extensions: []string{".rs"}},
	{Lang: "c", Command: "clangd", Extensions: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx"}},
}

// languageID maps a file extension to the LSP languageId a server expects for
// didOpen. The identifier affects how some servers (notably TypeScript) treat
// a file, so the common cases are spelled out.
func languageID(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	default:
		return strings.TrimPrefix(strings.ToLower(ext), ".")
	}
}

// baseName is filepath.Base, named for readability at the call site.
func baseName(p string) string { return filepath.Base(p) }

// parseLocations normalizes a definition/references result, which a server may
// return as a single Location or an array of them.
func parseLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arr []Location
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr
	}
	var one Location
	if json.Unmarshal(raw, &one) == nil && one.URI != "" {
		return []Location{one}
	}
	return nil
}

// flattenHover reduces the several shapes hover contents can take (a marked
// string, an array of them, or a MarkupContent object) to plain text.
func flattenHover(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// MarkupContent: {kind, value}
	var markup struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &markup) == nil && markup.Value != "" {
		return strings.TrimSpace(markup.Value)
	}
	// Plain string.
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return strings.TrimSpace(s)
	}
	// MarkedString {language, value} or an array of the above.
	var marked struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &marked) == nil && marked.Value != "" {
		return strings.TrimSpace(marked.Value)
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, item := range arr {
			if p := flattenHover(item); p != "" {
				parts = append(parts, p)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}
