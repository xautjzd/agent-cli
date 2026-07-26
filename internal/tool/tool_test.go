package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runTool(t *testing.T, tl Tool, args string) (string, error) {
	t.Helper()
	return tl.Execute(context.Background(), json.RawMessage(args))
}

func TestEditFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("hello world\nhello again\n"), 0o644)
	edit := &EditFile{WorkDir: dir}

	// Ambiguous match must be rejected with a count.
	_, err := runTool(t, edit, `{"path":"a.txt","old_string":"hello","new_string":"hi"}`)
	if err == nil || !strings.Contains(err.Error(), "2 locations") {
		t.Fatalf("ambiguous edit should fail with count, got %v", err)
	}

	// Unique match succeeds.
	if _, err := runTool(t, edit, `{"path":"a.txt","old_string":"hello world","new_string":"hi world"}`); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "hi world") {
		t.Errorf("edit not applied: %q", data)
	}

	// replace_all handles multiple occurrences.
	if _, err := runTool(t, edit, `{"path":"a.txt","old_string":"world","new_string":"go","replace_all":true}`); err != nil {
		t.Fatal(err)
	}

	// Missing old_string errors.
	if _, err := runTool(t, edit, `{"path":"a.txt","old_string":"absent","new_string":"x"}`); err == nil {
		t.Error("expected not-found error")
	}
}

func TestEditFileWhitespaceTolerant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	// The file is tab-indented with a trailing space the model won't reproduce.
	os.WriteFile(path, []byte("func f() {\n\ta := 1 \n\tb := 2\n}\n"), 0o644)
	edit := &EditFile{WorkDir: dir}

	// The model supplies the block with no indentation and no trailing space.
	out, err := runTool(t, edit, `{"path":"main.go","old_string":"a := 1\nb := 2","new_string":"a := 10\nb := 20"}`)
	if err != nil {
		t.Fatalf("whitespace-tolerant edit should succeed, got %v", err)
	}
	if !strings.Contains(out, "whitespace tolerance") {
		t.Errorf("fuzzy match should be flagged in the report: %s", out)
	}
	data, _ := os.ReadFile(path)
	// The replacement lands re-indented with tabs, structure preserved.
	if string(data) != "func f() {\n\ta := 10\n\tb := 20\n}\n" {
		t.Errorf("tolerant edit wrong result: %q", data)
	}
}

func TestEditFileNotFoundHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\ndelta\n"), 0o644)
	edit := &EditFile{WorkDir: dir}
	// One wrong line means no tier matches; the error should hint at the region.
	_, err := runTool(t, edit, `{"path":"x.txt","old_string":"beta\nWRONG\ndelta","new_string":"z"}`)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected nearest-region hint, got %v", err)
	}
}

