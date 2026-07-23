package repl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"sort"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/catalog"
	"github.com/xautjzd/agent-cli/internal/checkpoint"
	usercmd "github.com/xautjzd/agent-cli/internal/command"
	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/home"
	"github.com/xautjzd/agent-cli/internal/hook"
	"github.com/xautjzd/agent-cli/internal/lsp"
	"github.com/xautjzd/agent-cli/internal/mcp"
	"github.com/xautjzd/agent-cli/internal/memory"
	"github.com/xautjzd/agent-cli/internal/permission"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/skill"
	"github.com/xautjzd/agent-cli/internal/subagent"
	"github.com/xautjzd/agent-cli/internal/textwidth"
	"github.com/xautjzd/agent-cli/internal/tool"
	"github.com/xautjzd/agent-cli/internal/usage"
)

// errExit signals a clean user-requested shutdown of the loop.
var errExit = errors.New("exit")

// Repl drives the interactive session. It owns input parsing and command
// dispatch; the agent owns the conversation (SRP: UI concerns stay here).
type Repl struct {
	Agent  *agent.Agent
	Cfg    *config.Config
	Skills skill.Repository
	// Commands holds user-defined slash commands (prompt templates); nil
	// disables them.
	Commands usercmd.Repository
	Memory   memory.Store
	Tools    *tool.Registry
	WorkDir  string
	In       io.Reader
	Out      io.Writer

	// Sessions persists conversation history for /resume; nil disables
	// session recording.
	Sessions session.Store

	// Checkpoints records a restore point before each turn (conversation
	// position + file snapshots) backing /rewind; nil disables the feature.
	Checkpoints *checkpoint.Manager

	// LSP manages language servers backing the code-navigation tools; nil
	// disables the /lsp listing (the tools themselves are still registered).
	LSP *lsp.Manager

	// MCP holds the live Model Context Protocol connections whose tools are
	// merged into Tools; nil when no MCP servers are configured. The REPL
	// owns closing it on shutdown.
	MCP *mcp.Manager

	// Spawner backs the "task" tool's subagent delegation; nil disables the
	// /agents listing. Used only to enumerate subagent types for display.
	Spawner *subagent.Spawner

	// gateMu serializes BeforeToolCall so concurrent subagents cannot
	// interleave permission prompts or their output.
	gateMu sync.Mutex

	// tuiAsk, when set (full-screen TUI active), services readInput by
	// prompting inside the running program instead of reading stdin directly.
	// tuiAskSecret is the masked variant for credentials.
	tuiAsk       func(prompt string) (string, bool)
	tuiAskSecret func(prompt string) (string, bool)
	// tuiSelect, when set, presents an arrow-navigable selection overlay
	// inside the running program (used by /resume, /config) and returns the
	// chosen index, so lists are picked with ↑/↓ rather than a typed number.
	tuiSelect func(title string, items []pickerItem) (int, bool)

	// Policy decides approval for tool calls (rules + risk classifier); nil
	// lazily builds a default. Audit records every gated decision as
	// structured JSON. SandboxActive reflects whether bash is confined.
	Policy        *permission.Policy
	Audit         *permission.AuditLogger
	SandboxActive bool

	// Hooks runs third-party integration commands at lifecycle events; nil
	// is a no-op (the runner tolerates a nil receiver).
	Hooks *hook.Runner

	// NonInteractive is set for one-shot/CI runs: the gate never waits for a
	// human. A dangerous operation that would normally prompt is denied
	// (unless bypass mode is active), so an unattended run can never hang.
	NonInteractive bool

	// VisionClient overrides the lazily built vision-fallback provider
	// (primarily a test seam).
	VisionClient provider.Provider

	scanner *bufio.Scanner
	useTUI  bool
	current *session.Session
	// pendingTitle holds a title chosen with /rename before the session
	// file exists, so naming a session up front survives its creation.
	pendingTitle string
	// imagePastes maps an "[Image #N]" placeholder number to the absolute
	// path of the pasted image it stands for. The placeholder is what the
	// user sees; the file lives out of sight in the user's agent home.
	imagePastes map[int]string
	pasteSeq    int
	// rawInputs holds what the user typed for each user-role message, in
	// order — persisted as Record.Display so resumed transcripts replay
	// exactly as they looked live (wire content may carry expanded @file
	// blocks the user never saw).
	rawInputs []string

	// goal is the active session goal (/goal). While set, every turn is
	// followed by goal-check rounds that keep the agent working until it
	// verifies completion. GoalMaxRounds caps the rounds per trigger
	// (0 means the default of 8).
	goal          string
	GoalMaxRounds int

	// planMode gates the agent to read-only tools until a proposed plan is
	// approved; fullTools holds the complete registry for restoration.
	planMode  bool
	fullTools *tool.Registry

	// Mode is the configured permission mode ("" means the HITL default).
	// An active goal overrides it to bypass — see permMode.
	Mode permission.Mode

	// Editor state: cached @-completion file list and input history.
	fileCache []string
	history   []string
	histIdx   int
	histDraft string
}

// command is one built-in slash command.
type command struct {
	name    string
	usage   string
	desc    string
	handler func(r *Repl, ctx context.Context, args string) error
}

// commands is the ordered built-in command table. Adding a command is a pure
// addition to this slice (OCP). It is populated in init because cmdHelp
// itself iterates the table, which Go's initializer cycle check rejects for
// a plain composite literal.
var commands []command

