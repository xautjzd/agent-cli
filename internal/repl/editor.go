package repl

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xautjzd/agent-cli/internal/textwidth"
)

// Popup styling. Kept subtle so the editor reads like a shell, not a form.
var (
	styleSelected = lipgloss.NewStyle().Reverse(true)
	styleDesc     = lipgloss.NewStyle().Faint(true)
	styleHint     = lipgloss.NewStyle().Faint(true)
	// styleInputBox frames the pending input, clearly separating what is
	// being typed from the scrollback above (Claude Code-style).
	styleInputBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1)
	// styleMarker renders the collapsed "❯" marker a submitted line keeps
	// in scrollback.
	styleMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

// editorModel is the bubbletea model for one input line: a text field plus
// a live completion popup for "/" commands, skills, and "@" file paths.
type editorModel struct {
	repl  *Repl
	input textinput.Model
	cands []candidate
	sel   int
	// offset is the first visible popup row; the window follows the
	// selection so long candidate lists scroll instead of truncating.
	offset int
	// width is the terminal width from the latest WindowSizeMsg, used to
	// stretch the input box across the screen.
	width int
	// status is a transient notice (e.g. paste feedback) under the box.
	status string
	result string
	done   bool // submitted a line
	abort  bool // Ctrl-C / Ctrl-D: caller should exit the session
}

func newEditorModel(r *Repl, prompt string) *editorModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Focus()
	m := &editorModel{repl: r, input: ti, sel: -1}
	// Seed a width immediately so the very first frame is sized correctly,
	// before any WindowSizeMsg arrives.
	m.width = r.terminalWidth()
	m.resize()
	return m
}

// boxMargin leaves one column between the input box and the terminal edge
// so a wide-character boundary can never push the border into a wrap.
const boxMargin = 1

// resize recomputes the text-input viewport width to fit inside the box.
//
// Key flow: the box's inner text area is the terminal width minus the
// border, padding and margin. The prompt ("> " or "plan> ") and the cursor
// cell both consume part of that area, and a wide (CJK) character at the
// scroll boundary can occupy one extra column — so the viewport is sized
// with room for all three. Without this, prompt + text + cursor could equal
// the inner width exactly, and one more column would make lipgloss wrap the
// whole line, inflating the box to several rows.
func (m *editorModel) resize() {
	if m.width <= 0 {
		m.width = 80
	}
	inner := m.boxInnerWidth()
	promptW := textwidth.Width(m.input.Prompt)
	w := inner - promptW - 2 // 1 for the cursor cell, 1 for wide-char slack
	if w < 8 {
		w = 8
	}
	m.input.Width = w
}

// boxInnerWidth is the number of columns available for text inside the
// bordered, padded box.
func (m *editorModel) boxInnerWidth() int {
	// box total = boxContentWidth() + 2 (border); content includes 2 cols
	// of horizontal padding, leaving content-2 for text.
	return m.boxContentWidth() - 2
}

// boxContentWidth is the lipgloss content width for the box, chosen so the
// rendered box (content + border) stays boxMargin columns inside the
// terminal.
func (m *editorModel) boxContentWidth() int {
	w := m.width - 2 - boxMargin
	if w < 12 {
		w = 12
	}
	return w
}

func (m *editorModel) Init() tea.Cmd { return textinput.Blink }

