package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/textwidth"
)

// TestProviderListingDedupsProfiles reproduces the reported duplicate-entry
// bug: config profiles named like presets ("glm", "deepseek") must appear
// once, in the custom section, and be hidden from the built-in list.
func TestProviderListingDedupsProfiles(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	r.Cfg.Providers = map[string]config.ProviderConfig{
		"glm":      {Format: "anthropic", BaseURL: "https://gw/anthropic", Model: "glm-5.2"},
		"deepseek": {Format: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro"},
	}
	if err := r.listProviders(); err != nil {
		t.Fatal(err)
	}
	got := out.String()

	your, builtin, found := strings.Cut(got, "Built-in providers")
	if !found || !strings.Contains(your, "Your providers") {
		t.Fatalf("missing custom section:\n%s", got)
	}
	if !strings.Contains(your, "glm") || !strings.Contains(your, "overrides built-in") {
		t.Errorf("profile not labeled as an override:\n%s", your)
	}
	for _, line := range strings.Split(builtin, "\n") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		// "glm" is an alias of the zai preset, so the profile hides that
		// preset too — a vendor must never be listed under two names.
		if name == "glm" || name == "zai" || name == "deepseek" {
			t.Errorf("overridden preset still shown as built-in: %q", line)
		}
		// A wire is not a provider: no vendor gets a second "-anthropic" row.
		if strings.HasSuffix(name, "-anthropic") {
			t.Errorf("anthropic wire listed as a separate provider: %q", line)
		}
	}
	// A vendor's Anthropic wire is advertised on the vendor's own row.
	if !strings.Contains(builtin, "kimi") || !strings.Contains(builtin, "--anthropic available") {
		t.Errorf("anthropic wire not advertised on the vendor row:\n%s", builtin)
	}
}

func TestProviderCompletionDedups(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	r.Cfg.Providers = map[string]config.ProviderConfig{
		"glm": {Format: "anthropic", BaseURL: "https://gw", Model: "glm-5.2"},
	}
	cands := r.completionsFor("/provider glm", 13)
	n := 0
	for _, c := range cands {
		if c.text == "glm" {
			n++
			if !strings.Contains(c.desc, "your config") {
				t.Errorf("glm suggestion should be the profile: %q", c.desc)
			}
		}
	}
	if n != 1 {
		t.Errorf("glm appears %d times in completion, want 1: %+v", n, cands)
	}
}

// withSessions attaches a session store to a test repl.
func withSessions(t *testing.T, r *Repl) *session.FileStore {
	t.Helper()
	store := &session.FileStore{Dir: filepath.Join(t.TempDir(), "sessions")}
	r.Sessions = store
	return store
}

func TestTurnsAreRecorded(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	store := withSessions(t, r)

	if err := r.runPrompt(context.Background(), "first task here"); err != nil {
		t.Fatal(err)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 session, got %d", len(metas))
	}
	if metas[0].Title != "first task here" || metas[0].MessageCount != 2 {
		t.Errorf("meta = %+v", metas[0])
	}

	// A second turn updates the same session rather than creating another.
	if err := r.runPrompt(context.Background(), "follow-up"); err != nil {
		t.Fatal(err)
	}
	metas, _ = store.List()
	if len(metas) != 1 || metas[0].MessageCount != 4 {
		t.Errorf("after second turn: %+v", metas)
	}
}

func TestClearDetachesSession(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	store := withSessions(t, r)

	r.runPrompt(context.Background(), "task one")
	r.dispatch(context.Background(), "/clear")
	r.runPrompt(context.Background(), "task two")

	metas, _ := store.List()
	if len(metas) != 2 {
		t.Fatalf("expected 2 sessions after /clear, got %+v", metas)
	}
}

