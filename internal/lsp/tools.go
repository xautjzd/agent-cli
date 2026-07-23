package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/tool"
)

// maxLocations caps how many reference/definition locations are returned, so a
// very common symbol does not flood the model's context.
const maxLocations = 100

// resolve turns a possibly-relative path into an absolute one under workDir and
// reads its contents.
func resolve(workDir, path string) (abs, content string, err error) {
	if path == "" {
		return "", "", fmt.Errorf("path must not be empty")
	}
	abs = path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workDir, abs)
	}
	abs = filepath.Clean(abs)
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", "", err
	}
	return abs, string(data), nil
}

// rel renders an absolute path relative to workDir for compact display.
func rel(workDir, abs string) string {
	if r, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return abs
}

// formatLocations renders locations as "path:line:col", sorted and de-duped,
// with 1-based line/column numbers for human/model readability.
func formatLocations(workDir string, locs []Location) string {
	seen := map[string]bool{}
	var lines []string
	for _, l := range locs {
		s := fmt.Sprintf("%s:%d:%d", rel(workDir, uriToPath(l.URI)), l.Range.Start.Line+1, l.Range.Start.Character+1)
		if !seen[s] {
			seen[s] = true
			lines = append(lines, s)
		}
	}
	sort.Strings(lines)
	if len(lines) > maxLocations {
		extra := len(lines) - maxLocations
		lines = lines[:maxLocations]
		lines = append(lines, fmt.Sprintf("… and %d more", extra))
	}
	return strings.Join(lines, "\n")
}

// positionArgs is the shared argument shape for symbol-anchored tools.
type positionArgs struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
}

const positionSchema = `{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "File path, absolute or relative to the project root"},
		"line": {"type": "integer", "description": "1-based line number where the symbol appears (from read_file output)"},
		"symbol": {"type": "string", "description": "The exact identifier text on that line to locate"}
	},
	"required": ["path", "line", "symbol"]
}`

// Tools returns the LSP-backed tools for the given manager and working dir.
func Tools(m *Manager, workDir string) []tool.Tool {
	return []tool.Tool{
		&diagnosticsTool{m, workDir},
		&referencesTool{m, workDir},
		&definitionTool{m, workDir},
		&hoverTool{m, workDir},
	}
}

// --- lsp_diagnostics ----------------------------------------------------------

type diagnosticsTool struct {
	mgr     *Manager
	workDir string
}

func (t *diagnosticsTool) Name() string { return "lsp_diagnostics" }
func (t *diagnosticsTool) Description() string {
	return "Report compiler/linter diagnostics (errors, warnings) for a file from its language server. " +
		"Run this after editing a file to catch problems the edit introduced before moving on."
}
func (t *diagnosticsTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"path": {"type": "string", "description": "File path, absolute or relative to the project root"}},
		"required": ["path"]
	}`)
}
func (t *diagnosticsTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	abs, content, err := resolve(t.workDir, args.Path)
	if err != nil {
		return "", err
	}
	diags, err := t.mgr.Diagnostics(ctx, abs, content)
	if err != nil {
		return "", err
	}
	if len(diags) == 0 {
		return fmt.Sprintf("No diagnostics for %s.", rel(t.workDir, abs)), nil
	}
	sort.Slice(diags, func(i, j int) bool {
		if diags[i].Range.Start.Line != diags[j].Range.Start.Line {
			return diags[i].Range.Start.Line < diags[j].Range.Start.Line
		}
		return diags[i].Range.Start.Character < diags[j].Range.Start.Character
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic(s) in %s:\n", len(diags), rel(t.workDir, abs))
	for _, d := range diags {
		src := ""
		if d.Source != "" {
			src = " (" + d.Source + ")"
		}
		fmt.Fprintf(&b, "  %d:%d %s: %s%s\n",
			d.Range.Start.Line+1, d.Range.Start.Character+1, SeverityName(d.Severity), d.Message, src)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// --- lsp_references -----------------------------------------------------------

type referencesTool struct {
	mgr     *Manager
	workDir string
}

func (t *referencesTool) Name() string { return "lsp_references" }
func (t *referencesTool) Description() string {
	return "Find every reference to a symbol across the project using its language server — more accurate than " +
		"text search because it understands scope, imports, and shadowing. Give the file, the 1-based line the " +
		"symbol appears on, and the identifier text."
}
func (t *referencesTool) Schema() json.RawMessage { return json.RawMessage(positionSchema) }
func (t *referencesTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	args, pos, abs, content, err := resolvePosition(t.workDir, input)
	if err != nil {
		return "", err
	}
	locs, err := t.mgr.References(ctx, abs, content, pos)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return fmt.Sprintf("No references to %q found.", args.Symbol), nil
	}
	return fmt.Sprintf("References to %q:\n%s", args.Symbol, formatLocations(t.workDir, locs)), nil
}

// --- lsp_definition -----------------------------------------------------------

type definitionTool struct {
	mgr     *Manager
	workDir string
}

func (t *definitionTool) Name() string { return "lsp_definition" }
func (t *definitionTool) Description() string {
	return "Jump to where a symbol is defined using its language server. Give the file, the 1-based line the " +
		"symbol appears on, and the identifier text."
}
func (t *definitionTool) Schema() json.RawMessage { return json.RawMessage(positionSchema) }
func (t *definitionTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	args, pos, abs, content, err := resolvePosition(t.workDir, input)
	if err != nil {
		return "", err
	}
	locs, err := t.mgr.Definition(ctx, abs, content, pos)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return fmt.Sprintf("No definition found for %q.", args.Symbol), nil
	}
	return fmt.Sprintf("Definition of %q:\n%s", args.Symbol, formatLocations(t.workDir, locs)), nil
}

// --- lsp_hover ----------------------------------------------------------------

type hoverTool struct {
	mgr     *Manager
	workDir string
}

func (t *hoverTool) Name() string { return "lsp_hover" }
func (t *hoverTool) Description() string {
	return "Show a symbol's type signature and documentation from its language server (hover info). Give the " +
		"file, the 1-based line the symbol appears on, and the identifier text."
}
func (t *hoverTool) Schema() json.RawMessage { return json.RawMessage(positionSchema) }
func (t *hoverTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	args, pos, abs, content, err := resolvePosition(t.workDir, input)
	if err != nil {
		return "", err
	}
	text, err := t.mgr.Hover(ctx, abs, content, pos)
	if err != nil {
		return "", err
	}
	if text == "" {
		return fmt.Sprintf("No hover information for %q.", args.Symbol), nil
	}
	return text, nil
}

// resolvePosition parses the shared position arguments, reads the file, and
// computes the LSP position of the requested symbol.
func resolvePosition(workDir string, input json.RawMessage) (positionArgs, Position, string, string, error) {
	var args positionArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return args, Position{}, "", "", fmt.Errorf("invalid arguments: %w", err)
	}
	abs, content, err := resolve(workDir, args.Path)
	if err != nil {
		return args, Position{}, "", "", err
	}
	pos, err := locateSymbol(content, args.Symbol, args.Line)
	if err != nil {
		return args, Position{}, "", "", err
	}
	return args, pos, abs, content, nil
}
