package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestTUI(t *testing.T) *tuiModel {
	t.Helper()
	r, _, _ := newTestRepl(t, "")
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Focus()
	return &tuiModel{repl: r, sb: &scrollback{}, input: ti, ctx: context.Background()}
}

func TestScrollbackWritesAndNotifies(t *testing.T) {
	notified := 0
	sb := &scrollback{notify: func() { notified++ }}
	sb.Write([]byte("hello "))
	sb.Write([]byte("world"))
	if sb.String() != "hello world" {
		t.Errorf("scrollback content = %q", sb.String())
	}
	if notified != 2 {
		t.Errorf("notify called %d times, want 2", notified)
	}
}

func TestTUIRendersBoxAtBottom(t *testing.T) {
	m := newTestTUI(t)
	// Simulate the initial sizing.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := m.View()
	// The bordered input box must be present.
	if !strings.Contains(view, "╭") || !strings.Contains(view, "> ") {
		t.Errorf("expected an input box in the view:\n%s", view)
	}
	// The last non-empty content is the footer (box/hint), i.e. input is pinned
	// at the bottom.
	if !strings.Contains(view, "pgup/pgdn scroll") {
		t.Errorf("expected the footer hint at the bottom:\n%s", view)
	}
}

func TestTUIResizeIsClean(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// A resize must not panic and must re-fit the box to the new width.
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 18})
	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if w := lineWidth(line); w > 70 {
				t.Errorf("line exceeds new width 70 (=%d): %q", w, line)
		}
	}
}

func TestTUISubmitStartsTurn(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Type a line and submit.
	for _, r := range "hello" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy {
		t.Error("submitting should set busy")
	}
	if cmd == nil {
		t.Error("submitting should return a turn command")
	}
	// The submitted line is echoed into the scrollback with the ❯ marker.
	if !strings.Contains(m.sb.String(), "hello") {
		t.Errorf("submitted line not echoed: %q", m.sb.String())
	}
	// While busy, further typing is ignored (only Ctrl-C interrupts).
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if strings.Contains(m.input.Value(), "x") {
		t.Error("input should be ignored while busy")
	}
}

func TestTUIAskModalRoundTrip(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reply := make(chan tuiReply, 1)
	// Deliver an ask request (as requestInput would).
	m.Update(askMsg{prompt: "Allow? [y/N]", reply: reply})
	if m.ask == nil {
		t.Fatal("ask modal not opened")
	}
	// Type an answer and press Enter.
	for _, r := range "y" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-reply:
		if !got.ok || got.text != "y" {
			t.Errorf("reply = %+v, want {y true}", got)
		}
	default:
		t.Fatal("no reply delivered on Enter")
	}
	if m.ask != nil {
		t.Error("ask modal should close after answering")
	}
}

func TestTUIAskCancel(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reply := make(chan tuiReply, 1)
	m.Update(askMsg{prompt: "pick", reply: reply})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	got := <-reply
	if got.ok {
		t.Error("Ctrl-C should cancel the prompt (ok=false)")
	}
}

func TestTUISelectOverlayArrowPick(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reply := make(chan pickReply, 1)
	items := []pickerItem{
		{label: "alpha", filterText: "alpha"},
		{label: "beta", filterText: "beta"},
		{label: "gamma", filterText: "gamma"},
	}
	m.Update(selectMsg{title: "Pick one", items: items, reply: reply})
	if m.pick == nil {
		t.Fatal("selection overlay not opened")
	}
	// The overlay renders in the footer with the ❯ marker on the first item.
	if v := m.View(); !strings.Contains(v, "Pick one") || !strings.Contains(v, "alpha") {
		t.Errorf("overlay not rendered:\n%s", v)
	}
	// Arrow down twice, then Enter selects the third item (gamma).
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := <-reply
	if !got.ok || got.idx != 2 {
		t.Errorf("pick = %+v, want idx 2 ok true", got)
	}
	if m.pick != nil {
		t.Error("overlay should close after Enter")
	}
}

