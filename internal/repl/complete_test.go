package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xautjzd/agent-cli/internal/config"
)

// TestInputBoxStaysOneRow guards the reported paste-then-type bug: the
// bordered input box must stay exactly 3 rows tall (top border, one text
// row, bottom border) and never render wider than the terminal, for any
// prompt, width, or content — CJK and long input included.
func TestInputBoxStaysOneRow(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	prompts := []string{"> ", "plan> "}
	widths := []int{40, 80, 120, 150}
	contents := []string{
		"[Image #3] 的",
		"[Image #12] 请分析这张图片的内容并给出建议",
		strings.Repeat("的", 300),
		strings.Repeat("x", 300),
		"",
	}
	for _, prompt := range prompts {
		for _, w := range widths {
			for _, content := range contents {
				m := newEditorModel(r, prompt)
				m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
				m.input.SetValue(content)
				m.input.CursorEnd()

				box := styleInputBox.Width(m.boxContentWidth()).MaxHeight(3)
				rendered := box.Render(m.input.View())
				if h := strings.Count(rendered, "\n") + 1; h != 3 {
					t.Errorf("box height = %d (want 3): prompt=%q width=%d content=%q",
						h, prompt, w, content)
				}
				for _, line := range strings.Split(rendered, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Errorf("line width %d > terminal %d: prompt=%q content=%q",
							lw, w, prompt, content)
					}
				}
			}
		}
	}
}

func TestTokenBounds(t *testing.T) {
	cases := []struct {
		value      string
		pos        int
		start, end int
	}{
		{"/mo", 3, 0, 3},
		{"/model gpt", 10, 7, 10},
		{"explain @ma please", 11, 8, 11},
		{"", 0, 0, 0},
		{"abc", 1, 0, 3},
	}
	for _, c := range cases {
		s, e := tokenBounds(c.value, c.pos)
		if s != c.start || e != c.end {
			t.Errorf("tokenBounds(%q,%d) = %d,%d want %d,%d", c.value, c.pos, s, e, c.start, c.end)
		}
	}
}

func TestCompletionsForCommands(t *testing.T) {
	r, _, _ := newTestRepl(t, "")

	// "/" alone lists every command (plus skills) — none may fall off.
	cands := r.completionsFor("/", 1)
	if len(cands) == 0 || cands[0].text != "/help" {
		t.Fatalf("bare slash candidates wrong: %+v", cands)
	}
	got := map[string]bool{}
	for _, c := range cands {
		got[c.text] = true
	}
	for _, c := range commands {
		if !got["/"+c.name] {
			t.Errorf("command /%s missing from bare-slash candidates", c.name)
		}
	}
	if !got["/demo"] {
		t.Error("skill /demo missing from bare-slash candidates")
	}

	// Prefix match ranks /model first for "/mo".
	cands = r.completionsFor("/mo", 3)
	if len(cands) == 0 || cands[0].text != "/model" {
		t.Errorf("/mo candidates = %+v", cands)
	}

	// Skills are included and marked.
	cands = r.completionsFor("/de", 3)
	found := false
	for _, c := range cands {
		if c.text == "/demo" && strings.HasPrefix(c.desc, "skill:") {
			found = true
		}
	}
	if !found {
		t.Errorf("skill not offered: %+v", cands)
	}

	// A slash later in the line is not a command position.
	if cands := r.completionsFor("look at a/b", 11); cands != nil {
		t.Errorf("mid-line slash should not complete: %+v", cands)
	}
}

