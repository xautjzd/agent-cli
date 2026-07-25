package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/theme"
	"github.com/xautjzd/agent-cli/internal/version"
)

// Full-screen interactive UI. Unlike the per-line inline editor (which the
// terminal reflows on resize, leaving stacked ghost frames), this is a single
// persistent bubbletea program in the alternate screen: a scrolling
// conversation viewport on top and a bottom-pinned input. Because it owns the
// whole viewport, a resize triggers one clean full repaint — no artifacts, the
// input stays pinned at the bottom, and history stays visible and scrollable.
//
// It reuses the existing command/turn logic: output is routed to an in-memory
// scrollback that the viewport renders, and mid-turn prompts (permission
// confirmations, numbered pickers) are serviced by the running program via
// r.tuiAsk, so no nested program is ever needed.

// scrollback is the thread-safe text buffer the viewport renders. Both the
// command output (r.Out) and the agent event sink write to it; every write
// notifies the program so the viewport refreshes live (streaming output).
type scrollback struct {
	mu     sync.Mutex
	buf    strings.Builder
	notify func()
}

func (s *scrollback) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.buf.Write(p)
	s.mu.Unlock()
	if s.notify != nil {
		s.notify()
	}
	return len(p), nil
}

func (s *scrollback) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Reset clears the buffer so the transcript can be reprinted from scratch
// (used when /theme re-colors the whole session).
func (s *scrollback) Reset() {
	s.mu.Lock()
	s.buf.Reset()
	s.mu.Unlock()
	if s.notify != nil {
		s.notify()
	}
}

// --- messages ---------------------------------------------------------------

type refreshMsg struct{}             // scrollback changed; re-render the viewport
type turnDoneMsg struct{ done bool } // a turn finished (done => quit)
type escExpireMsg struct{}           // the double-Esc arm window elapsed

// escWindow is how long after a first Esc a second Esc still counts as the
// double-tap that interrupts a running turn.
const escWindow = time.Second
type askMsg struct {                 // a mid-turn text prompt needs answering
	prompt string
	secret bool // mask the input (API keys, passwords)
	reply  chan tuiReply
}
type tuiReply struct {
	text string
	ok   bool
}

// selectMsg opens an arrow-navigable selection overlay and delivers the chosen
// index back on reply. It powers /resume, /config, and any other list picker,
// so selection is by ↑/↓ (with type-to-search) rather than typing a number.
type selectMsg struct {
	title string
	items []pickerItem
	reply chan pickReply
	// preview, when set, is called with the original item index each time the
	// highlight moves (and on open), so a picker can show a live preview of the
	// highlighted choice (e.g. /theme applies the theme as you scroll).
	preview func(int)
}
type pickReply struct {
	idx int
	ok  bool
}

// pickState is the live state of an open selection overlay.
type pickState struct {
	title    string
	items    []pickerItem
	filtered []int // indexes into items matching the search
	search   string
	sel      int // index into filtered
	offset   int // first visible row
	reply    chan pickReply
	preview  func(int)
}

// pickRows is the visible height of a selection overlay's list.
const pickRows = 10

// tuiModel is the bubbletea model for the full-screen UI.
type tuiModel struct {
	repl    *Repl
	ctx     context.Context
	program *tea.Program
	sb      *scrollback

	vp    viewport.Model
	input textinput.Model
	ready bool // first WindowSizeMsg received
	w, h  int

	busy       bool               // a turn is running
	turnCancel context.CancelFunc // cancels the running turn (Ctrl-C)

	// escArmed is set after the first Esc during a running turn: the next Esc
	// within escWindow interrupts it (double-Esc to abort, like the mainstream
	// agents). escAt records when the first Esc landed so a stale arm expires.
	escArmed bool
	escAt    time.Time
	// interrupting marks that the current turn is ending because the user
	// aborted it (Esc/Ctrl-C), so turnDone returns to the prompt instead of
	// quitting — the user can add more and continue the conversation.
	interrupting bool

	// completion popup state (reuses the inline editor's logic)
	cands  []candidate
	sel    int
	offset int

	// ask is set while a mid-turn text prompt (permission) is awaiting the
	// user's answer; pick is set while an arrow-navigable selection overlay
	// (/resume, /config) is open; stats is set while the /stats overview is
	// open. At most one is active at a time.
	ask   *askMsg
	pick  *pickState
	stats *statsState

	quitting bool
}