func TestResumeRestoresHistory(t *testing.T) {
	r, stub, out := newTestRepl(t, "")
	store := withSessions(t, r)

	// Record a session, then clear the conversation.
	r.runPrompt(context.Background(), "remember the magic word is xyzzy")
	recorded := r.current.ID
	r.dispatch(context.Background(), "/clear")
	if len(r.Agent.History()) != 1 {
		t.Fatal("clear failed")
	}

	// Resume by ID: history is back and the next request carries it.
	if err := r.dispatch(context.Background(), "/resume "+recorded); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Agent.History()); got != 3 { // system + user + assistant
		t.Fatalf("history after resume = %d", got)
	}
	if !strings.Contains(out.String(), "Resumed session "+recorded) {
		t.Errorf("missing resume confirmation: %s", out.String())
	}

	r.runPrompt(context.Background(), "what was the magic word?")
	found := false
	for _, m := range stub.last.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "xyzzy") {
			found = true
		}
	}
	if !found {
		t.Error("resumed history not sent to provider")
	}

	// Continuing a resumed session updates it in place.
	metas, _ := store.List()
	// 2 recorded + user/assistant from the follow-up turn = 4.
	if len(metas) != 1 || metas[0].ID != recorded || metas[0].MessageCount != 4 {
		t.Errorf("resumed session not updated in place: %+v", metas)
	}
}

func TestResumePickerFlow(t *testing.T) {
	// /resume with no args lists sessions and reads a numbered choice.
	r, _, out := newTestRepl(t, "1\n")
	withSessions(t, r)

	r.runPrompt(context.Background(), "earlier session")
	r.dispatch(context.Background(), "/clear")

	if err := r.dispatch(context.Background(), "/resume"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "earlier session") || !strings.Contains(out.String(), "Resumed session") {
		t.Errorf("picker flow output:\n%s", out.String())
	}
}

// captureEvents records the rendering calls made during replay.
type captureEvents struct {
	calls []string
}

func (c *captureEvents) OnUserPrompt(t string)    { c.calls = append(c.calls, "user:"+t) }
func (c *captureEvents) OnThinking(t string)      { c.calls = append(c.calls, "think:"+t) }
func (c *captureEvents) OnAssistantText(t string) { c.calls = append(c.calls, "text:"+t) }
func (c *captureEvents) OnToolCall(n, a string)   { c.calls = append(c.calls, "call:"+n+":"+a) }
func (c *captureEvents) OnToolResult(n, _ string, ok bool) {
	c.calls = append(c.calls, fmt.Sprintf("result:%s:%v", n, ok))
}
func (c *captureEvents) OnTurnStats(agent.TurnStats) {}

func TestReplayUsesLiveRenderingPipeline(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	withSessions(t, r)

	// Record a turn whose wire content differs from what the user typed:
	// the @ref is expanded on the wire but the replayed prompt must show
	// the original input.
	os.WriteFile(filepath.Join(r.WorkDir, "ref.txt"), []byte("expanded-secret"), 0o644)
	if err := r.runPrompt(context.Background(), "summarize @ref.txt"); err != nil {
		t.Fatal(err)
	}
	recorded := r.current.ID
	r.dispatch(context.Background(), "/new")

	ev := &captureEvents{}
	r.Agent.Events = ev
	if err := r.dispatch(context.Background(), "/resume "+recorded); err != nil {
		t.Fatal(err)
	}
	if len(ev.calls) != 2 {
		t.Fatalf("replay calls = %v", ev.calls)
	}
	if ev.calls[0] != "user:summarize @ref.txt" {
		t.Errorf("replayed prompt must be the raw input, got %q", ev.calls[0])
	}
	if ev.calls[1] != "text:ok" {
		t.Errorf("assistant replay = %q", ev.calls[1])
	}
}