// TestEffortArgCompletionMarksCurrent verifies the "/effort " argument popup
// flags the active level as current and opens highlighting it, instead of
// always landing on the first (alphabetical) row.
func TestEffortArgCompletionMarksCurrent(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Cfg.Thinking = "low"

	cands := r.completionsFor("/effort ", len("/effort "))
	if len(cands) == 0 {
		t.Fatal("no effort candidates")
	}
	curIdx := -1
	for i, c := range cands {
		if c.current {
			if c.text != "low" {
				t.Fatalf("current flag on %q, want low", c.text)
			}
			curIdx = i
		}
	}
	if curIdx < 0 {
		t.Fatalf("no candidate marked current: %+v", cands)
	}
	// The popup pre-selects the marked candidate rather than index 0.
	if got := defaultCandIdx(cands); got != curIdx {
		t.Errorf("default selection = %d, want current index %d", got, curIdx)
	}
}

// TestCommandCompletesArgs verifies the TUI can tell which bare commands have
// a value picker to open on submit ("/effort") from those that don't.
func TestCommandCompletesArgs(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	// effort/mode have static value sets, so the picker always opens.
	// (model/provider depend on catalog data and only open when non-empty.)
	for _, cmd := range []string{"effort", "mode"} {
		if !r.commandCompletesArgs(cmd) {
			t.Errorf("commandCompletesArgs(%q) = false, want true", cmd)
		}
	}
	// theme is intentionally excluded: it uses its own live-preview picker.
	for _, cmd := range []string{"theme", "new", "exit", "help", ""} {
		if r.commandCompletesArgs(cmd) {
			t.Errorf("commandCompletesArgs(%q) = true, want false", cmd)
		}
	}
}

