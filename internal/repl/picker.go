package repl

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// pickerItem is one selectable row in the interactive picker.
type pickerItem struct {
	// label is the rendered row.
	label string
	// filterText is what the search input matches against (typically the
	// label plus hidden identifiers).
	filterText string
}

// pickerModel is a bubbletea list with type-to-search filtering — the
// Claude Code /resume interaction: ↑↓ to move, typing narrows, Enter picks,
// Esc cancels.
type pickerModel struct {
	header  string
	items   []pickerItem
	filter  textinput.Model
	visible []int // indexes into items matching the current filter
	sel     int   // index into visible
	choice  int   // final selection, index into items
	done    bool
	cancel  bool
}

func newPickerModel(header string, items []pickerItem) *pickerModel {
	ti := textinput.New()
	ti.Prompt = "  search: "
	ti.Focus()
	m := &pickerModel{header: header, items: items, filter: ti, choice: -1}
	m.refilter()
	return m
}

func (m *pickerModel) Init() tea.Cmd { return textinput.Blink }

func (m *pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		return m, cmd
	}

	switch key.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.cancel = true
		return m, tea.Quit
	case tea.KeyEnter:
		if len(m.visible) > 0 {
			m.choice = m.visible[m.sel]
			m.done = true
			return m, tea.Quit
		}
		return m, nil
	case tea.KeyUp, tea.KeyCtrlP:
		if len(m.visible) > 0 {
			m.sel = (m.sel - 1 + len(m.visible)) % len(m.visible)
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if len(m.visible) > 0 {
			m.sel = (m.sel + 1) % len(m.visible)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.refilter()
	return m, cmd
}

// refilter recomputes visible rows with a case-insensitive substring match
// and clamps the selection.
func (m *pickerModel) refilter() {
	query := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.visible = m.visible[:0]
	for i, it := range m.items {
		if query == "" || strings.Contains(strings.ToLower(it.filterText), query) {
			m.visible = append(m.visible, i)
		}
	}
	if m.sel >= len(m.visible) {
		m.sel = 0
	}
}

func (m *pickerModel) View() string {
	if m.done || m.cancel {
		return "" // leave no picker residue; the caller prints the outcome
	}
	var sb strings.Builder
	sb.WriteString(m.header + "\n")
	sb.WriteString(m.filter.View() + "\n")
	if len(m.visible) == 0 {
		sb.WriteString(styleDesc.Render("  (no matches)") + "\n")
	}
	for vi, idx := range m.visible {
		line := "  " + m.items[idx].label
		if vi == m.sel {
			line = styleSelected.Render("❯ " + m.items[idx].label)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(styleHint.Render("  ↑↓ navigate · type to search · enter select · esc cancel"))
	return sb.String()
}

// pickInteractive runs the picker and returns the chosen item index.
// ok=false means the user cancelled.
func (r *Repl) pickInteractive(header string, items []pickerItem) (int, bool, error) {
	m := newPickerModel(header, items)
	p := tea.NewProgram(m, tea.WithInput(r.In), tea.WithOutput(r.Out))
	res, err := p.Run()
	if err != nil {
		return -1, false, err
	}
	final := res.(*pickerModel)
	if final.cancel || final.choice < 0 {
		return -1, false, nil
	}
	return final.choice, true, nil
}