// statsMsg opens the /stats overview overlay, delivering a signal back on reply
// when the user closes it so the command goroutine can resume.
type statsMsg struct {
	data  statsData
	reply chan struct{}
}

// tui styles.
var (
	styleWorking = lipgloss.NewStyle().Faint(true)                                          // "working…" placeholder
	styleAsk     = lipgloss.NewStyle().Bold(true).Foreground(theme.Current().AccentColor()) // overlay title
)

// printBanner writes the startup banner to w: a boxed header showing the
// version, active provider/model, and project path — the orientation info the
// mainstream coding agents surface on launch. Shared by runTUI and /theme
// re-render so the reprinted transcript keeps its header.
func (r *Repl) printBanner(w io.Writer) {
	th := theme.Current()

	// label renders "key   value" with a dimmed, fixed-width key so the values
	// line up in a column.
	label := func(k, v string) string {
		return th.Paint(th.Muted, fmt.Sprintf("%-9s", k)) + v
	}
	effort, _ := provider.ParseEffort(r.Cfg.Thinking)
	rows := []string{
		th.Paint(th.Accent, "✻ agent-cli") + th.Paint(th.Muted, "  v"+version.Version),
		"",
		label("provider", r.Cfg.Provider),
		label("model", r.Cfg.Model),
		label("effort", string(effort)),
		label("cwd", abbrevHome(r.WorkDir)),
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderColor()).
		Padding(0, 2).
		Render(strings.Join(rows, "\n"))

	fmt.Fprintln(w, box)
	fmt.Fprintf(w, "%s\n", th.Paint(th.Muted, "Type a task · \"@path\" to reference files · \"/\" for commands · /exit to quit"))
}

// abbrevHome shortens a path under the user's home directory to a leading "~",
// matching how shells and other CLIs present the working directory.
func abbrevHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel := strings.TrimPrefix(p, home+string(os.PathSeparator)); rel != p {
		return "~" + string(os.PathSeparator) + rel
	}
	return p
}

// runTUI runs the full-screen interactive session. It swaps r.Out and the
// agent's event sink onto an in-memory scrollback for the duration, restoring
// them on exit.
func (r *Repl) runTUI(ctx context.Context) error {
	// Restyle the input box / marker / overlay from the configured theme: the
	// package-level lipgloss styles were built at init time (default theme),
	// before main applied the config, so a non-default configured theme needs
	// them rebuilt now.
	applyThemeStyles()

	realOut := r.Out
	sb := &scrollback{}

	ti := textinput.New()
	ti.Prompt = "> "
	ti.Focus()

	m := &tuiModel{
		repl:  r,
		ctx:   ctx,
		sb:    sb,
		input: ti,
	}

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion(),
		tea.WithInput(r.In), tea.WithOutput(realOut))
	m.program = p

	// Seed the banner while notify is still nil, so it does not try to Send to
	// the program before its event loop is running (which would deadlock).
	r.printBanner(sb)

	// Refresh on every scrollback write. Send from a goroutine so a write from
	// within Update (or before Run starts) never blocks the caller.
	sb.notify = func() { go p.Send(refreshMsg{}) }

	// Route all conversation output through the scrollback while the TUI runs.
	r.Out = sb
	r.sb = sb
	oldEvents := r.Agent.Events
	r.Agent.Events = newTUIEvents(sb)
	r.tuiAsk = m.requestInput
	r.tuiAskSecret = m.requestSecret
	r.tuiSelect = m.requestSelect
	r.tuiSelectPreview = m.requestSelectPreview
	r.tuiStats = m.requestStats
	r.useTUI = false // in-model overlays replace the old nested-program pickers
	defer func() {
		r.Out = realOut
		r.sb = nil
		r.Agent.Events = oldEvents
		r.tuiAsk = nil
		r.tuiAskSecret = nil
		r.tuiSelect = nil
		r.tuiSelectPreview = nil
		r.tuiStats = nil
	}()

	_, err := p.Run()
	return err
}