func init() {
	commands = []command{
		{"help", "/help", "List commands and installed skills", (*Repl).cmdHelp},
		{"model", "/model [name]", "Show or switch the model", (*Repl).cmdModel},
		{"provider", "/provider <name> [model]", "Switch provider (anthropic, openai, deepseek, custom)", (*Repl).cmdProvider},
		{"skills", "/skills", "List installed skills (run one with /<skill-name> [task])", (*Repl).cmdSkills},
		{"commands", "/commands", "List user-defined slash commands (run one with /<name> [args])", (*Repl).cmdCommands},
		{"tools", "/tools", "List available tools", (*Repl).cmdTools},
		{"todos", "/todos", "Show the agent's current task list (todo_write)", (*Repl).cmdTodos},
		{"mcp", "/mcp", "List connected MCP servers and their tools", (*Repl).cmdMCP},
		{"lsp", "/lsp", "List language servers backing the code-navigation tools", (*Repl).cmdLSP},
		{"agents", "/agents", "List subagent types the task tool can delegate to", (*Repl).cmdAgents},
		{"hooks", "/hooks", "List configured lifecycle hooks (third-party integration)", (*Repl).cmdHooks},
		{"config", "/config [set k v]", "Open the settings panel (view + edit); or set one value", (*Repl).cmdConfig},
		{"memory", "/memory", "List saved project memories", (*Repl).cmdMemory},
		{"goal", "/goal [text|clear]", "Set a session goal the agent works toward until met", (*Repl).cmdGoal},
		{"plan", "/plan [task|off]", "Plan mode: explore read-only, propose a plan, implement on approval", (*Repl).cmdPlan},
		{"mode", "/mode [hitl|bypass]", "Show or switch the permission mode for dangerous operations", (*Repl).cmdMode},
		{"usage", "/usage", "Show token usage, timing, and context occupancy", (*Repl).cmdUsage},
		{"rename", "/rename [title]", "Rename the current session", (*Repl).cmdRename},
		{"new", "/new", "Start a new session (current one stays resumable)", (*Repl).cmdNew},
		{"resume", "/resume [id]", "Resume a previous session in this project", (*Repl).cmdResume},
		{"rewind", "/rewind", "Undo to a checkpoint: restore files and conversation to before an earlier message", (*Repl).cmdRewind},
		{"compact", "/compact", "Summarize earlier turns to free up context now", (*Repl).cmdCompact},
		{"clear", "/clear", "Clear conversation history (keeps system prompt)", (*Repl).cmdClear},
		{"exit", "/exit", "Quit the session", (*Repl).cmdExit},
	}
}

// Run executes the interactive loop until EOF, /exit, or a fatal error.
//
// Key flow per line: a bare "/" opens the command/skill picker; "/name ..."
// dispatches a built-in command, falling back to a skill with that name
// (so "/commit-helper" works like in Claude Code); anything else is a user
// prompt whose @path references are expanded before hitting the agent.
func (r *Repl) Run(ctx context.Context) error {
	// The full-screen TUI needs a real terminal; piped stdin falls back to the
	// plain line loop so scripts and tests keep working.
	r.useTUI = isTerminal(r.In)
	r.scanner = bufio.NewScanner(r.In)
	r.scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// SessionStart hooks fire once the loop is ready; SessionEnd on exit.
	r.fireLifecycle(ctx, hook.SessionStart, "")
	defer r.fireLifecycle(ctx, hook.SessionEnd, "")

	if r.useTUI {
		return r.runTUI(ctx)
	}
	return r.runPlain(ctx)
}

// runPlain is the line-at-a-time loop used for non-terminal input.
func (r *Repl) runPlain(ctx context.Context) error {
	fmt.Fprintf(r.Out, "agent-cli — provider=%s model=%s\n", r.Cfg.Provider, r.Cfg.Model)
	fmt.Fprintln(r.Out, `Type a task, "@path" to reference files, "/" for commands and skills, "/exit" to quit.`)
	for {
		fmt.Fprintln(r.Out)
		prompt := "> "
		if r.planMode {
			prompt = "plan> " // visible reminder that the mode is active
		}
		line, ok := r.readInput(prompt)
		if !ok {
			return nil
		}
		if done := r.handleLine(ctx, strings.TrimSpace(line)); done {
			return nil
		}
	}
}

// handleLine processes one submitted input line: a slash command or a prompt.
// It returns done=true when the session should end (/exit or a Ctrl-C
// interrupt). Output and errors go to r.Out, so both the plain loop and the
// TUI share this logic.
func (r *Repl) handleLine(ctx context.Context, input string) (done bool) {
	if input == "" {
		return false
	}
	var err error
	if strings.HasPrefix(input, "/") {
		err = r.dispatch(ctx, input)
	} else {
		err = r.runPrompt(ctx, input)
	}
	switch {
	case errors.Is(err, errExit):
		return true
	case err != nil && ctx.Err() != nil:
		return true // interrupted with Ctrl-C
	case err != nil:
		fmt.Fprintln(r.Out, "error:", err)
	}
	return false
}

// readInput reads one line. Inside the full-screen TUI it routes through the
// running program (tuiAsk) so mid-turn prompts — permission confirmations,
// numbered pickers, rename — appear as a modal at the bottom without a nested
// program. On a plain terminal it uses the bubbletea line editor; otherwise
// the scanner. ok=false means cancel/EOF.
func (r *Repl) readInput(prompt string) (string, bool) {
	if r.tuiAsk != nil {
		return r.tuiAsk(prompt)
	}
	if r.useTUI {
		line, ok, err := r.editLine(prompt)
		if err != nil {
			// A broken terminal should not kill the session; degrade to
			// plain reading for the rest of it.
			fmt.Fprintln(r.Out, "editor error, falling back to plain input:", err)
			r.useTUI = false
		} else {
			return line, ok
		}
	}
	fmt.Fprint(r.Out, prompt)
	if !r.scanner.Scan() {
		return "", false
	}
	return r.scanner.Text(), true
}

// readSecret reads a credential, masking the echo inside the TUI. Outside the
// TUI it falls back to a normal read (unmasked); that path is for scripts.
func (r *Repl) readSecret(prompt string) (string, bool) {
	if r.tuiAskSecret != nil {
		return r.tuiAskSecret(prompt)
	}
	return r.readInput(prompt)
}

// stripImagesIfNeeded removes image parts from history after switching to
// a text-only model — otherwise every later request would resend them and
// be rejected by the new provider.
func (r *Repl) stripImagesIfNeeded() {
	if r.Cfg.ModelSupportsVision() {
		return
	}
	if n := r.Agent.StripImageParts(); n > 0 {
		fmt.Fprintf(r.Out, "Note: %d image message(s) in history replaced with placeholders — %s has no vision.\n", n, r.Cfg.Model)
	}
}

// addPastedImage records a pasted image file and returns the placeholder
// number that will refer to it in the input.
func (r *Repl) addPastedImage(path string) int {
	if r.imagePastes == nil {
		r.imagePastes = map[int]string{}
	}
	r.pasteSeq++
	r.imagePastes[r.pasteSeq] = path
	return r.pasteSeq
}