func TestCompletionsForFiles(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	os.WriteFile(filepath.Join(r.WorkDir, "main.go"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(r.WorkDir, "docs"), 0o755)
	os.WriteFile(filepath.Join(r.WorkDir, "docs", "guide.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(r.WorkDir, "node_modules", "junk"), 0o755)
	os.WriteFile(filepath.Join(r.WorkDir, "node_modules", "junk", "x.js"), []byte("x"), 0o644)

	cands := r.completionsFor("read @ma", 8)
	if len(cands) == 0 || cands[0].text != "@main.go" {
		t.Fatalf("@ma candidates = %+v", cands)
	}

	// Basename matching finds nested files.
	r.fileCache = nil
	cands = r.completionsFor("@guide", 6)
	if len(cands) == 0 || cands[0].text != "@docs/guide.md" {
		t.Errorf("@guide candidates = %+v", cands)
	}

	// Skip dirs are excluded.
	r.fileCache = nil
	for _, c := range r.completionsFor("@", 1) {
		if strings.Contains(c.text, "node_modules") {
			t.Errorf("node_modules leaked into candidates: %+v", c)
		}
	}

	// No @ token, no candidates.
	if cands := r.completionsFor("plain text", 5); cands != nil {
		t.Errorf("unexpected candidates: %+v", cands)
	}

	// "@" glued to CJK text with no leading space still completes — the common
	// case for Chinese prompts like "看一下@main".
	r.fileCache = nil
	value := "看一下@ma"
	if cands := r.completionsFor(value, len(value)); len(cands) == 0 || cands[0].text != "@main.go" {
		t.Errorf("CJK-glued @ candidates = %+v", cands)
	}

	// An email address is not a file reference: the "@" follows a word byte.
	r.fileCache = nil
	if cands := r.completionsFor("mail user@ma", 12); cands != nil {
		t.Errorf("email should not complete as a file ref: %+v", cands)
	}
}

func TestAcceptCandidate(t *testing.T) {
	value, pos := acceptCandidate("/mo", 3, candidate{text: "/model"})
	if value != "/model " || pos != 7 {
		t.Errorf("accept = %q pos=%d", value, pos)
	}

	// Mid-line token replacement keeps surrounding text.
	value, pos = acceptCandidate("explain @ma please", 11, candidate{text: "@main.go"})
	if value != "explain @main.go please" || pos != 16 {
		t.Errorf("accept = %q pos=%d", value, pos)
	}

	// "@" glued to CJK text replaces only the ref, not the preceding words.
	src := "看一下@ma"
	value, _ = acceptCandidate(src, len(src), candidate{text: "@main.go"})
	if value != "看一下@main.go " {
		t.Errorf("CJK accept = %q", value)
	}
}

// TestEditorFileCompletionAfterCJK reproduces the reported bug: typing "@"
// after a line that begins with CJK text must still open the file popup. The
// editor drives completion off input.Position(), which is a rune index, so this
// guards the rune→byte conversion the token helpers depend on.
func TestEditorFileCompletionAfterCJK(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	os.WriteFile(filepath.Join(r.WorkDir, "main.go"), []byte("x"), 0o644)

	m := newEditorModel(r, "> ")
	typeText(m, "看一下@ma")
	if len(m.cands) == 0 || m.cands[0].text != "@main.go" {
		t.Fatalf("no @ popup after CJK prefix: %+v", m.cands)
	}

	// Accepting inserts the path without corrupting the leading CJK text.
	key(m, tea.KeyEnter)
	if got := m.input.Value(); got != "看一下@main.go " {
		t.Fatalf("accept after CJK = %q", got)
	}
}

// key sends one key to the editor model.
func key(m *editorModel, k tea.KeyType) { m.Update(tea.KeyMsg{Type: k}) }

func typeText(m *editorModel, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestEditorEnterPolicy(t *testing.T) {
	r, _, _ := newTestRepl(t, "")

	// Typing "/mo" then Enter first completes to "/model ", second Enter submits.
	m := newEditorModel(r, "> ")
	typeText(m, "/mo")
	if len(m.cands) == 0 || m.cands[m.sel].text != "/model" {
		t.Fatalf("popup not showing /model: %+v", m.cands)
	}
	key(m, tea.KeyEnter)
	if m.done {
		t.Fatal("first Enter should accept, not submit")
	}
	if got := m.input.Value(); got != "/model " {
		t.Fatalf("after accept value = %q", got)
	}
	key(m, tea.KeyEnter)
	if !m.done || m.result != "/model " {
		t.Errorf("second Enter should submit, got done=%v result=%q", m.done, m.result)
	}

	// A fully typed command submits on the first Enter.
	m = newEditorModel(r, "> ")
	typeText(m, "/model")
	key(m, tea.KeyEnter)
	if !m.done || m.result != "/model" {
		t.Errorf("exact command should submit immediately, done=%v result=%q", m.done, m.result)
	}
}

func TestEditorNavigationAndDismiss(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	m := newEditorModel(r, "> ")
	typeText(m, "/s")
	if len(m.cands) < 2 {
		t.Fatalf("expected multiple /s candidates, got %+v", m.cands)
	}
	first := m.cands[m.sel].text
	key(m, tea.KeyDown)
	if m.cands[m.sel].text == first {
		t.Error("Down did not move selection")
	}
	key(m, tea.KeyEsc)
	if m.cands != nil {
		t.Error("Esc did not dismiss popup")
	}
	key(m, tea.KeyCtrlC)
	if !m.abort {
		t.Error("Ctrl-C did not abort")
	}
}

func TestEditorPopupScrollsToSelection(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	m := newEditorModel(r, "> ")
	typeText(m, "/")
	if len(m.cands) <= popupRows {
		t.Skipf("need more than %d candidates to test scrolling, got %d", popupRows, len(m.cands))
	}

	// Initially the window starts at the top and reports hidden rows below.
	view := m.View()
	if !strings.Contains(view, "↓") || !strings.Contains(view, "more") {
		t.Errorf("expected below-window indicator:\n%s", view)
	}

	// Walk the selection past the visible window: the highlighted candidate
	// must always be rendered.
	for i := 0; i < len(m.cands)-1; i++ {
		key(m, tea.KeyDown)
	}
	last := m.cands[m.sel].text
	view = m.View()
	if !strings.Contains(view, last) {
		t.Errorf("selected %q not visible after scrolling:\n%s", last, view)
	}
	if !strings.Contains(view, "↑") {
		t.Errorf("expected above-window indicator at list end:\n%s", view)
	}

	// /goal specifically is selectable via arrows from a bare slash.
	m = newEditorModel(r, "> ")
	typeText(m, "/")
	found := false
	for i := 0; i < len(m.cands); i++ {
		if m.cands[m.sel].text == "/goal" {
			found = true
			break
		}
		key(m, tea.KeyDown)
	}
	if !found {
		t.Fatal("/goal never became selectable")
	}
	if !strings.Contains(m.View(), "/goal") {
		t.Error("/goal selected but not rendered in window")
	}
}

func TestEditorInputBoxAndCollapse(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	m := newEditorModel(r, "> ")
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	typeText(m, "hello world")

	// While editing, the input is framed to separate it from scrollback.
	view := m.View()
	if !strings.Contains(view, "╭") || !strings.Contains(view, "╰") || !strings.Contains(view, "│") {
		t.Errorf("expected rounded frame around pending input:\n%s", view)
	}
	if !strings.Contains(view, "hello world") {
		t.Errorf("input text missing from framed view:\n%s", view)
	}

	// The popup renders below the frame and stays visible.
	m2 := newEditorModel(r, "> ")
	m2.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	typeText(m2, "/mo")
	v2 := m2.View()
	if !strings.Contains(v2, "╰") || !strings.Contains(v2, "/model") {
		t.Errorf("popup should render below the frame:\n%s", v2)
	}

	// After submit, the frame collapses to a compact marker line.
	key(m, tea.KeyEnter)
	final := m.View()
	if strings.Contains(final, "╭") {
		t.Errorf("submitted line must not keep the frame:\n%s", final)
	}
	if !strings.Contains(final, "❯") || !strings.Contains(final, "hello world") {
		t.Errorf("collapsed line wrong:\n%s", final)
	}

	// Non-default prompts stay visible in the collapsed line.
	m3 := newEditorModel(r, "plan> ")
	typeText(m3, "task")
	key(m3, tea.KeyEnter)
	if !strings.Contains(m3.View(), "plan> task") {
		t.Errorf("plan prompt lost on collapse:\n%s", m3.View())
	}
}

func TestProviderAndModelArgumentCompletion(t *testing.T) {
	r, _, _ := newTestRepl(t, "")

	// "/provider za" suggests catalog providers.
	cands := r.completionsFor("/provider za", 12)
	if len(cands) == 0 {
		t.Fatal("no provider suggestions")
	}
	if cands[0].text != "zai" {
		t.Errorf("first suggestion = %q, want zai", cands[0].text)
	}
	if cands[0].desc == "" {
		t.Error("suggestion should carry a description")
	}

	// Configured profiles are offered alongside the built-ins.
	r.Cfg.Providers = map[string]config.ProviderConfig{
		"my-gateway": {BaseURL: "https://x/v1"},
	}
	cands = r.completionsFor("/provider my", 12)
	found := false
	for _, c := range cands {
		if c.text == "my-gateway" {
			found = true
		}
	}
	if !found {
		t.Errorf("profile not offered: %+v", cands)
	}

	// "/model" completes against the *current* provider's models.
	r.Cfg.Provider = "anthropic"
	cands = r.completionsFor("/model claude-op", 16)
	if len(cands) == 0 {
		t.Fatal("no model suggestions")
	}
	for _, c := range cands {
		if !strings.HasPrefix(c.text, "claude-op") {
			t.Errorf("unrelated model suggested: %q", c.text)
		}
	}

	// "/mode " offers the permission modes, matching /model's behavior.
	cands = r.completionsFor("/mode ", 6)
	if len(cands) != 2 {
		t.Fatalf("mode suggestions = %+v, want hitl and bypass", cands)
	}
	if cands[0].text != "bypass" || cands[1].text != "hitl" {
		t.Errorf("mode suggestions = %+v, want bypass then hitl (sorted)", cands)
	}
	cands = r.completionsFor("/mode by", 8)
	if len(cands) != 1 || cands[0].text != "bypass" {
		t.Errorf("/mode by = %+v, want bypass only", cands)
	}

	// A bare command with no argument yet still completes command names,
	// not arguments.
	if cands := r.completionsFor("/provi", 6); len(cands) == 0 || cands[0].text != "/provider" {
		t.Errorf("command completion regressed: %+v", cands)
	}
	// Commands without known arguments produce nothing.
	if cands := r.completionsFor("/goal something", 15); cands != nil {
		t.Errorf("/goal should not complete arguments: %+v", cands)
	}
	// "/provider <name> <model>" completes the named provider's models, not
	// the currently active provider's. anthropic is active from above.
	cands = r.completionsFor("/provider glm glm-4", 19)
	if len(cands) == 0 {
		t.Fatal("no model suggestions for /provider glm")
	}
	for _, c := range cands {
		if !strings.HasPrefix(c.text, "glm-4") {
			t.Errorf("unrelated model suggested for glm: %q", c.text)
		}
	}

	// A third argument is free text — completion stops after the model.
	if cands := r.completionsFor("/provider glm glm-4 extra", 25); cands != nil {
		t.Errorf("third argument should not complete: %+v", cands)
	}
	// Other commands still stop after their first argument.
	if cands := r.completionsFor("/model claude-3 extra", 21); cands != nil {
		t.Errorf("/model second argument should not complete: %+v", cands)
	}
}

func TestHistoryRecall(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.historyAdd("first")
	r.historyAdd("second")

	if got := r.historyPrev("draft"); got != "second" {
		t.Errorf("prev = %q", got)
	}
	if got := r.historyPrev("x"); got != "first" {
		t.Errorf("prev2 = %q", got)
	}
	if got := r.historyNext(); got != "second" {
		t.Errorf("next = %q", got)
	}
	if got := r.historyNext(); got != "draft" {
		t.Errorf("draft not restored: %q", got)
	}
}

// The effort popup must follow the ladder (adaptive → strengths → off), not
// the alphabet: sorting it by name reads as a scrambled scale.
func TestEffortCompletionKeepsLadderOrder(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Cfg.Model = "claude-fable-5"

	cands := r.completionsFor("/effort ", 8)
	got := make([]string, len(cands))
	for i, c := range cands {
		got[i] = c.text
	}
	want := []string{"adaptive", "low", "medium", "high", "xhigh", "max", "off"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("effort completion order = %v, want %v", got, want)
	}

	// A model with a different set keeps the same relative order.
	r.Cfg.Model = "kimi-k3"
	cands = r.completionsFor("/effort ", 8)
	got = got[:0]
	for _, c := range cands {
		got = append(got, c.text)
	}
	if strings.Join(got, " ") != "adaptive low high max" {
		t.Errorf("kimi-k3 effort completion = %v", got)
	}
}

// The provider popup follows the catalog's own order (frontier labs first,
// aggregators and local runtimes last), with config profiles ahead of it —
// alphabetizing would reshuffle a list users learn by position.
func TestProviderCompletionKeepsCatalogOrder(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Cfg.Providers = map[string]config.ProviderConfig{
		"work":    {BaseURL: "https://a/v1"},
		"backup":  {BaseURL: "https://b/v1"},
		"minimax": {BaseURL: "https://c/v1"}, // shadows the preset
	}
	cands := r.completionsFor("/provider ", 10)
	got := make([]string, len(cands))
	for i, c := range cands {
		got[i] = c.text
	}
	want := []string{
		"backup", "minimax", "work", // profiles, sorted
		"openai", "anthropic", "google", "deepseek", "zai", "kimi",
		"xai", "openrouter", "dashscope", "dashscope-intl", "siliconflow", "ollama",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("provider completion order =\n%v\nwant\n%v", got, want)
	}
}