func TestEditFileReportsDiff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	os.WriteFile(path, []byte("package main\n\nfunc old() {}\n\nvar x = 1\n"), 0o644)
	edit := &EditFile{WorkDir: dir}

	out, err := runTool(t, edit, `{"path":"code.go","old_string":"func old() {}","new_string":"func renamed() {}"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Header carries the change counts…
	if !strings.HasPrefix(out, "Edited") || !strings.Contains(out, "(+1 -1)") {
		t.Errorf("header wrong:\n%s", out)
	}
	// …and the body is a line-numbered unified diff the renderer colorizes.
	if !strings.Contains(out, "- func old() {}") || !strings.Contains(out, "+ func renamed() {}") {
		t.Errorf("diff body missing:\n%s", out)
	}
	// Surrounding context is included for orientation.
	if !strings.Contains(out, "package main") {
		t.Errorf("diff lacks context:\n%s", out)
	}
}

func TestWriteFileDiffsOnlyOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkDir: dir}

	// A brand-new file reports creation, not a diff against nothing.
	out, err := runTool(t, w, `{"path":"new.txt","content":"a\nb\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Created") || strings.Contains(out, "+ a") {
		t.Errorf("new file should not render a diff:\n%s", out)
	}

	// Overwriting an existing file shows what changed.
	out, err = runTool(t, w, `{"path":"new.txt","content":"a\nCHANGED\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(+1 -1)") || !strings.Contains(out, "+ CHANGED") {
		t.Errorf("overwrite should diff:\n%s", out)
	}
}

func TestReadWriteFile(t *testing.T) {
	dir := t.TempDir()
	w := &WriteFile{WorkDir: dir}
	r := &ReadFile{WorkDir: dir}

	if _, err := runTool(t, w, `{"path":"sub/new.txt","content":"line1\nline2\nline3"}`); err != nil {
		t.Fatal(err)
	}
	out, err := runTool(t, r, `{"path":"sub/new.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1\tline1") || !strings.Contains(out, "3\tline3") {
		t.Errorf("expected numbered lines, got %q", out)
	}

	// Offset/limit windowing.
	out, err = runTool(t, r, `{"path":"sub/new.txt","offset":2,"limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line2") || strings.Contains(out, "line1") {
		t.Errorf("window wrong: %q", out)
	}
}

// TestReadFileRefusesBinary pins the guard that keeps binary noise out of the
// context window: a NUL-bearing file is refused, and an image is refused with a
// pointer to the @path attachment mechanism that does work.
func TestReadFileRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	r := &ReadFile{WorkDir: dir}

	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("ELF\x00\x01\x02rest"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A NUL past the sniff window must not count: only the leading bytes decide.
	long := append(bytes.Repeat([]byte("text\n"), 4000), 0x00)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), long, 0o644); err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runTool(t, r, `{"path":"a.bin"}`)
	if err == nil {
		t.Fatalf("expected binary file to be refused, got %q", out)
	}
	if !strings.Contains(err.Error(), "binary file") {
		t.Errorf("error should name the problem, got %q", err)
	}

	out, err = runTool(t, r, `{"path":"shot.png"}`)
	if err == nil {
		t.Fatalf("expected image to be refused, got %q", out)
	}
	// The hint must spell the ref the way the model did, so a retry works.
	if !strings.Contains(err.Error(), `"@shot.png"`) {
		t.Errorf("image error should point at @path attachment, got %q", err)
	}

	// A text file larger than the sniff window still reads in full.
	out, err = runTool(t, r, `{"path":"long.txt","offset":3999,"limit":2}`)
	if err != nil {
		t.Fatalf("text file beyond the sniff window must read: %v", err)
	}
	if !strings.Contains(out, "text") {
		t.Errorf("expected content past the sniff window, got %q", out)
	}
}

// TestEditFileRefusesBinary covers the same guard on the edit path, where a
// matched replacement would otherwise emit a diff of binary bytes.
func TestEditFileRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(path, []byte("head\x00tail"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &EditFile{WorkDir: dir}
	if out, err := runTool(t, e, `{"path":"a.bin","old_string":"head","new_string":"HEAD"}`); err == nil {
		t.Fatalf("expected binary edit to be refused, got %q", out)
	}
	// The file must be untouched by the refusal.
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "head\x00tail" {
		t.Errorf("file was modified despite refusal: %q (%v)", data, err)
	}
}

func TestGlobAndGrep(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg", "sub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc Hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "pkg", "sub", "util.go"), []byte("package sub\n// Hello helper\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "node_modules", "x", "skip.go"), []byte("Hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("Hello docs"), 0o644)

	g := &Glob{WorkDir: dir}
	out, err := runTool(t, g, `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.go") || !strings.Contains(out, filepath.Join("pkg", "sub", "util.go")) {
		t.Errorf("glob missed files: %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("glob should skip node_modules: %q", out)
	}
	if strings.Contains(out, "readme.md") {
		t.Errorf("glob matched wrong extension: %q", out)
	}

	gr := &Grep{WorkDir: dir}
	out, err = runTool(t, gr, `{"pattern":"Hello","glob":"**/*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.go:2") {
		t.Errorf("grep missed match with line number: %q", out)
	}
	if strings.Contains(out, "readme.md") {
		t.Errorf("grep glob filter failed: %q", out)
	}
}

func TestBash(t *testing.T) {
	dir := t.TempDir()
	b := &Bash{WorkDir: dir}
	out, err := runTool(t, b, `{"command":"echo hi && pwd"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hi") {
		t.Errorf("missing stdout: %q", out)
	}
	// Non-zero exit is reported as content, not a Go error.
	out, err = runTool(t, b, `{"command":"exit 3"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exit") {
		t.Errorf("expected exit info, got %q", out)
	}
}

func TestRegistryExecuteUnknownTool(t *testing.T) {
	r := NewRegistry(&ListDir{WorkDir: t.TempDir()})
	out, ok := r.Execute(context.Background(), "nope", json.RawMessage(`{}`))
	if ok || !strings.Contains(out, "unknown tool") {
		t.Errorf("expected failed recovery message, got ok=%v %q", ok, out)
	}

	// A successful call reports ok=true.
	out, ok = r.Execute(context.Background(), "list_dir", json.RawMessage(`{}`))
	if !ok {
		t.Errorf("list_dir should succeed, got %q", out)
	}
}