// isTerminal reports whether the reader is an interactive terminal.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// runPrompt expands @references and hands the message to the agent. In
// plan mode every ordinary input is a planning turn.
func (r *Repl) runPrompt(ctx context.Context, input string) error {
	if r.planMode {
		return r.runPlanTurn(ctx, input)
	}
	// UserPromptSubmit hooks may block the turn or inject extra context.
	augmented, blocked := r.onUserPromptSubmit(ctx, input)
	if blocked {
		return nil
	}
	msg, err := buildUserMessage(augmented, r.WorkDir, r.imagePastes, nil)
	if err != nil {
		return err
	}
	if msg, err = r.prepareImageMessage(ctx, msg); err != nil {
		return err
	}
	// Open a restore point before the turn runs, capturing the conversation
	// position; the turn's file edits snapshot into it as they happen, so
	// /rewind can undo this message and its effects.
	if r.Checkpoints != nil {
		r.Checkpoints.Begin(input, len(r.Agent.History()), len(r.rawInputs))
	}
	_, err = r.Agent.RunMessage(ctx, msg)
	// The raw input (not the hook-augmented form) is what the user typed, so
	// that is what the session records for replay.
	r.saveSession(input)
	if err != nil {
		return err
	}
	// Stop hooks fire when the agent finishes responding to the turn.
	r.fireLifecycle(ctx, hook.Stop, "")
	// An active /goal keeps the agent working after every turn.
	return r.checkGoal(ctx)
}

// saveSession syncs the conversation into the current session file. The
// session is created lazily on the first real turn so command-only sessions
// leave no empty files. Persistence failures are reported but never abort
// the conversation.
func (r *Repl) saveSession(rawInput string) {
	if r.Sessions == nil {
		return
	}
	if r.current == nil {
		now := time.Now()
		title := r.pendingTitle
		if title == "" {
			title = session.TitleFrom(rawInput)
		}
		r.pendingTitle = ""
		r.current = &session.Session{Meta: session.Meta{
			ID:        session.NewID(now),
			Title:     title,
			CreatedAt: now,
		}}
	}
	r.rawInputs = append(r.rawInputs, rawInput)
	r.syncSession()
}

// syncSession writes the current agent history into the session file without
// recording a new input. It is the shared persistence path for saveSession
// (after a turn) and /rewind (after trimming history), so both agree on how
// records align to raw inputs.
func (r *Repl) syncSession() {
	if r.Sessions == nil || r.current == nil {
		return
	}
	r.current.Provider = r.Cfg.Provider
	r.current.Model = r.Cfg.Model
	r.current.Goal = r.goal

	// System prompt excluded: it is rebuilt on resume. Real user records carry
	// the raw typed input for replay. Compaction can inject a synthetic
	// summary user turn and drop older real turns from the front, so the
	// surviving real user turns are the most recent ones — align them to the
	// tail of rawInputs (last N), and give a summary turn no Display so it
	// replays as its own compaction notice.
	msgs := r.Agent.History()[1:]
	realUsers := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser && !agent.IsSummaryMessage(m) {
			realUsers++
		}
	}
	offset := len(r.rawInputs) - realUsers
	if offset < 0 {
		offset = 0
	}
	records := make([]session.Record, len(msgs))
	userIdx := 0
	for i, m := range msgs {
		records[i] = session.Record{Message: m}
		if m.Role == provider.RoleUser && !agent.IsSummaryMessage(m) {
			if idx := offset + userIdx; idx >= 0 && idx < len(r.rawInputs) {
				records[i].Display = r.rawInputs[idx]
			}
			userIdx++
		}
	}
	r.current.Messages = records
	if err := r.Sessions.Save(r.current); err != nil {
		fmt.Fprintln(r.Out, "warning: session not saved:", err)
	}
}

// dispatch routes one "/..." line: bare slash → picker, built-in command,
// then skill-name fallback.
func (r *Repl) dispatch(ctx context.Context, input string) error {
	name, args, _ := strings.Cut(strings.TrimPrefix(input, "/"), " ")
	args = strings.TrimSpace(args)

	if name == "" {
		return r.picker(ctx)
	}
	for _, c := range commands {
		if c.name == name || (name == "quit" && c.name == "exit") {
			return c.handler(r, ctx, args)
		}
	}
	// Not a built-in: a user-defined slash command takes precedence over a
	// skill of the same name, since the user authored it as a command.
	if r.Commands != nil {
		if _, err := r.Commands.Load(name); err == nil {
			return r.invokeCommand(ctx, name, args)
		}
	}
	// Finally treat "/<skill-name> [task]" as explicit skill use.
	if _, err := r.Skills.Load(name); err == nil {
		return r.invokeSkill(ctx, name, args)
	}
	return fmt.Errorf("unknown command or skill %q — type / to list both", name)
}

// invokeCommand runs a user-defined slash command: its prompt body is filled
// with the arguments and any @file references, then sent to the agent as an
// ordinary user turn. The raw "/name args" is what the session records.
func (r *Repl) invokeCommand(ctx context.Context, name, args string) error {
	cmd, err := r.Commands.Load(name)
	if err != nil {
		return err
	}
	prompt := usercmd.Expand(cmd.Body, args)
	if prompt, err = ExpandFileRefs(prompt, r.WorkDir); err != nil {
		return err
	}
	if strings.TrimSpace(prompt) == "" {
		return fmt.Errorf("command %q has an empty prompt", name)
	}
	_, err = r.Agent.Run(ctx, prompt)
	r.saveSession(strings.TrimSpace("/" + name + " " + args))
	if err != nil {
		return err
	}
	return r.checkGoal(ctx)
}

