package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xautjzd/agent-cli/internal/diff"
	"github.com/xautjzd/agent-cli/internal/editmatch"
)

// changeReport renders the result of a file modification: a summary line
// plus a unified diff. The diff goes to the model as well as the user — it
// is the most precise confirmation of what actually changed, and lets the
// model catch a mis-applied edit without re-reading the file.
func changeReport(verb, path, before, after string) string {
	d := diff.Compute(path, before, after, diff.DefaultOptions())
	header := fmt.Sprintf("%s %s %s", verb, path, d.Summary())
	body := d.Unified()
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// resolvePath makes model-supplied paths absolute relative to the workdir
// and rejects escapes above it via "..", keeping file tools scoped to the
// project (defense in depth; bash intentionally has broader reach).
func resolvePath(workDir, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}
	return filepath.Clean(p), nil
}

// ReadFile returns file contents with line numbers so the model can
// reference exact locations when editing.
type ReadFile struct{ WorkDir string }

func (t *ReadFile) Name() string { return "read_file" }

func (t *ReadFile) Description() string {
	return "Read a file and return its content with line numbers. Supports optional offset/limit for large files."
}

func (t *ReadFile) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File path, absolute or relative to the project root"},
			"offset": {"type": "integer", "description": "1-based line to start from (default 1)"},
			"limit": {"type": "integer", "description": "Max lines to return (default 2000)"}
		},
		"required": ["path"]
	}`)
}

func (t *ReadFile) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	path, err := resolvePath(t.WorkDir, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	start := args.Offset
	if start < 1 {
		start = 1
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 2000
	}
	if start > len(lines) {
		return "", fmt.Errorf("offset %d beyond end of file (%d lines)", start, len(lines))
	}
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	for i := start - 1; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(lines)-end)
	}
	return sb.String(), nil
}

// FileSnapshotter captures a file's pre-change contents so an edit can later
// be undone (the /rewind checkpoint mechanism). The file tools depend only on
// this small interface (DIP); a nil snapshotter disables it, leaving the tools
// fully usable without the checkpoint layer.
type FileSnapshotter interface {
	// SnapshotFile records the current contents of an absolute path before it
	// is modified.
	SnapshotFile(path string)
}

// WriteFile creates or overwrites a file, creating parent directories.
type WriteFile struct {
	WorkDir string
	// Snapshot, when set, is asked to capture the file's prior state before
	// each write so /rewind can restore it.
	Snapshot FileSnapshotter
}

func (t *WriteFile) Name() string { return "write_file" }

func (t *WriteFile) Description() string {
	return "Create or overwrite a file with the given content. Parent directories are created automatically."
}

func (t *WriteFile) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File path, absolute or relative to the project root"},
			"content": {"type": "string", "description": "Full file content to write"}
		},
		"required": ["path", "content"]
	}`)
}

func (t *WriteFile) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	path, err := resolvePath(t.WorkDir, args.Path)
	if err != nil {
		return "", err
	}
	// Capture the prior state before touching the file, so /rewind can undo
	// this write (including creation of a new file).
	if t.Snapshot != nil {
		t.Snapshot.SnapshotFile(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	// Read the previous contents first so an overwrite can be reported as a
	// diff; a missing file simply diffs against empty.
	before, _ := os.ReadFile(path)
	existed := len(before) > 0
	if err := os.WriteFile(path, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	if !existed {
		return fmt.Sprintf("Created %s (%d bytes, %d lines)",
			path, len(args.Content), strings.Count(args.Content, "\n")+1), nil
	}
	return changeReport("Wrote", path, string(before), args.Content), nil
}

// EditFile replaces a snippet of a file. It anchors on the surrounding lines
// and tolerates whitespace/indentation differences (via internal/editmatch),
// so an edit no longer fails on a stray space or a differing indent — while
// still refusing ambiguous matches so a change never lands in the wrong place.
type EditFile struct {
	WorkDir string
	// Snapshot, when set, captures the file's prior state before the edit so
	// /rewind can restore it.
	Snapshot FileSnapshotter
}

func (t *EditFile) Name() string { return "edit_file" }

func (t *EditFile) Description() string {
	return "Replace a snippet of text in a file. Provide old_string with enough surrounding context " +
		"to identify one unique location, and new_string to replace it with. Matching is whitespace- and " +
		"indentation-tolerant (leading/trailing spaces and indent level need not match exactly), and the " +
		"replacement is re-indented to fit. old_string must resolve to exactly one location unless replace_all is true."
}

func (t *EditFile) Schema() json.RawMessage {
	return mustSchema(`{
		"type": "object",
		"properties": {
			"path": {"type": "string", "description": "File path, absolute or relative to the project root"},
			"old_string": {"type": "string", "description": "Exact text to replace"},
			"new_string": {"type": "string", "description": "Replacement text"},
			"replace_all": {"type": "boolean", "description": "Replace every occurrence (default false)"}
		},
		"required": ["path", "old_string", "new_string"]
	}`)
}

// Execute validates uniqueness before writing: an ambiguous match is
// rejected with an actionable count so the model can add context and retry.
func (t *EditFile) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Path       string `json:"path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.OldString == args.NewString {
		return "", fmt.Errorf("old_string and new_string are identical")
	}
	path, err := resolvePath(t.WorkDir, args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	// Snapshot the original before the edit lands, so /rewind can restore it.
	if t.Snapshot != nil {
		t.Snapshot.SnapshotFile(path)
	}

	res, err := editmatch.Replace(content, args.OldString, args.NewString, args.ReplaceAll)
	if err != nil {
		// The matcher's errors already carry actionable guidance (ambiguity
		// counts, nearest-region hints); prefix the path for context.
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(res.Updated), 0o644); err != nil {
		return "", err
	}

	verb := "Edited"
	if res.Count > 1 {
		verb = fmt.Sprintf("Edited (%d occurrences in)", res.Count)
	}
	report := changeReport(verb, path, content, res.Updated)
	// When a fuzzy tier matched, tell the model the match was not exact so it
	// can double-check the diff — the whitespace it sent did not match the
	// file byte-for-byte.
	if res.Fuzzy() {
		report = fmt.Sprintf("(matched with whitespace tolerance: %s)\n%s", res.Strategy, report)
	}
	return report, nil
}