// requestInput services readInput from within the running program: it posts an
// ask prompt and blocks the calling (turn) goroutine until the user answers.
func (m *tuiModel) requestInput(prompt string) (string, bool) {
	return m.ask2(prompt, false)
}

// requestSecret is requestInput with a masked echo (for API keys).
func (m *tuiModel) requestSecret(prompt string) (string, bool) {
	return m.ask2(prompt, true)
}

func (m *tuiModel) ask2(prompt string, secret bool) (string, bool) {
	reply := make(chan tuiReply, 1)
	m.program.Send(askMsg{prompt: prompt, secret: secret, reply: reply})
	r := <-reply
	return r.text, r.ok
}

// requestSelect opens an arrow-navigable selection overlay and blocks the
// calling (turn) goroutine until the user picks an item or cancels.
func (m *tuiModel) requestSelect(title string, items []pickerItem) (int, bool) {
	return m.requestSelectPreview(title, items, nil)
}

// requestSelectPreview is requestSelect with a live-preview callback invoked as
// the highlight moves (and on open); on cancel the caller reverts the preview.
func (m *tuiModel) requestSelectPreview(title string, items []pickerItem, preview func(int)) (int, bool) {
	reply := make(chan pickReply, 1)
	m.program.Send(selectMsg{title: title, items: items, reply: reply, preview: preview})
	rp := <-reply
	return rp.idx, rp.ok
}

// requestStats opens the /stats overview overlay and blocks the calling (turn)
// goroutine until the user closes it.
func (m *tuiModel) requestStats(data statsData) {
	reply := make(chan struct{}, 1)
	m.program.Send(statsMsg{data: data, reply: reply})
	<-reply
}

func (m *tuiModel) Init() tea.Cmd { return textinput.Blink }

func (m *tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		// Mark ready before refreshing: refreshViewport early-returns while
		// !ready, so on the first size message the seeded banner would
		// otherwise not render until the next refresh (the user's first input).
		m.ready = true
		m.layout()
		m.refreshViewport()
		return m, nil

	case refreshMsg:
		m.refreshViewport()
		return m, nil

	case escExpireMsg:
		// The double-Esc window elapsed without a second tap: disarm so the
		// footer drops the "press esc again" prompt.
		if m.escArmed && time.Since(m.escAt) >= escWindow {
			m.escArmed = false
		}
		return m, nil

	case askMsg:
		// A mid-turn prompt: focus a fresh answer field at the bottom, masking
		// the echo for secrets (API keys).
		m.ask = &msg
		m.input.SetValue("")
		m.input.Prompt = "  " + strings.TrimSpace(msg.prompt) + " "
		if msg.secret {
			m.input.EchoMode = textinput.EchoPassword
		} else {
			m.input.EchoMode = textinput.EchoNormal
		}
		m.input.Focus()
		m.cands = nil
		return m, nil

	case selectMsg:
		// A selection overlay: arrow keys navigate, typing filters.
		m.pick = &pickState{title: msg.title, items: msg.items, reply: msg.reply, preview: msg.preview}
		m.pickRefilter() // also previews the initially highlighted item
		return m, nil

	case statsMsg:
		// The stats overview takes over the screen until closed.
		m.stats = newStatsState(msg.data, msg.reply)
		return m, nil

	case turnDoneMsg:
		m.busy = false
		m.turnCancel = nil
		m.escArmed = false
		m.input.Prompt = m.basePrompt()
		m.input.Focus()
		// handleLine reports done=true on a cancelled context, but a user
		// interrupt should return to the prompt, not quit — only a real /exit
		// ends the session.
		if msg.done && !m.interrupting {
			m.quitting = true
			return m, tea.Quit
		}
		m.interrupting = false
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Forward other messages (mouse, etc.) to the viewport for scrolling.
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// handleKey routes a keypress by mode: answering a prompt, running a turn, or
// idle editing.
func (m *tuiModel) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// A selection overlay captures all keys (including its own arrow nav).
	if m.pick != nil {
		return m.handlePickKey(key)
	}

	// The stats overview captures all keys until closed.
	if m.stats != nil {
		return m.handleStatsKey(key)
	}

	// Scrolling works in every other mode.
	switch key.Type {
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyCtrlU, tea.KeyCtrlD:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(key)
		return m, cmd
	}

	if m.ask != nil {
		return m.handleAskKey(key)
	}
	if m.busy {
		return m.handleBusyKey(key)
	}
	return m.handleIdleKey(key)
}