func TestReplayToolCallsWithStatus(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	store := withSessions(t, r)

	sess := &session.Session{
		Meta: session.Meta{ID: "20260720-120000-cccc", Title: "tools"},
		Messages: []session.Record{
			{Message: provider.Message{Role: provider.RoleUser, Content: "run it"}},
			{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "c1", Type: "function", Function: provider.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
				{ID: "c2", Type: "function", Function: provider.FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}},
			}}},
			{Message: provider.Message{Role: provider.RoleTool, ToolCallID: "c1", Content: "file.txt"}},
			{Message: provider.Message{Role: provider.RoleTool, ToolCallID: "c2", Content: "Error: no such file"}},
			{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
		},
	}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	ev := &captureEvents{}
	r.Agent.Events = ev
	if err := r.dispatch(context.Background(), "/resume "+sess.ID); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"user:run it",
		`call:bash:{"command":"ls"}`,
		"result:bash:true",
		`call:read_file:{"path":"x"}`,
		"result:read_file:false",
		"text:done",
	}
	if len(ev.calls) != len(want) {
		t.Fatalf("replay calls = %v", ev.calls)
	}
	for i := range want {
		if ev.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, ev.calls[i], want[i])
		}
	}
}

func TestSessionPickerModel(t *testing.T) {
	items := []pickerItem{
		{label: "alpha session", filterText: "alpha session id-aaa"},
		{label: "beta session", filterText: "beta session id-bbb"},
		{label: "gamma task", filterText: "gamma task id-ccc"},
	}
	// Typing filters; Enter selects the highlighted match.
	m := newPickerModel("Resume:", items)
	for _, r := range "session" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.visible) != 2 {
		t.Fatalf("filter 'session' visible = %v", m.visible)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done || m.choice != 1 {
		t.Errorf("expected beta (index 1) selected, got done=%v choice=%d", m.done, m.choice)
	}

	// Esc cancels.
	m = newPickerModel("Resume:", items)
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.cancel {
		t.Error("Esc should cancel")
	}

	// No matches: Enter is a no-op, not a selection.
	m = newPickerModel("Resume:", items)
	for _, r := range "zzz" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.done {
		t.Error("Enter with no matches must not select")
	}
}

func TestSessionLabelsAlignMixedScripts(t *testing.T) {
	// The rows that mis-aligned in practice: CJK titles, ASCII titles, and
	// an over-long URL title, with model names of differing lengths.
	now := time.Now()
	metas := []session.Meta{
		{Title: "你是什么模型", Model: "glm-5.2", MessageCount: 6, UpdatedAt: now.Add(-25 * time.Minute)},
		{Title: "hello", Model: "glm-5.2", MessageCount: 9, UpdatedAt: now.Add(-38 * time.Minute)},
		{Title: "https://ws-mkh2yc3hngbq7zqg.ap-southeast-1.maas.aliyuncs.com/apps/anthropic",
			Model: "deepseek-v4-flash", MessageCount: 2, UpdatedAt: now.Add(-time.Hour)},
		{Title: "我要支持输入token与输出token展示以及耗时展示",
			Model: "deepseek-v4-flash", MessageCount: 23, UpdatedAt: now.Add(-9 * time.Hour)},
		{Title: "删除 Readme", Model: "deepseek-v4-pro", MessageCount: 8, UpdatedAt: now.Add(-8 * time.Hour)},
	}

	const avail = 96
	labels := sessionLabels(metas, avail)
	if len(labels) != len(metas) {
		t.Fatalf("labels = %d, want %d", len(labels), len(metas))
	}

	// No row may overflow the terminal.
	for i, l := range labels {
		if w := textwidth.Width(l); w > avail {
			t.Errorf("row %d overflows: width %d > %d", i, w, avail)
		}
	}

	// Both fixed columns must begin at the same offset on every row —
	// that is what "aligned" means here.
	for _, col := range []struct {
		name string
		of   func(i int) string
	}{
		{"model", func(i int) string { return metas[i].Model }},
		{"title", func(i int) string { return titlePrefix(metas[i].Title) }},
	} {
		offset := -1
		for i, l := range labels {
			idx := strings.Index(l, col.of(i))
			if idx < 0 {
				t.Fatalf("row %d lost its %s: %q", i, col.name, l)
			}
			at := textwidth.Width(l[:idx])
			if offset == -1 {
				offset = at
			} else if at != offset {
				t.Errorf("row %d %s column at %d, want %d\n%s",
					i, col.name, at, offset, strings.Join(labels, "\n"))
			}
		}
	}

	// The title is last, so nothing follows it that could drift.
	for i, l := range labels {
		if !strings.HasSuffix(l, textwidth.Truncate(metas[i].Title, 200)) &&
			!strings.HasSuffix(l, "…") {
			t.Errorf("row %d should end with the title: %q", i, l)
		}
	}

	// Message counts are gone — they carried no useful signal.
	for i, l := range labels {
		if strings.Contains(l, "msgs") {
			t.Errorf("row %d still shows a message count: %q", i, l)
		}
	}

	// Long titles are elided rather than pushing the layout out.
	if !strings.Contains(labels[2], "…") {
		t.Errorf("over-long title was not truncated: %q", labels[2])
	}
	// Short titles survive intact.
	if !strings.HasSuffix(labels[1], "hello") {
		t.Errorf("short title lost: %q", labels[1])
	}
}