// picker renders a numbered menu of commands and skills and executes the
// selection. This is the stdlib-friendly equivalent of the fuzzy "/" menus
// in Claude Code and pi.
func (r *Repl) picker(ctx context.Context) error {
	fmt.Fprintln(r.Out, "Commands:")
	for i, c := range commands {
		fmt.Fprintf(r.Out, "  %2d. %-28s %s\n", i+1, c.usage, c.desc)
	}
	var customs []usercmd.Command
	if r.Commands != nil {
		customs, _ = r.Commands.List()
	}
	for i, c := range customs {
		if i == 0 {
			fmt.Fprintln(r.Out, "Custom commands:")
		}
		fmt.Fprintf(r.Out, "  %2d. %-28s %s\n", len(commands)+i+1, "/"+c.Name, c.Description)
	}
	skills, _ := r.Skills.List()
	for i, s := range skills {
		if i == 0 {
			fmt.Fprintln(r.Out, "Skills:")
		}
		fmt.Fprintf(r.Out, "  %2d. %-28s %s\n", len(commands)+len(customs)+i+1, "/"+s.Name, s.Description)
	}

	line, ok := r.readInput("Select a number (or press Enter to cancel): ")
	if !ok {
		return errExit
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return nil
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(commands)+len(customs)+len(skills) {
		return fmt.Errorf("invalid selection %q", choice)
	}

	if n <= len(commands) {
		c := commands[n-1]
		args, ok := r.readInput(fmt.Sprintf("Arguments for %s (or Enter for none): ", c.usage))
		if !ok {
			return errExit
		}
		return c.handler(r, ctx, strings.TrimSpace(args))
	}
	if n <= len(commands)+len(customs) {
		c := customs[n-len(commands)-1]
		hint := c.ArgumentHint
		if hint == "" {
			hint = "or Enter for none"
		}
		args, ok := r.readInput(fmt.Sprintf("Arguments for /%s (%s): ", c.Name, hint))
		if !ok {
			return errExit
		}
		return r.invokeCommand(ctx, c.Name, strings.TrimSpace(args))
	}
	s := skills[n-len(commands)-len(customs)-1]
	task, ok := r.readInput("Task for the skill (or Enter to just apply it): ")
	if !ok {
		return errExit
	}
	return r.invokeSkill(ctx, s.Name, strings.TrimSpace(task))
}

// invokeSkill explicitly loads a skill and runs the agent with its full
// instructions plus the user's task. Unlike the model-initiated use_skill
// tool, this is the user forcing a skill, so the instructions are embedded
// directly in the user message.
func (r *Repl) invokeSkill(ctx context.Context, name, task string) error {
	s, err := r.Skills.Load(name)
	if err != nil {
		return err
	}
	if task == "" {
		task = "Apply this skill to the current project now."
	}
	task, err = ExpandFileRefs(task, r.WorkDir)
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("Follow the instructions of skill %q (directory: %s):\n\n%s\n\n---\nTask: %s",
		s.Name, s.Dir, s.Body, task)
	_, err = r.Agent.Run(ctx, msg)
	r.saveSession("/" + name + " " + task)
	if err != nil {
		return err
	}
	return r.checkGoal(ctx)
}

// cmdResume lists recorded sessions and swaps the chosen one in, replaying
// a transcript tail so the user regains context — the Claude Code /resume
// experience adapted to this UI.
func (r *Repl) cmdResume(_ context.Context, args string) error {
	if r.Sessions == nil {
		return fmt.Errorf("session persistence is disabled")
	}
	if args != "" {
		return r.resume(args)
	}

	metas, err := r.Sessions.List()
	if err != nil {
		return err
	}
	// Offer everything except the session we are already in.
	var others []session.Meta
	for _, m := range metas {
		if r.current == nil || m.ID != r.current.ID {
			others = append(others, m)
		}
	}
	if len(others) == 0 {
		return fmt.Errorf("no previous sessions in this project")
	}

	// Arrow-navigable overlay inside the full-screen TUI: ↑↓ to move, type to
	// search, Enter to select.
	if r.tuiSelect != nil {
		labels := sessionLabels(others, r.terminalWidth()-4)
		items := make([]pickerItem, len(others))
		for i, m := range others {
			items[i] = pickerItem{label: labels[i], filterText: labels[i] + " " + m.ID}
		}
		idx, ok := r.tuiSelect("Resume a session:", items)
		if !ok {
			return nil
		}
		return r.resume(others[idx].ID)
	}
	// Legacy nested picker (only reached in the old inline TTY path).
	if r.useTUI {
		labels := sessionLabels(others, r.terminalWidth()-4)
		items := make([]pickerItem, len(others))
		for i, m := range others {
			items[i] = pickerItem{
				label:      labels[i],
				filterText: labels[i] + " " + m.ID,
			}
		}
		idx, ok, err := r.pickInteractive("Resume a session:", items)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return r.resume(others[idx].ID)
	}

	fmt.Fprintln(r.Out, "Sessions:")
	labels := sessionLabels(others, r.terminalWidth()-8)
	for i := range others {
		fmt.Fprintf(r.Out, "  %2d. %s\n", i+1, labels[i])
	}
	line, ok := r.readInput("Select a session (Enter to cancel): ")
	if !ok {
		return errExit
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return nil
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(others) {
		return fmt.Errorf("invalid selection %q", choice)
	}
	return r.resume(others[n-1].ID)
}

// orDefault returns s, or def when s is empty.
func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// terminalWidth reports the output terminal's width, falling back to a
// sane default when output is piped (scripts, tests).
func (r *Repl) terminalWidth() int {
	if f, ok := r.Out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 100
}

// sessionLabels lays out one row per session as: relative time, model, then
// the title.
//
// Key flow: the title goes last on purpose. It is the only field holding
// arbitrary user text — CJK, emoji, and the "…" elision marker — and some
// terminals disagree with the Unicode tables on how wide those are
// (ambiguous-width characters render as one column in a Latin locale and
// two in a CJK one). Any such disagreement can only shift what follows, so
// keeping the variable-width field last means the aligned columns cannot
// drift. Time and model are ASCII, whose width is unambiguous everywhere.
func sessionLabels(metas []session.Meta, avail int) []string {
	const timeWidth = 9
	if avail < 40 {
		avail = 40
	}

	// The model column is as wide as its widest entry, capped so a long
	// model name cannot squeeze out the title.
	modelWidth := 0
	for _, m := range metas {
		if w := textwidth.Width(m.Model); w > modelWidth {
			modelWidth = w
		}
	}
	if limit := avail / 3; modelWidth > limit {
		modelWidth = limit
	}

	titleWidth := avail - timeWidth - modelWidth - 2 // two single-space gaps
	if titleWidth < 12 {
		titleWidth = 12
	}

	// The trailing title is truncated but not padded: trailing blanks would
	// buy nothing, and padding it would reintroduce the width guesswork
	// this layout exists to avoid.
	labels := make([]string, len(metas))
	for i, m := range metas {
		labels[i] = textwidth.Pad(relativeTime(m.UpdatedAt), timeWidth) + " " +
			textwidth.Pad(m.Model, modelWidth) + " " +
			textwidth.Truncate(m.Title, titleWidth)
	}
	return labels
}

// cmdRename retitles the current session. With no argument it shows the
// current title and prompts for a new one.
//
// Key flow: a rename before the first turn is remembered and applied when
// the session file is created, so a session can be named up front; after
// that the new title is written through immediately, and the auto-derived
// title never overwrites it because titles are only generated at creation.
func (r *Repl) cmdRename(_ context.Context, args string) error {
	if r.Sessions == nil {
		return fmt.Errorf("session persistence is disabled")
	}
	title := CleanTitle(args)
	if title == "" {
		fmt.Fprintf(r.Out, "Current title: %s\n", r.sessionTitle())
		line, ok := r.readInput("New title (Enter to cancel): ")
		if !ok {
			return errExit
		}
		if title = CleanTitle(line); title == "" {
			fmt.Fprintln(r.Out, "Cancelled.")
			return nil
		}
	}

	if r.current == nil {
		// No turn has been recorded yet; hold the name until there is a
		// session to attach it to.
		r.pendingTitle = title
		fmt.Fprintf(r.Out, "Title set to %q — it applies to this session once it starts.\n", title)
		return nil
	}
	r.current.Title = title
	if err := r.Sessions.Save(r.current); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "Renamed session %s to %q\n", r.current.ID, title)
	return nil
}

// sessionTitle reports the title in effect, including one chosen before the
// session was created.
func (r *Repl) sessionTitle() string {
	switch {
	case r.current != nil && r.current.Title != "":
		return r.current.Title
	case r.pendingTitle != "":
		return r.pendingTitle
	default:
		return "(untitled — derived from your first message)"
	}
}

// CleanTitle normalizes a user-supplied title to a single line of bounded
// display width, matching how auto-derived titles are stored. It is shared
// with the `agent session rename` subcommand so both paths agree.
func CleanTitle(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return textwidth.Truncate(strings.TrimSpace(line), 60)
}

// resume loads a session by ID (or unique prefix) into the agent and
// replays it.
func (r *Repl) resume(id string) error {
	sess, err := r.Sessions.Load(id)
	if err != nil {
		return err
	}
	r.Agent.Restore(sess.ProviderMessages())
	r.current = sess
	// Continue raw-input bookkeeping from the recorded displays so later
	// turns keep aligning with user messages. Synthetic compaction summaries
	// are not user input and carry no raw text, so they are skipped — keeping
	// rawInputs a list of real typed inputs.
	r.rawInputs = r.rawInputs[:0]
	for _, rec := range sess.Messages {
		if rec.Role != provider.RoleUser || agent.IsSummaryMessage(rec.Message) {
			continue
		}
		if rec.Display != "" {
			r.rawInputs = append(r.rawInputs, rec.Display)
		} else {
			r.rawInputs = append(r.rawInputs, rec.Content)
		}
	}
	r.goal = sess.Goal
	fmt.Fprintf(r.Out, "Resumed session %s — %q (%d messages)\n", sess.ID, sess.Title, len(sess.Messages))
	r.replayTranscript(sess.Messages)
	if r.goal != "" {
		fmt.Fprintf(r.Out, "Active goal restored: %s (re-checked on your next message)\n", r.goal)
	}
	return nil
}

// replayTranscript re-renders the conversation through the same Events
// pipeline the live loop uses, so resumed history looks exactly like it did
// when it happened: "> input" lines, assistant text, and tool calls with
// their status dots and result previews.
func (r *Repl) replayTranscript(records []session.Record) {
	ev := r.Agent.Events
	if ev == nil {
		return
	}
	for i, rec := range records {
		switch rec.Role {
		case provider.RoleUser:
			display := rec.Display
			if display == "" {
				display = rec.Content
			}
			ev.OnUserPrompt(display)
		case provider.RoleAssistant:
			// Mirror the live loop's ordering: text first, then tools.
			if rec.Content != "" {
				ev.OnAssistantText(rec.Content)
			}
			for _, call := range rec.ToolCalls {
				ev.OnToolCall(call.Function.Name, call.Function.Arguments)
				result, ok := findToolResult(records[i+1:], call.ID)
				ev.OnToolResult(call.Function.Name, result, ok)
			}
		}
	}
}

// findToolResult locates the recorded result for a tool call ID. Success is
// inferred from the persisted result text, which the registry prefixes with
// "Error: " on failure.
func findToolResult(rest []session.Record, callID string) (string, bool) {
	for _, rec := range rest {
		if rec.Role == provider.RoleTool && rec.ToolCallID == callID {
			return rec.Content, !strings.HasPrefix(rec.Content, "Error:")
		}
	}
	return "(no result recorded)", false
}

// relativeTime renders "5m ago"-style timestamps for session listings.
func relativeTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// --- built-in command handlers ---------------------------------------------

func (r *Repl) cmdHelp(_ context.Context, _ string) error {
	avail := r.terminalWidth() - 2
	rows := make([][2]string, len(commands))
	for i, c := range commands {
		rows[i] = [2]string{c.usage, c.desc}
	}
	textwidth.WriteList(r.Out, rows, avail, 2)

	skills, _ := r.Skills.List()
	if len(skills) > 0 {
		fmt.Fprintln(r.Out, "\nSkills (run with /<name> [task]):")
		srows := make([][2]string, len(skills))
		for i, s := range skills {
			srows[i] = [2]string{"/" + s.Name, s.Description}
		}
		textwidth.WriteList(r.Out, srows, avail, 1)
	}
	fmt.Fprintln(r.Out, "\nReference files with @path anywhere in a prompt, e.g. \"explain @cmd/agent/main.go\".")
	return nil
}

func (r *Repl) cmdModel(_ context.Context, args string) error {
	if args == "" {
		fmt.Fprintf(r.Out, "model: %s (provider %s)\n", r.Cfg.Model, r.Cfg.Provider)
		if models := catalog.ModelsFor(r.Cfg.Provider); len(models) > 0 {
			fmt.Fprintf(r.Out, "known models: %s\n", strings.Join(models, ", "))
			fmt.Fprintln(r.Out, "(any model the provider accepts works — this list is only a hint)")
		}
		return nil
	}
	r.Cfg.Model = args
	r.Agent.SetModel(args)
	fmt.Fprintf(r.Out, "Switched model to %s\n", args)
	r.stripImagesIfNeeded()
	return nil
}

// cmdProvider rebuilds the provider client and re-resolves credentials for
// the new vendor, keeping conversation history intact.
func (r *Repl) cmdProvider(_ context.Context, args string) error {
	if args == "" {
		return r.listProviders()
	}
	name, model, _ := strings.Cut(args, " ")
	model = strings.TrimSpace(model)

	cfg, err := config.LoadFor(name)
	if err != nil {
		return err
	}
	if model != "" {
		cfg.Model = model
	}
	if cfg.Model == "" {
		return fmt.Errorf("no default model for provider %q; use /provider %s <model>", name, name)
	}
	p, err := cfg.BuildProvider()
	if err != nil {
		// A missing credential is recoverable: prompt for the key (masked)
		// instead of failing, then retry — and offer to save it.
		if cfg.APIKey != "" {
			return err
		}
		key, ok := r.promptProviderKey(name)
		if !ok {
			fmt.Fprintln(r.Out, "Provider switch cancelled.")
			return nil
		}
		cfg.APIKey = key
		if p, err = cfg.BuildProvider(); err != nil {
			return err
		}
		r.offerSaveProviderKey(name, key)
	}
	r.Agent.SetProvider(p, cfg.Model)
	r.Cfg = cfg
	fmt.Fprintf(r.Out, "Switched to provider=%s model=%s (history preserved)\n", name, cfg.Model)
	r.stripImagesIfNeeded()
	return nil
}

// promptProviderKey asks for a provider's API key (masked), including where to
// get one when the catalog knows.
func (r *Repl) promptProviderKey(name string) (string, bool) {
	prompt := fmt.Sprintf("Enter API key for %s (Enter to cancel):", name)
	if p, ok := catalog.Lookup(name); ok && p.Notes != "" {
		prompt = fmt.Sprintf("Enter API key for %s — get one at %s (Enter to cancel):", name, p.Notes)
	}
	key, ok := r.readSecret(prompt)
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return "", false
	}
	return key, true
}

