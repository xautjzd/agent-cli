package repl

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xautjzd/agent-cli/internal/textwidth"
)

// settingKind decides how a row is edited.
type settingKind int

const (
	kindText   settingKind = iota // free text, edited inline on Enter
	kindInt                       // integer, edited inline on Enter
	kindSecret                    // like text but the value is masked
	kindEnum                      // fixed choices, cycled on Space
)

// setting describes one editable config row.
type setting struct {
	key     string
	label   string
	kind    settingKind
	choices []string // for kindEnum
}

// configSettings is the ordered list shown in the panel. Enum settings are
// toggled with Space; the rest open an inline editor on Enter.
var configSettings = []setting{
	{"provider", "Provider", kindText, nil},
	{"model", "Model", kindText, nil},
	{"api_key", "API key", kindSecret, nil},
	{"base_url", "Base URL", kindText, nil},
	{"thinking", "Extended thinking", kindEnum, []string{"adaptive", "off"}},
	{"permission_mode", "Permission mode", kindEnum, []string{"hitl", "bypass"}},
	{"bash_policy", "Bash risk posture", kindEnum, []string{"standard", "strict"}},
	{"sandbox", "Command sandbox", kindEnum, []string{"off", "auto", "on"}},
	{"auto_compact", "Auto context compaction", kindEnum, []string{"on", "off"}},
	{"context_limit", "Context window (tokens)", kindInt, nil},
	{"max_turns", "Max tool-loop turns", kindInt, nil},
	{"goal_max_rounds", "Goal check rounds", kindInt, nil},
	{"vision_provider", "Vision fallback provider", kindText, nil},
	{"vision_model", "Vision fallback model", kindText, nil},
}

// configModel is the bubbletea model for the interactive settings panel,
// modeled on Claude Code's /config: a searchable list where Space toggles a
// choice value and Enter edits a free-text one, staying open so several
// settings can be changed in a row. Every change is applied to the running
// session and persisted to the global config immediately.
type configModel struct {
	repl    *Repl
	ctx     context.Context
	search  textinput.Model
	visible []int // indexes into configSettings matching the search
	sel     int   // index into visible
	offset  int   // first visible row (scrolling window)

	editing bool            // an inline value editor is open
	editor  textinput.Model // the inline editor
	editIdx int             // configSettings index being edited
	status  string          // transient feedback (applied / error)
	width   int
	abort   bool
}

const configRows = 12 // visible list height

func newConfigModel(r *Repl, ctx context.Context) *configModel {
	s := textinput.New()
	s.Prompt = "  search: "
	s.Focus()
	m := &configModel{repl: r, ctx: ctx, search: s, width: r.terminalWidth()}
	m.refilter()
	return m
}

func (m *configModel) Init() tea.Cmd { return textinput.Blink }

func (m *configModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sz, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = sz.Width
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// While an inline editor is open, keys go to it.
	if m.editing {
		return m.updateEditor(key)
	}

	switch key.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.abort = true
		return m, tea.Quit
	case tea.KeyUp, tea.KeyCtrlP:
		if len(m.visible) > 0 {
			m.sel = (m.sel - 1 + len(m.visible)) % len(m.visible)
			m.scroll()
		}
		m.status = ""
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if len(m.visible) > 0 {
			m.sel = (m.sel + 1) % len(m.visible)
			m.scroll()
		}
		m.status = ""
		return m, nil
	case tea.KeySpace:
		m.toggle()
		return m, nil
	case tea.KeyEnter:
		m.activate()
		return m, nil
	}

	// Any other key edits the search filter.
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	m.refilter()
	return m, cmd
}

// updateEditor handles keys while the inline value editor is open.
func (m *configModel) updateEditor(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.editing = false
		return m, nil
	case tea.KeyEnter:
		m.commitEdit(m.editor.Value())
		m.editing = false
		return m, nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(key)
	return m, cmd
}

// toggle cycles an enum setting to its next choice; for a non-enum row it
// falls through to the inline editor so Space is never a dead key.
func (m *configModel) toggle() {
	s, ok := m.selected()
	if !ok {
		return
	}
	if s.kind != kindEnum || len(s.choices) == 0 {
		m.activate()
		return
	}
	cur := m.repl.currentValue(s.key)
	next := s.choices[0]
	for i, c := range s.choices {
		if c == cur {
			next = s.choices[(i+1)%len(s.choices)]
			break
		}
	}
	m.commitEdit(next)
}