// handleBusyKey handles keys while a turn runs: the only accepted actions are
// interrupting it, either with Ctrl-C or by pressing Esc twice in quick
// succession (the double-tap the mainstream agents use to abort streaming).
func (m *tuiModel) handleBusyKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyCtrlC:
		m.interruptTurn()
		return m, nil
	case tea.KeyEsc:
		if m.escArmed && time.Since(m.escAt) <= escWindow {
			m.escArmed = false
			m.interruptTurn()
			return m, nil
		}
		// First tap: arm the window and prompt for a confirming second tap.
		m.escArmed = true
		m.escAt = time.Now()
		return m, tea.Tick(escWindow, func(time.Time) tea.Msg { return escExpireMsg{} })
	}
	return m, nil
}

// interruptTurn stops the running turn and returns to the input prompt: it
// cancels the turn (halting streaming/tool execution) and flags the abort so
// turnDone does not quit. Whatever streamed so far is kept, so the user can add
// more and continue the conversation.
func (m *tuiModel) interruptTurn() {
	m.interrupting = true
	if m.turnCancel != nil {
		m.turnCancel()
	}
	fmt.Fprintln(m.sb, "\033[2m⏹ interrupted — type to add more and continue\033[0m")
}

// handlePickKey drives the selection overlay: ↑/↓ move, Enter chooses, Esc or
// Ctrl-C cancels, and any printable key filters the list by search text.
func (m *tuiModel) handlePickKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pick
	switch key.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		if len(p.filtered) > 0 {
			p.sel = (p.sel - 1 + len(p.filtered)) % len(p.filtered)
			m.pickScroll()
			m.pickPreview()
		}
		return m, nil
	case tea.KeyDown, tea.KeyCtrlN:
		if len(p.filtered) > 0 {
			p.sel = (p.sel + 1) % len(p.filtered)
			m.pickScroll()
			m.pickPreview()
		}
		return m, nil
	case tea.KeyEnter:
		idx, ok := -1, false
		if p.sel >= 0 && p.sel < len(p.filtered) {
			idx, ok = p.filtered[p.sel], true
		}
		p.reply <- pickReply{idx: idx, ok: ok}
		m.pick = nil
		return m, nil
	case tea.KeyEsc, tea.KeyCtrlC:
		p.reply <- pickReply{idx: -1, ok: false}
		m.pick = nil
		return m, nil
	case tea.KeyBackspace:
		if p.search != "" {
			p.search = p.search[:len(p.search)-1]
			m.pickRefilter()
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		p.search += string(key.Runes)
		if key.Type == tea.KeySpace {
			p.search += " "
		}
		m.pickRefilter()
		return m, nil
	}
	return m, nil
}