// offerSaveProviderKey asks whether to persist the just-entered key so the
// provider reconnects without prompting next time.
func (r *Repl) offerSaveProviderKey(name, key string) {
	ans, ok := r.readInput(fmt.Sprintf("Save this key for %s to config? [Y/n]:", name))
	if ok && strings.EqualFold(strings.TrimSpace(ans), "n") {
		return
	}
	if err := config.SaveProviderKey(config.ScopeGlobal, "", name, key); err != nil {
		fmt.Fprintln(r.Out, "warning: could not save key:", err)
		return
	}
	path, _ := config.Path()
	fmt.Fprintf(r.Out, "Saved key for %s to %s\n", name, path)
}

// listProviders prints the two provider sources separately and without
// duplication: the user's own config profiles first, then the built-in
// catalog with any preset a profile overrides removed. This is what keeps a
// user profile named like a preset (e.g. "glm", "deepseek") from appearing
// twice — the profile wins and the preset is hidden.
func (r *Repl) listProviders() error {
	fmt.Fprintf(r.Out, "Current: %s (model %s)\n\n", r.Cfg.Provider, r.Cfg.Model)
	avail := r.terminalWidth() - 2

	if len(r.Cfg.Providers) > 0 {
		fmt.Fprintln(r.Out, "Your providers (from config):")
		names := make([]string, 0, len(r.Cfg.Providers))
		for name := range r.Cfg.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := make([][2]string, 0, len(names))
		for _, name := range names {
			p := r.Cfg.Providers[name]
			desc := fmt.Sprintf("%s · %s · %s", orDefault(p.Format, "openai"),
				orDefault(p.Model, "(default)"), p.BaseURL)
			if _, isPreset := catalog.Lookup(name); isPreset {
				desc += " · overrides built-in"
			}
			rows = append(rows, [2]string{name, desc})
		}
		textwidth.WriteList(r.Out, rows, avail, 1)
		fmt.Fprintln(r.Out)
	}

	fmt.Fprintln(r.Out, "Built-in providers — /provider <name> [model]:")
	rows := make([][2]string, 0, len(catalog.All()))
	for _, p := range catalog.All() {
		// A profile of the same name shadows this preset entirely; don't
		// list it twice.
		if _, overridden := r.Cfg.Providers[p.Name]; overridden {
			continue
		}
		// Name the credential variable actually supplying the key (or the
		// first expected one) — a missing key is the usual switch failure.
		cred := p.EnvKeys[0] + " unset"
		for _, k := range p.EnvKeys {
			if os.Getenv(k) != "" {
				cred = k + " ✓"
				break
			}
		}
		desc := fmt.Sprintf("%s · %s · %s", p.Label, p.DefaultModel, cred)
		if p.Notes != "" {
			desc += " · " + p.Notes
		}
		rows = append(rows, [2]string{p.Name, desc})
	}
	textwidth.WriteList(r.Out, rows, avail, 1)
	return nil
}