// A selection overlay with a preview callback fires it for the highlighted
// item on open and on every move — the mechanism behind /theme's live preview.
func TestTUISelectPreviewFires(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	var previewed []int
	reply := make(chan pickReply, 1)
	items := []pickerItem{
		{label: "a", filterText: "a"},
		{label: "b", filterText: "b"},
		{label: "c", filterText: "c"},
	}
	m.Update(selectMsg{title: "Pick", items: items, reply: reply,
		preview: func(i int) { previewed = append(previewed, i) }})

	// Preview fired once for the initially highlighted item (index 0).
	if len(previewed) != 1 || previewed[0] != 0 {
		t.Fatalf("open preview = %v, want [0]", previewed)
	}
	// Down → preview 1, Down → preview 2.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := previewed[len(previewed)-1]; got != 2 {
		t.Errorf("last preview = %d, want 2 (from %v)", got, previewed)
	}
	// Enter delivers the previewed index.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; !got.ok || got.idx != 2 {
		t.Errorf("pick = %+v, want idx 2", got)
	}
}

func TestTUIEnterRunsSelectedCommand(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Type "/mo" so the menu highlights "/model", then Enter runs it directly
	// (accept + submit) — no second Enter needed.
	for _, r := range "/mo" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.cands) == 0 || m.cands[m.sel].text != "/model" {
		t.Fatalf("menu not highlighting /model: %+v", m.cands)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy || cmd == nil {
		t.Error("Enter on a selected command should submit immediately")
	}
	if !strings.Contains(m.sb.String(), "/model") {
		t.Errorf("command not echoed/submitted: %q", m.sb.String())
	}
}

func TestTUIEnterOnFileCompletionFillsOnly(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// Create a file so @-completion has a candidate.
	if err := os.WriteFile(filepath.Join(m.repl.WorkDir, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.repl.fileCache = nil
	for _, r := range "see @not" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.cands) == 0 || !strings.HasPrefix(m.cands[m.sel].text, "@") {
		t.Fatalf("expected a @file candidate: %+v", m.cands)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// A file completion only fills in — it must NOT submit.
	if m.busy {
		t.Error("Enter on a @file completion should not submit")
	}
	if !strings.Contains(m.input.Value(), "@notes.md") {
		t.Errorf("file not accepted into input: %q", m.input.Value())
	}
}

func TestTUICtrlNPNavigation(t *testing.T) {
	// Ctrl-N/Ctrl-P move the selection overlay just like Down/Up.
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reply := make(chan pickReply, 1)
	items := []pickerItem{{label: "a", filterText: "a"}, {label: "b", filterText: "b"}, {label: "c", filterText: "c"}}
	m.Update(selectMsg{title: "t", items: items, reply: reply})

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN}) // → b
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN}) // → c
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP}) // → b
	if m.pick.sel != 1 {
		t.Errorf("Ctrl-N/P selection = %d, want 1 (b)", m.pick.sel)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; got.idx != 1 {
		t.Errorf("chosen idx = %d, want 1", got.idx)
	}

	// Ctrl-P/Ctrl-N also step through input history at the idle prompt.
	m.repl.history = []string{"first", "second"}
	m.repl.histIdx = len(m.repl.history)
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.input.Value() != "second" {
		t.Errorf("Ctrl-P history = %q, want second", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.input.Value() != "first" {
		t.Errorf("Ctrl-P again = %q, want first", m.input.Value())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.input.Value() != "second" {
		t.Errorf("Ctrl-N history = %q, want second", m.input.Value())
	}
}

func TestTUISelectFilterAndCancel(t *testing.T) {
	m := newTestTUI(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	reply := make(chan pickReply, 1)
	items := []pickerItem{
		{label: "apple", filterText: "apple"},
		{label: "banana", filterText: "banana"},
		{label: "cherry", filterText: "cherry"},
	}
	m.Update(selectMsg{title: "Fruit", items: items, reply: reply})
	// Type "ban" → only banana matches; Enter picks it (original index 1).
	for _, r := range "ban" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(m.pick.filtered) != 1 || m.pick.filtered[0] != 1 {
		t.Fatalf("filter wrong: %+v", m.pick.filtered)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; !got.ok || got.idx != 1 {
		t.Errorf("filtered pick = %+v, want idx 1", got)
	}

	// Esc cancels without a selection.
	reply2 := make(chan pickReply, 1)
	m.Update(selectMsg{title: "x", items: items, reply: reply2})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := <-reply2; got.ok {
		t.Error("Esc should cancel the overlay")
	}
}

// lineWidth measures a rendered line's display width, ignoring ANSI escapes.
func lineWidth(s string) int {
	// Strip common CSI sequences.
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			// skip until a letter terminator
			for i < len(s) && !((s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z')) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return len([]rune(b.String()))
}