// pickRefilter recomputes the visible items from the search text.
func (m *tuiModel) pickRefilter() {
	p := m.pick
	q := strings.ToLower(strings.TrimSpace(p.search))
	p.filtered = p.filtered[:0]
	for i, it := range p.items {
		if q == "" || strings.Contains(strings.ToLower(it.filterText), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.sel >= len(p.filtered) {
		p.sel = 0
	}
	m.pickScroll()
	m.pickPreview()
}

// pickPreview invokes the overlay's preview callback for the currently
// highlighted item, so pickers like /theme show the choice live as you move.
func (m *tuiModel) pickPreview() {
	p := m.pick
	if p == nil || p.preview == nil || p.sel < 0 || p.sel >= len(p.filtered) {
		return
	}
	p.preview(p.filtered[p.sel])
}

func (m *tuiModel) pickScroll() {
	p := m.pick
	if p.sel < p.offset {
		p.offset = p.sel
	}
	if p.sel >= p.offset+pickRows {
		p.offset = p.sel - pickRows + 1
	}
}

// pickView renders the selection overlay shown in place of the input box.
func (m *tuiModel) pickView() string {
	p := m.pick
	var b strings.Builder
	title := p.title
	if p.search != "" {
		title += "  (filter: " + p.search + ")"
	}
	b.WriteString(styleAsk.Render(title) + "\n")

	end := p.offset + pickRows
	if end > len(p.filtered) {
		end = len(p.filtered)
	}
	if p.offset > 0 {
		b.WriteString(styleHint.Render(fmt.Sprintf("  ↑ %d more", p.offset)) + "\n")
	}
	for i := p.offset; i < end; i++ {
		it := p.items[p.filtered[i]]
		line := "  " + it.label
		if i == p.sel {
			line = styleSelected.Render("❯ " + it.label)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if len(p.filtered) == 0 {
		b.WriteString(styleHint.Render("  (no matches)"))
	}
	if end < len(p.filtered) {
		b.WriteString("\n" + styleHint.Render(fmt.Sprintf("  ↓ %d more", len(p.filtered)-end)))
	}
	b.WriteString("\n" + styleHint.Render("  ↑↓ select · enter choose · type to filter · esc cancel"))
	return b.String()
}

// handleAskKey answers a pending mid-turn prompt.
func (m *tuiModel) handleAskKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEnter:
		val := m.input.Value()
		m.ask.reply <- tuiReply{text: val, ok: true}
		m.ask = nil
		m.input.SetValue("")
		m.input.EchoMode = textinput.EchoNormal
		m.input.Prompt = m.workingPrompt()
		return m, nil
	case tea.KeyCtrlC:
		m.ask.reply <- tuiReply{ok: false}
		m.ask = nil
		m.input.SetValue("")
		m.input.EchoMode = textinput.EchoNormal
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

// handleIdleKey edits the input line and completion popup, and submits.
func (m *tuiModel) handleIdleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyCtrlC, tea.KeyCtrlD:
		m.quitting = true
		return m, tea.Quit

	case tea.KeyCtrlV:
		m.pasteImage()
		return m, nil

	case tea.KeyEnter:
		if c, ok := m.selectedCand(); ok && m.wouldChange(c) {
			m.acceptCand(c)
			switch {
			case strings.HasPrefix(c.text, "/"):
				// A highlighted slash command: if it takes known argument
				// values, open the value picker so the user can pick one
				// (Tab still just fills). Otherwise run it immediately.
				if m.maybeOpenArgPicker() {
					return m, nil
				}
				return m.submit()
			case m.completedSlashArg():
				// Picked a value for a "/cmd <value>" command → run it.
				return m.submit()
			default:
				// A @file completion is only filled in — the message is
				// still being typed.
				return m, nil
			}
		}
		// A bare "/effort"-style command with a value picker opens it rather
		// than running with no argument.
		if m.maybeOpenArgPicker() {
			return m, nil
		}
		return m.submit()

	case tea.KeyTab:
		if c, ok := m.selectedCand(); ok {
			m.acceptCand(c)
		}
		return m, nil

	case tea.KeyEsc:
		m.cands = nil
		return m, nil

	// ↑/↓ and the Emacs Ctrl-P/Ctrl-N move through the completion menu (when
	// open) or input history (when not).
	case tea.KeyUp, tea.KeyCtrlP:
		m.idleUp()
		return m, nil

	case tea.KeyDown, tea.KeyCtrlN:
		m.idleDown()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	m.refreshCands()
	return m, cmd
}

// idleUp/idleDown move through the completion menu when it is open, otherwise
// through input history.
func (m *tuiModel) idleUp() {
	if len(m.cands) > 0 {
		m.sel = (m.sel - 1 + len(m.cands)) % len(m.cands)
		m.scrollCands()
		return
	}
	m.input.SetValue(m.repl.historyPrev(m.input.Value()))
	m.input.CursorEnd()
}

func (m *tuiModel) idleDown() {
	if len(m.cands) > 0 {
		m.sel = (m.sel + 1) % len(m.cands)
		m.scrollCands()
		return
	}
	m.input.SetValue(m.repl.historyNext())
	m.input.CursorEnd()
}

// submit runs the current input line as a turn.
func (m *tuiModel) submit() (tea.Model, tea.Cmd) {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return m, nil
	}
	m.repl.historyAdd(line)
	// Echo the submitted line into the scrollback with the ❯ marker.
	label := line
	if p := m.basePrompt(); p != "> " {
		label = p + line
	}
	fmt.Fprintf(m.sb, "\n%s\n", styleMarker.Render("❯ "+label))

	m.input.SetValue("")
	m.cands = nil
	m.busy = true
	m.input.Prompt = m.workingPrompt()

	tctx, cancel := context.WithCancel(m.ctx)
	m.turnCancel = cancel
	return m, func() tea.Msg {
		done := m.repl.handleLine(tctx, line)
		return turnDoneMsg{done: done}
	}
}

// --- completion (reuses the inline editor's helpers) ------------------------

func (m *tuiModel) refreshCands() {
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
		// Open on the candidate that matches the active setting (e.g. the
		// running /effort level) when one is marked, otherwise the first row.
		m.sel = defaultCandIdx(m.cands)
		m.offset = 0
	}
	m.scrollCands()
}

// defaultCandIdx returns the index of the candidate flagged as current, or 0.
func defaultCandIdx(cands []candidate) int {
	for i, c := range cands {
		if c.current {
			return i
		}
	}
	return 0
}

func (m *tuiModel) scrollCands() {
	if m.sel < m.offset {
		m.offset = m.sel
	}
	if m.sel >= m.offset+popupRows {
		m.offset = m.sel - popupRows + 1
	}
}

func (m *tuiModel) selectedCand() (candidate, bool) {
	if m.sel >= 0 && m.sel < len(m.cands) {
		return m.cands[m.sel], true
	}
	return candidate{}, false
}

func (m *tuiModel) wouldChange(c candidate) bool {
	v := m.input.Value()
	start, end := tokenBounds(v, runePosToByte(v, m.input.Position()))
	return v[start:end] != c.text
}

// maybeOpenArgPicker turns a bare "/cmd" whose argument has known values
// (e.g. /effort) into "/cmd " and opens the value picker, so Enter walks into
// an interactive choice instead of running the command with no argument.
// Returns false (leaving the input untouched) for anything else.
func (m *tuiModel) maybeOpenArgPicker() bool {
	line := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(line, "/") || strings.ContainsRune(line, ' ') {
		return false
	}
	if !m.repl.commandCompletesArgs(line[1:]) {
		return false
	}
	m.input.SetValue(line + " ")
	m.input.CursorEnd()
	m.refreshCands()
	return true
}

// completedSlashArg reports whether the input is now a "/cmd <value>" for a
// command with a known value set, so Enter on a picked value runs it.
func (m *tuiModel) completedSlashArg() bool {
	line := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(line, "/") {
		return false
	}
	cmd, rest, found := strings.Cut(line[1:], " ")
	if !found || strings.TrimSpace(rest) == "" {
		return false
	}
	return m.repl.commandCompletesArgs(cmd)
}

func (m *tuiModel) acceptCand(c candidate) {
	value, pos := acceptCandidate(m.input.Value(), m.input.Position(), c)
	m.input.SetValue(value)
	m.input.SetCursor(pos)
	m.refreshCands()
}

// pasteImage mirrors the inline editor's Ctrl+V behavior.
func (m *tuiModel) pasteImage() {
	data, err := readClipboardImage()
	if err != nil {
		return
	}
	path, err := savePastedImage(data)
	if err != nil {
		return
	}
	n := m.repl.addPastedImage(path)
	token := fmt.Sprintf("[Image #%d] ", n)
	v, pos := m.input.Value(), m.input.Position()
	bp := runePosToByte(v, pos)
	m.input.SetValue(v[:bp] + token + v[bp:])
	m.input.SetCursor(pos + utf8.RuneCountInString(token))
}

// --- layout & rendering -----------------------------------------------------

func (m *tuiModel) basePrompt() string {
	if m.repl.planMode {
		return "plan> "
	}
	return "> "
}

func (m *tuiModel) workingPrompt() string { return "> " }

// layout sizes the viewport to fill everything above the footer.
func (m *tuiModel) layout() {
	footer := lipgloss.Height(m.footer())
	vpH := m.h - footer
	if vpH < 1 {
		vpH = 1
	}
	if !m.ready {
		m.vp = viewport.New(m.w, vpH)
	} else {
		m.vp.Width = m.w
		m.vp.Height = vpH
	}
}

// refreshViewport re-wraps the scrollback to the current width and shows it,
// keeping the view pinned to the newest output.
func (m *tuiModel) refreshViewport() {
	if !m.ready {
		return
	}
	m.layout() // footer height can change (popup shown/hidden)
	wrap := lipgloss.NewStyle().Width(m.vp.Width)
	m.vp.SetContent(wrap.Render(strings.TrimRight(m.sb.String(), "\n")))
	m.vp.GotoBottom()
}

// footer renders the bottom region: completion popup (if any) above the input
// box, or the working indicator while a turn runs.
func (m *tuiModel) footer() string {
	width := m.boxWidth()

	// Tint the input prompt "> " with the active accent so the box is themed
	// end to end (rebuilt each frame so a /theme switch takes effect at once).
	m.input.PromptStyle = lipgloss.NewStyle().Foreground(theme.Current().AccentColor())

	// A selection overlay takes over the whole footer.
	if m.pick != nil {
		return m.pickView()
	}

	var b strings.Builder
	// Completion popup (idle only).
	if m.ask == nil && !m.busy && len(m.cands) > 0 {
		b.WriteString(m.popupView() + "\n")
	}

	box := styleInputBox.Width(width).MaxHeight(3)
	switch {
	case m.ask != nil:
		b.WriteString(box.Render(m.input.View()))
	case m.busy:
		hint := "working… (esc esc or Ctrl-C to interrupt)"
		if m.escArmed {
			hint = "working… (press esc again to interrupt)"
		}
		b.WriteString(box.Render(styleWorking.Render(hint)))
		b.WriteString("\n" + styleHint.Render("  interrupting keeps the conversation — add more and continue"))
	default:
		b.WriteString(box.Render(m.input.View()))
		b.WriteString("\n" + styleHint.Render("  ↑↓ history/menu · tab accept · pgup/pgdn scroll · /copy · /exit"))
	}
	return b.String()
}

func (m *tuiModel) popupView() string {
	var b strings.Builder
	end := m.offset + popupRows
	if end > len(m.cands) {
		end = len(m.cands)
	}
	if m.offset > 0 {
		b.WriteString(styleHint.Render(fmt.Sprintf("  ↑ %d more", m.offset)) + "\n")
	}
	for i := m.offset; i < end; i++ {
		c := m.cands[i]
		desc := c.desc
		if c.current {
			desc += " (current)"
		}
		padded := " " + truncPad(c.text, 28)
		line := padded + " " + styleDesc.Render(desc)
		if i == m.sel {
			line = styleSelected.Render(padded) + " " + styleDesc.Render(desc)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	if end < len(m.cands) {
		b.WriteString("\n" + styleHint.Render(fmt.Sprintf("  ↓ %d more", len(m.cands)-end)))
	}
	return b.String()
}

func (m *tuiModel) boxWidth() int {
	w := m.w - 2
	if w < 12 {
		w = 12
	}
	return w
}

func (m *tuiModel) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "starting…"
	}
	// The stats overview replaces the transcript view while it is open.
	if m.stats != nil {
		return m.stats.view(m.w)
	}
	return m.vp.View() + "\n" + m.footer()
}

// truncPad truncates or right-pads s to n display columns.
func truncPad(s string, n int) string {
	if lipgloss.Width(s) > n {
		return lipgloss.NewStyle().MaxWidth(n).Render(s)
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}