func (r *Repl) cmdSkills(_ context.Context, _ string) error {
	skills, err := r.Skills.List()
	if err != nil {
		return err
	}
	if len(skills) == 0 {
		fmt.Fprintln(r.Out, "No skills installed. Install with: agent skill install <dir-or-git-url>")
		return nil
	}
	rows := make([][2]string, len(skills))
	for i, s := range skills {
		rows[i] = [2]string{s.Name, s.Description}
	}
	textwidth.WriteList(r.Out, rows, r.terminalWidth()-2, 2)
	fmt.Fprintf(r.Out, "\n%d skill(s). Run \"agent skill show <name>\" for the full instructions.\n", len(skills))
	return nil
}

// cmdCommands lists user-defined slash commands and where to add more.
func (r *Repl) cmdCommands(_ context.Context, _ string) error {
	var cmds []usercmd.Command
	if r.Commands != nil {
		cmds, _ = r.Commands.List()
	}
	if len(cmds) == 0 {
		fmt.Fprintf(r.Out, "No custom commands. Add one as a markdown file in %s "+
			"(personal) or .agent/commands (this project), then run it with /<name>.\n",
			home.Path("commands"))
		return nil
	}
	rows := make([][2]string, len(cmds))
	for i, c := range cmds {
		name := "/" + c.Name
		if c.ArgumentHint != "" {
			name += " " + c.ArgumentHint
		}
		origin := "user"
		if c.Project {
			origin = "project"
		}
		rows[i] = [2]string{name, fmt.Sprintf("%s (%s)", c.Description, origin)}
	}
	textwidth.WriteList(r.Out, rows, r.terminalWidth()-2, 2)
	fmt.Fprintf(r.Out, "\n%d custom command(s). Use $ARGUMENTS or $1,$2… in the body for arguments.\n", len(cmds))
	return nil
}