// Update implements the editor's key policy.
//
// Key flow: Tab always accepts the highlighted candidate. Enter accepts it
// only when doing so would change the input (menu selection); otherwise it
// submits the line — so a fully typed "/help" submits on the first Enter.
// Up/Down navigate the popup when it is open and input history when it is
// closed. Every other key goes to the text field, after which candidates
// are recomputed from the token under the cursor.
func (m *editorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = size.Width
		m.resize()
		return m, nil
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	m.status = "" // any keypress clears the previous notice

	switch key.Type {
	case tea.KeyCtrlV:
		m.pasteImage()
		return m, nil

	case tea.KeyCtrlC, tea.KeyCtrlD:
		m.abort = true
		return m, tea.Quit

	case tea.KeyEnter:
		if c, ok := m.selected(); ok && m.wouldChange(c) {
			m.accept(c)
			return m, nil
		}
		m.result = m.input.Value()
		m.done = true
		return m, tea.Quit

	case tea.KeyTab:
		if c, ok := m.selected(); ok {
			m.accept(c)
		}
		return m, nil

	case tea.KeyEsc:
		m.cands = nil
		return m, nil

	case tea.KeyUp:
		if len(m.cands) > 0 {
			m.sel = (m.sel - 1 + len(m.cands)) % len(m.cands)
			m.scrollToSelection()
		} else {
			m.input.SetValue(m.repl.historyPrev(m.input.Value()))
			m.input.CursorEnd()
		}
		return m, nil

	case tea.KeyDown:
		if len(m.cands) > 0 {
			m.sel = (m.sel + 1) % len(m.cands)
			m.scrollToSelection()
		} else {
			m.input.SetValue(m.repl.historyNext())
			m.input.CursorEnd()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.refreshCandidates()
	return m, cmd
}

// refreshCandidates recomputes the popup for the token under the cursor,
// preserving the selection when the same first candidate is still offered.
func (m *editorModel) refreshCandidates() {
	prevFirst := ""
	if len(m.cands) > 0 {
		prevFirst = m.cands[0].text
	}
	m.cands = m.repl.completionsFor(m.input.Value(), m.input.Position())
	if len(m.cands) == 0 {
		m.sel = -1
		m.offset = 0
		return
	}
	if m.sel < 0 || m.sel >= len(m.cands) || prevFirst != m.cands[0].text {
		m.sel = 0
		m.offset = 0
	}
	m.scrollToSelection()
}

// scrollToSelection keeps the highlighted candidate inside the visible
// window.
func (m *editorModel) scrollToSelection() {
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+popupRows {
		m.offset = m.sel - popupRows + 1
	}
}

func (m *editorModel) selected() (candidate, bool) {
	if m.sel >= 0 && m.sel < len(m.cands) {
		return m.cands[m.sel], true
	}
	return candidate{}, false
}

// wouldChange reports whether accepting c would alter the current token —
// the test that lets Enter double as "select from menu" and "submit".
func (m *editorModel) wouldChange(c candidate) bool {
	start, end := tokenBounds(m.input.Value(), m.input.Position())
	return m.input.Value()[start:end] != c.text
}

// pasteImage grabs an image from the system clipboard, stores it out of
// sight in the user's agent home, and inserts a clean "[Image #N]"
// placeholder at the cursor. The next submit resolves the placeholder to a
// multimodal part — the user never sees or types a file path.
func (m *editorModel) pasteImage() {
	data, err := readClipboardImage()
	if err != nil {
		m.status = "⚠ " + err.Error()
		return
	}
	path, err := savePastedImage(data)
	if err != nil {
		m.status = "⚠ save paste: " + err.Error()
		return
	}
	n := m.repl.addPastedImage(path)
	token := fmt.Sprintf("[Image #%d] ", n)
	v, pos := m.input.Value(), m.input.Position()
	m.input.SetValue(v[:pos] + token + v[pos:])
	m.input.SetCursor(pos + len(token))
	m.status = fmt.Sprintf("📎 image #%d attached", n)
}

func (m *editorModel) accept(c candidate) {
	value, pos := acceptCandidate(m.input.Value(), m.input.Position(), c)
	m.input.SetValue(value)
	m.input.SetCursor(pos)
	m.refreshCandidates()
}

// View renders the pending input inside a rounded frame so it is clearly
// separated from the scrollback above. Once submitted, the frame collapses
// to a compact "❯ input" line so history stays dense.
func (m *editorModel) View() string {
	if m.done || m.abort {
		label := m.input.Value()
		if m.input.Prompt != "> " {
			// Keep non-default prompts (e.g. "plan> ") visible in history.
			label = m.input.Prompt + label
		}
		return styleMarker.Render("❯") + " " + label + "\n"
	}

	// MaxHeight(1) is belt-and-suspenders: even if some future content
	// slips past the width budget, the box stays one text row tall instead
	// of ballooning vertically.
	box := styleInputBox.Width(m.boxContentWidth()).MaxHeight(3)
	view := box.Render(m.input.View())
	if m.status != "" {
		view += "\n" + styleHint.Render("  "+m.status)
	}
	if len(m.cands) == 0 {
		return view
	}
	view += "\n"
	end := m.offset + popupRows
	if end > len(m.cands) {
		end = len(m.cands)
	}
	if m.offset > 0 {
		view += styleHint.Render(fmt.Sprintf("  ↑ %d more", m.offset)) + "\n"
	}
	for i := m.offset; i < end; i++ {
		c := m.cands[i]
		// Pad by display width so CJK file names keep the description
		// column aligned.
		padded := " " + textwidth.Pad(c.text, 32)
		line := padded + " " + styleDesc.Render(c.desc)
		if i == m.sel {
			line = styleSelected.Render(padded) + " " + styleDesc.Render(c.desc)
		}
		view += line + "\n"
	}
	if end < len(m.cands) {
		view += styleHint.Render(fmt.Sprintf("  ↓ %d more", len(m.cands)-end)) + "\n"
	}
	view += styleHint.Render("  ↑↓ navigate · tab/enter accept · esc dismiss")
	return view
}

// editLine runs the bubbletea editor for one line. It returns ok=false when
// the user aborted (Ctrl-C/Ctrl-D), which ends the session.
func (r *Repl) editLine(prompt string) (string, bool, error) {
	r.fileCache = nil // rescan files once per prompt, not per keystroke
	m := newEditorModel(r, prompt)
	p := tea.NewProgram(m, tea.WithInput(r.In), tea.WithOutput(r.Out))
	res, err := p.Run()
	if err != nil {
		return "", false, err
	}
	final := res.(*editorModel)
	if final.abort {
		return "", false, nil
	}
	r.historyAdd(final.result)
	return final.result, true, nil
}

// --- input history ----------------------------------------------------------

// historyAdd records a submitted line for Up/Down recall.
func (r *Repl) historyAdd(line string) {
	if line == "" || (len(r.history) > 0 && r.history[len(r.history)-1] == line) {
		r.histIdx = len(r.history)
		return
	}
	r.history = append(r.history, line)
	r.histIdx = len(r.history)
}

// historyPrev steps back through history; current preserves the in-progress
// line the first time Up is pressed.
func (r *Repl) historyPrev(current string) string {
	if len(r.history) == 0 {
		return current
	}
	if r.histIdx == len(r.history) {
		r.histDraft = current
	}
	if r.histIdx > 0 {
		r.histIdx--
	}
	return r.history[r.histIdx]
}

// historyNext steps forward, restoring the draft line at the end.
func (r *Repl) historyNext() string {
	if r.histIdx < len(r.history) {
		r.histIdx++
	}
	if r.histIdx == len(r.history) {
		return r.histDraft
	}
	return r.history[r.histIdx]
}