// titlePrefix returns a short leading slice of a title — short enough to
// survive truncation — so a test can locate where the title column starts.
func titlePrefix(s string) string {
	r := []rune(s)
	if len(r) > 4 {
		r = r[:4]
	}
	return string(r)
}

func TestSessionLabelsNarrowTerminal(t *testing.T) {
	// A cramped window must still produce usable, non-overflowing rows.
	metas := []session.Meta{
		{Title: "一个比较长的中文标题内容", Model: "glm-4.6", MessageCount: 4, UpdatedAt: time.Now()},
		{Title: "hi", Model: "glm-4.6", MessageCount: 1, UpdatedAt: time.Now()},
	}
	labels := sessionLabels(metas, 20) // below the floor; clamped internally
	for i, l := range labels {
		if strings.ContainsRune(l, '\uFFFD') {
			t.Errorf("row %d contains a replacement character: %q", i, l)
		}
		// The fixed columns still align even when space is tight.
		idx := strings.Index(l, metas[i].Model)
		if idx < 0 {
			t.Fatalf("row %d lost its model: %q", i, l)
		}
		if i > 0 {
			prev := strings.Index(labels[i-1], metas[i-1].Model)
			if textwidth.Width(l[:idx]) != textwidth.Width(labels[i-1][:prev]) {
				t.Errorf("model column drifts in a narrow terminal:\n%s", strings.Join(labels, "\n"))
			}
		}
	}
}

func TestTitleFromIsWidthSafe(t *testing.T) {
	// Titles are stored truncated; byte slicing used to split a rune here
	// and surface as "图里说的是什�…" in the picker.
	long := strings.Repeat("图里说的是什么", 20)
	title := session.TitleFrom(long)
	if strings.ContainsRune(title, '�') {
		t.Errorf("title contains a replacement character: %q", title)
	}
	if textwidth.Width(title) > 60 {
		t.Errorf("title width = %d, want <= 60", textwidth.Width(title))
	}
	if !strings.HasSuffix(title, "…") {
		t.Errorf("truncated title should be marked: %q", title)
	}
}

func TestRenameCurrentSession(t *testing.T) {
	r, _, out := newTestRepl(t, "")
	store := withSessions(t, r)

	// A recorded session starts with an auto-derived title.
	r.runPrompt(context.Background(), "some long first message that becomes the title")
	id := r.current.ID
	if metas, _ := store.List(); metas[0].Title == "renamed" {
		t.Fatal("precondition: title should not already be the new one")
	}

	if err := r.dispatch(context.Background(), "/rename renamed"); err != nil {
		t.Fatal(err)
	}
	// The new title is persisted, not just held in memory.
	sess, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Title != "renamed" {
		t.Errorf("persisted title = %q, want %q", sess.Title, "renamed")
	}
	if !strings.Contains(out.String(), "Renamed session") {
		t.Errorf("missing confirmation: %s", out.String())
	}

	// A later turn must not clobber the chosen name with a derived one.
	r.runPrompt(context.Background(), "a subsequent message")
	sess, _ = store.Load(id)
	if sess.Title != "renamed" {
		t.Errorf("title overwritten by a later turn: %q", sess.Title)
	}
}