// cmdLSP lists the language servers backing the code-navigation tools, showing
// which are installed and which are currently running.
func (r *Repl) cmdLSP(_ context.Context, _ string) error {
	if r.LSP == nil {
		fmt.Fprintln(r.Out, "Language servers are not configured.")
		return nil
	}
	statuses := r.LSP.Status()
	fmt.Fprintln(r.Out, "Language servers (tools: lsp_diagnostics, lsp_references, lsp_definition, lsp_hover):")
	rows := make([][2]string, 0, len(statuses))
	for _, s := range statuses {
		state := "not installed"
		switch {
		case s.Disabled:
			state = "disabled"
		case s.Running:
			state = "running"
		case s.Available:
			state = "ready"
		}
		left := fmt.Sprintf("%s (%s)", s.Lang, s.Command)
		right := fmt.Sprintf("%s — %s", state, strings.Join(s.Extensions, " "))
		rows = append(rows, [2]string{left, right})
	}
	textwidth.WriteList(r.Out, rows, r.terminalWidth()-2, 1)
	fmt.Fprintln(r.Out, "\nServers start on first use. Add or override one with the \"lspServers\" config key.")
	return nil
}

func (r *Repl) cmdTools(_ context.Context, _ string) error {
	tools := r.Tools.All()
	rows := make([][2]string, len(tools))
	for i, t := range tools {
		rows[i] = [2]string{t.Name(), t.Description()}
	}
	textwidth.WriteList(r.Out, rows, r.terminalWidth()-2, 2)
	return nil
}

// cmdTodos shows the agent's current task list (maintained via the todo_write
// tool), so the user can see the plan and progress at a glance.
func (r *Repl) cmdTodos(_ context.Context, _ string) error {
	if tl, ok := r.Tools.Get("todo_write"); ok {
		if tw, ok := tl.(*tool.TodoWrite); ok {
			fmt.Fprintln(r.Out, tool.RenderTodos(tw.Items()))
			return nil
		}
	}
	fmt.Fprintln(r.Out, "No task list yet — the agent creates one with todo_write for multi-step tasks.")
	return nil
}

// cmdMCP lists the configured Model Context Protocol servers, their transport,
// connection status, and the tools each contributed.
func (r *Repl) cmdMCP(_ context.Context, _ string) error {
	if r.MCP == nil || len(r.MCP.Status) == 0 {
		fmt.Fprintln(r.Out, "No MCP servers configured. Add them under \"mcpServers\" in config.json.")
		return nil
	}
	for _, s := range r.MCP.Status {
		if s.OK() {
			fmt.Fprintf(r.Out, "● %s (%s) — %d tool(s)\n", s.Name, s.Transport, len(s.Tools))
			for _, name := range s.Tools {
				fmt.Fprintf(r.Out, "    %s\n", name)
			}
		} else {
			fmt.Fprintf(r.Out, "✗ %s (%s) — %v\n", s.Name, s.Transport, s.Err)
		}
	}
	return nil
}

// cmdAgents lists the subagent types the "task" tool can delegate to, so the
// user can see what parallel delegation targets are available.
func (r *Repl) cmdAgents(_ context.Context, _ string) error {
	if r.Spawner == nil {
		fmt.Fprintln(r.Out, "Task delegation is not available in this session.")
		return nil
	}
	types := r.Spawner.Types()
	rows := make([][2]string, len(types))
	for i, d := range types {
		rows[i] = [2]string{d.Name, d.Description}
	}
	fmt.Fprintln(r.Out, "Subagent types (delegate with the task tool; issue several in one turn to run in parallel):")
	textwidth.WriteList(r.Out, rows, r.terminalWidth()-2, 2)
	return nil
}

// cmdConfig shows the resolved configuration, opens the interactive editor
// (/config edit), or sets one value directly (/config set <key> <value>
// [global|project|session]).
// cmdConfig opens the settings panel, which both shows the current values
// and edits them — there is no separate read-only view. "set" remains for
// scripting a single value with an explicit scope.
func (r *Repl) cmdConfig(ctx context.Context, args string) error {
	fields := strings.Fields(args)
	switch {
	case len(fields) == 0:
		return r.configEdit(ctx)
	case len(fields) >= 3 && fields[0] == "set":
		scope := "g"
		if len(fields) >= 4 {
			scope = fields[3]
		}
		return r.applySetting(ctx, fields[1], fields[2], scope)
	default:
		return fmt.Errorf("usage: /config | /config set <key> <value> [global|project|session]")
	}
}

func (r *Repl) cmdMemory(_ context.Context, _ string) error {
	entries, err := r.Memory.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(r.Out, "No project memories saved yet.")
		return nil
	}
	for _, e := range entries {
		first, _, _ := strings.Cut(e.Content, "\n")
		fmt.Fprintf(r.Out, "%-24s %s\n", e.Name, first)
	}
	return nil
}

// cmdNew starts a fresh session: the conversation resets and the next turn
// is recorded under a new session ID, while the previous session remains
// resumable. /clear is the same operation under its traditional name.
func (r *Repl) cmdNew(_ context.Context, _ string) error {
	r.Agent.Reset()
	r.current = nil
	r.rawInputs = nil
	r.pendingTitle = ""
	r.goal = "" // goals are session-scoped
	fmt.Fprintln(r.Out, "Started a new session. Use /resume to return to a previous one.")
	return nil
}