// activate opens the inline editor for a free-text/int setting.
func (m *configModel) activate() {
	s, ok := m.selected()
	if !ok {
		return
	}
	if s.kind == kindEnum && len(s.choices) > 0 {
		m.toggle()
		return
	}
	ed := textinput.New()
	ed.Prompt = "  " + s.label + " = "
	ed.Focus()
	ed.Width = 48
	if s.kind == kindSecret {
		ed.EchoMode = textinput.EchoPassword
	} else if cur := m.repl.currentValue(s.key); !strings.HasPrefix(cur, "(") {
		ed.SetValue(cur) // pre-fill with the current value, minus placeholders
		ed.CursorEnd()
	}
	m.editor = ed
	m.editIdx = m.visible[m.sel]
	m.editing = true
}

// commitEdit applies and persists a new value for the selected setting.
func (m *configModel) commitEdit(value string) {
	idx := m.editIdx
	if !m.editing {
		idx = m.visible[m.sel]
	}
	s := configSettings[idx]
	value = strings.TrimSpace(value)
	if value == "" {
		m.status = "unchanged"
		return
	}
	// Applying some settings (provider switch) prints to the terminal;
	// silence that here so it can't corrupt the panel's own rendering. The
	// panel reports the outcome on its status line instead.
	saved := m.repl.Out
	m.repl.Out = io.Discard
	err := m.repl.setConfigValue(m.ctx, s.key, value)
	m.repl.Out = saved
	if err != nil {
		m.status = "⚠ " + err.Error()
		return
	}
	m.status = fmt.Sprintf("✓ %s = %s (saved)", s.label, m.repl.currentValue(s.key))
}

func (m *configModel) selected() (setting, bool) {
	if m.sel < 0 || m.sel >= len(m.visible) {
		return setting{}, false
	}
	return configSettings[m.visible[m.sel]], true
}

// refilter recomputes the visible rows from the search query.
func (m *configModel) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.search.Value()))
	m.visible = m.visible[:0]
	for i, s := range configSettings {
		if q == "" || strings.Contains(strings.ToLower(s.label+" "+s.key), q) {
			m.visible = append(m.visible, i)
		}
	}
	if m.sel >= len(m.visible) {
		m.sel = 0
	}
	m.scroll()
}

func (m *configModel) scroll() {
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+configRows {
		m.offset = m.sel - configRows + 1
	}
}

func (m *configModel) View() string {
	if m.abort {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.search.View() + "\n\n")

	labelW := 26
	end := m.offset + configRows
	if end > len(m.visible) {
		end = len(m.visible)
	}
	if m.offset > 0 {
		b.WriteString(styleHint.Render(fmt.Sprintf("  ↑ %d more", m.offset)) + "\n")
	}
	for i := m.offset; i < end; i++ {
		s := configSettings[m.visible[i]]
		name := textwidth.Pad(s.label, labelW)
		val := m.repl.currentValue(s.key)
		row := "  " + name + "  " + val
		if i == m.sel {
			row = styleSelected.Render("❯ "+name) + "  " + styleMarker.Render(val)
		}
		b.WriteString(row + "\n")
	}
	if end < len(m.visible) {
		b.WriteString(styleHint.Render(fmt.Sprintf("  ↓ %d more", len(m.visible)-end)) + "\n")
	}

	if m.editing {
		b.WriteString("\n" + m.editor.View() + "\n")
		b.WriteString(styleHint.Render("  enter save · esc cancel"))
	} else {
		b.WriteString("\n" + styleHint.Render("  ↑↓ move · space toggle · enter edit · type to search · esc done"))
	}
	if m.status != "" {
		b.WriteString("\n  " + m.status)
	}
	return b.String()
}

// runConfigPanel launches the interactive settings panel and blocks until
// the user exits it.
func (r *Repl) runConfigPanel(ctx context.Context) error {
	m := newConfigModel(r, ctx)
	p := tea.NewProgram(m, tea.WithInput(r.In), tea.WithOutput(r.Out))
	_, err := p.Run()
	return err
}