func TestRenameBeforeSessionExists(t *testing.T) {
	// Naming a session up front is the common case; it must survive until
	// the session file is actually created.
	r, _, out := newTestRepl(t, "")
	store := withSessions(t, r)

	if err := r.dispatch(context.Background(), "/rename planned work"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "once it starts") {
		t.Errorf("should explain the deferral: %s", out.String())
	}
	if metas, _ := store.List(); len(metas) != 0 {
		t.Errorf("no session file should exist yet: %+v", metas)
	}

	r.runPrompt(context.Background(), "this would normally become the title")
	metas, _ := store.List()
	if len(metas) != 1 || metas[0].Title != "planned work" {
		t.Fatalf("pending title not applied: %+v", metas)
	}

	// Starting a new session drops the pending name rather than reusing it.
	r.dispatch(context.Background(), "/new")
	r.runPrompt(context.Background(), "fresh topic here")
	metas, _ = store.List() // newest first
	if len(metas) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(metas))
	}
	if metas[0].Title == "planned work" {
		t.Error("pending title leaked into the new session")
	}
	if metas[1].Title != "planned work" {
		t.Errorf("original session lost its name: %q", metas[1].Title)
	}
}

func TestRenameInteractiveAndValidation(t *testing.T) {
	// With no argument the current title is shown and a new one prompted.
	r, _, out := newTestRepl(t, "interactive name\n")
	withSessions(t, r)
	r.runPrompt(context.Background(), "first")

	if err := r.dispatch(context.Background(), "/rename"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Current title:") {
		t.Errorf("current title not shown: %s", out.String())
	}
	if r.current.Title != "interactive name" {
		t.Errorf("interactive rename failed: %q", r.current.Title)
	}

	// An empty response cancels without changing anything.
	r2, _, out2 := newTestRepl(t, "\n")
	withSessions(t, r2)
	r2.runPrompt(context.Background(), "keep me")
	before := r2.current.Title
	if err := r2.dispatch(context.Background(), "/rename"); err != nil {
		t.Fatal(err)
	}
	if r2.current.Title != before {
		t.Errorf("empty input should cancel, title became %q", r2.current.Title)
	}
	if !strings.Contains(out2.String(), "Cancelled") {
		t.Errorf("cancellation not reported: %s", out2.String())
	}
}

func TestCleanTitleNormalizes(t *testing.T) {
	if got := CleanTitle("  spaced out  "); got != "spaced out" {
		t.Errorf("CleanTitle trim = %q", got)
	}
	// Only the first line is kept, so a pasted block cannot break the list.
	if got := CleanTitle("first line\nsecond line"); got != "first line" {
		t.Errorf("CleanTitle multiline = %q", got)
	}
	// Over-long titles are elided on a rune boundary, never mid-character.
	long := CleanTitle(strings.Repeat("重命名标题", 40))
	if textwidth.Width(long) > 60 {
		t.Errorf("width = %d, want <= 60", textwidth.Width(long))
	}
	if strings.ContainsRune(long, '�') {
		t.Errorf("truncation produced a replacement character: %q", long)
	}
	if CleanTitle("   ") != "" {
		t.Error("blank input should normalize to empty")
	}
}

func TestResumeUnknownID(t *testing.T) {
	r, _, _ := newTestRepl(t, "")
	withSessions(t, r)
	if err := r.dispatch(context.Background(), "/resume nope"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}