func (r *Repl) cmdClear(_ context.Context, _ string) error {
	r.Agent.Reset()
	// Detach from the recorded session: the next turn starts a fresh one,
	// while the old session stays resumable.
	r.current = nil
	r.rawInputs = nil
	r.pendingTitle = ""
	r.goal = "" // goals are session-scoped
	fmt.Fprintln(r.Out, "Conversation cleared (previous session remains available via /resume).")
	return nil
}

// cmdCompact summarizes earlier conversation turns on demand, freeing context
// while keeping recent turns verbatim. The same mechanism runs automatically
// when context nears the configured limit (auto_compact).
func (r *Repl) cmdCompact(ctx context.Context, _ string) error {
	stats, err := r.Agent.Compact(ctx)
	if err != nil {
		return err
	}
	suffix := "s"
	if stats.SummarizedMessages == 1 {
		suffix = ""
	}
	fmt.Fprintf(r.Out, "Compacted: summarized %d earlier message%s into ~%d chars (%d → %d messages). Recent turns kept in full.\n",
		stats.SummarizedMessages, suffix,
		stats.SummaryChars, stats.MessagesBefore, stats.MessagesAfter)
	return nil
}

// cmdUsage reports cumulative token/time consumption and the current
// context occupancy (estimated from the latest request's usage).
// usageRow is one aligned row of the /usage tables.
type usageRow struct{ name, tok, in, out, cost string }

func (r *Repl) cmdUsage(_ context.Context, _ string) error {
	const (
		bold  = "\033[1m"
		dim   = "\033[2m"
		reset = "\033[0m"
		cyan  = "\033[36m"
	)
	rec := r.Agent.Usage

	if rec != nil {
		if in, out, reqs, dur, cost, priced := rec.Totals(); in+out > 0 {
			fmt.Fprintf(r.Out, "\n%s%sUsage%s %s· this project · all time%s\n\n", bold, cyan, reset, dim, reset)

			// Label/value summary block, labels padded to a common width.
			lbl := func(k, v string) {
				fmt.Fprintf(r.Out, "  %s   %s\n", textwidth.Pad(k, 11), v)
			}
			lbl("Total cost", bold+money(cost, priced)+reset)
			lbl("Tokens", fmt.Sprintf("%s  %s(%s in · %s out)%s", abbrevTok(in+out), dim, abbrevTok(in), abbrevTok(out), reset))
			lbl("Requests", strconv.Itoa(reqs))
			lbl("Model time", dur.Round(time.Second).String())

			// Build aligned rows for both breakdowns.
			rows := func(es []usage.Entry, nameOf func(usage.Entry) string) []usageRow {
				out := make([]usageRow, len(es))
				for i, e := range es {
					out[i] = usageRow{nameOf(e), abbrevTok(e.Tokens()), abbrevTok(e.Input), abbrevTok(e.Output), money(e.Cost, e.Priced)}
				}
				return out
			}
			models := rec.ByModel()
			r.printUsageTable("By model", rows(models, func(e usage.Entry) string { return e.Model }))
			r.printUsageTable("By provider", rows(rec.ByProvider(), func(e usage.Entry) string { return e.Provider }))

			// List any unpriced models with a copy-pasteable config hint, so
			// "cost is —" is actionable rather than mysterious.
			var unpriced []string
			for _, e := range models {
				if !e.Priced {
					unpriced = append(unpriced, e.Model)
				}
			}
			if len(unpriced) > 0 {
				fmt.Fprintf(r.Out, "\n  %sno price found (models.dev/config) for: %s%s\n", dim, strings.Join(unpriced, ", "), reset)
				fmt.Fprintf(r.Out, "  %sset it in config.json to see cost, e.g.:%s\n", dim, reset)
				fmt.Fprintf(r.Out, "  %s  \"prices\": { %q: { \"input\": 0.5, \"output\": 1.5 } }%s\n", dim, unpriced[0], reset)
			} else {
				fmt.Fprintf(r.Out, "\n  %scost from models.dev prices (config \"prices\" overrides as needed)%s\n", dim, reset)
			}
		}
	}

	// Current session snapshot.
	tokens, dur, ctxTokens := r.Agent.Stats()
	ctxNote := abbrevTok(ctxTokens)
	if ctxTokens == 0 {
		ctxNote = "unknown until the next reply"
	}
	fmt.Fprintf(r.Out, "\n  %sThis session%s   %s tok · %s · context %s\n",
		dim, reset, abbrevTok(tokens), dur.Round(100*time.Millisecond), ctxNote)
	return nil
}

// printUsageTable renders a titled, column-aligned usage breakdown:
//
//	<name>   <tok> tok   <in> → <out>   <cost>
func (r *Repl) printUsageTable(title string, rows []usageRow) {
	const dim, reset = "\033[2m", "\033[0m"
	if len(rows) == 0 {
		return
	}
	var nameW, tokW, inW, outW, costW int
	for _, row := range rows {
		nameW = max(nameW, textwidth.Width(row.name))
		tokW = max(tokW, textwidth.Width(row.tok))
		inW = max(inW, textwidth.Width(row.in))
		outW = max(outW, textwidth.Width(row.out))
		costW = max(costW, textwidth.Width(row.cost))
	}
	fmt.Fprintf(r.Out, "\n  %s%s%s\n", dim, title, reset)
	for _, row := range rows {
		fmt.Fprintf(r.Out, "    %s   %s tok   %s %s→%s %s   %s\n",
			textwidth.Pad(row.name, nameW),
			textwidth.PadLeft(row.tok, tokW),
			textwidth.PadLeft(row.in, inW),
			dim, reset,
			textwidth.PadLeft(row.out, outW),
			textwidth.PadLeft(row.cost, costW),
		)
	}
}

// abbrevTok renders a token count compactly (1.2k, 3.4m), like Claude Code.
func abbrevTok(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

// money renders an estimated cost; an unpriced entry shows "—", and a
// partially-priced subtotal flags the estimate with "+". Small amounts get
// more decimals so cheap usage doesn't collapse to "$0.00".
func money(cost float64, priced bool) string {
	if cost == 0 && !priced {
		return "—"
	}
	var s string
	switch {
	case cost > 0 && cost < 1:
		s = fmt.Sprintf("$%.4f", cost)
	default:
		s = fmt.Sprintf("$%.2f", cost)
	}
	if !priced {
		s += "+"
	}
	return s
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (r *Repl) cmdExit(_ context.Context, _ string) error { return errExit }
