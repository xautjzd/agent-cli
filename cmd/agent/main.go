// Command agent is a Claude Code-style coding agent CLI.
//
// Usage:
//
//	agent                       start an interactive session
//	agent -p "prompt"           run one prompt and exit
//	agent skill list|install|remove|show ...
//	agent memory list|show|delete ...
//	agent session list|show|rename|delete ...
//	agent config [init|show]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/home"
	"github.com/xautjzd/agent-cli/internal/hook"
	"github.com/xautjzd/agent-cli/internal/mcp"
	"github.com/xautjzd/agent-cli/internal/memory"
	"github.com/xautjzd/agent-cli/internal/permission"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/repl"
	"github.com/xautjzd/agent-cli/internal/sandbox"
	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/skill"
	"github.com/xautjzd/agent-cli/internal/subagent"
	"github.com/xautjzd/agent-cli/internal/textwidth"
	"github.com/xautjzd/agent-cli/internal/usage"
	"github.com/xautjzd/agent-cli/internal/webtool"

	"github.com/xautjzd/agent-cli/internal/tool"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Subcommand dispatch happens before flag parsing so subcommands can
	// define their own flags.
	if len(args) > 0 {
		switch args[0] {
		case "skill":
			return runSkill(args[1:])
		case "memory":
			return runMemory(args[1:])
		case "session":
			return runSession(args[1:])
		case "config":
			return runConfig(args[1:])
		case "version":
			fmt.Println("agent-cli 0.1.0")
			return nil
		}
	}

	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	prompt := fs.String("p", "", "run a single prompt non-interactively and exit (\"-\" reads the prompt from stdin)")
	providerFlag := fs.String("provider", "", "override provider (anthropic, openai, deepseek, custom)")
	modelFlag := fs.String("model", "", "override model")
	bypass := fs.Bool("bypass", false, "permission bypass mode: no confirmations, dangerous operations are audit-logged")
	output := fs.String("output", "text", "non-interactive output format: text or json")
	quiet := fs.Bool("q", false, "non-interactive: print only the final answer (suppress tool activity)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *providerFlag != "" {
		// LoadFor re-targets the whole configuration at the requested
		// vendor — default model, endpoint and credentials — instead of
		// leaving settings bound to the previous provider behind.
		if cfg, err = config.LoadFor(*providerFlag); err != nil {
			return err
		}
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}
	if cfg.Model == "" {
		return fmt.Errorf("no model configured for provider %q; pass -model or set AGENT_MODEL", cfg.Provider)
	}

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	sess, err := buildSession(cfg, workDir)
	if sess != nil {
		// Terminate MCP child processes and connections on exit.
		defer sess.MCP.Close()
	}
	if err != nil {
		return err
	}
	if *bypass {
		sess.Mode = permission.ModeBypass
	}

	// Ctrl-C cancels the in-flight turn instead of killing the process
	// abruptly mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *prompt != "" {
		return runNonInteractive(ctx, sess, cfg, *prompt, *output, *quiet, *bypass)
	}
	return sess.Run(ctx)
}

// runNonInteractive executes one prompt and exits — the mode for PR review, CI,
// and GitHub Actions. It never waits for a human (dangerous ops are denied
// unless -bypass), reads the prompt or extra context from stdin, and can emit
// machine-readable JSON.
//
// Usage patterns:
//
//	agent -p "review the staged diff; list bugs and security issues"
//	git diff origin/main | agent -p "review this diff" -q
//	echo "summarize @CHANGELOG.md" | agent -p -
//	agent -p "audit @config.go" -output json | jq .result
func runNonInteractive(ctx context.Context, sess *repl.Repl, cfg *config.Config, prompt, format string, quiet, bypass bool) error {
	// Resolve the prompt: "-" reads it entirely from stdin; a normal prompt
	// with piped stdin appends that stdin as context (e.g. a diff).
	text := prompt
	if prompt == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(b)
	} else if !isTTY(os.Stdin) {
		if b, _ := io.ReadAll(os.Stdin); len(bytes.TrimSpace(b)) > 0 {
			text += "\n\n--- input from stdin ---\n" + string(b)
		}
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty prompt")
	}

	// No human is present: bypass auto-approves (audited), otherwise dangerous
	// operations are denied rather than blocking.
	if bypass {
		sess.Mode = permission.ModeBypass
	} else {
		sess.NonInteractive = true
	}

	jsonMode := format == "json"
	if format != "text" && format != "json" {
		return fmt.Errorf("unknown -output %q (use text or json)", format)
	}
	// Keep stdout for the result only: the gate's diagnostics go to stderr.
	sess.Out = os.Stderr
	sink := &ciSink{out: os.Stdout, errw: os.Stderr, quiet: quiet, silent: jsonMode}
	sess.Agent.Events = sink

	expanded, err := repl.ExpandFileRefs(text, sess.WorkDir)
	if err != nil {
		return err
	}
	start := time.Now()
	answer, runErr := sess.Agent.Run(ctx, expanded)

	if jsonMode {
		res := map[string]any{
			"result":           answer,
			"provider":         cfg.Provider,
			"model":            cfg.Model,
			"input_tokens":     sink.stats.PromptTokens,
			"output_tokens":    sink.stats.CompletionTokens,
			"rounds":           sink.stats.Rounds,
			"duration_seconds": round2(time.Since(start).Seconds()),
		}
		if c, ok := usage.Cost(cfg.Provider, cfg.Model, sink.stats.PromptTokens, sink.stats.CompletionTokens); ok {
			res["cost_usd"] = round2(c)
		}
		if runErr != nil {
			res["error"] = runErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	}
	return runErr
}

func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// isTTY reports whether f is an interactive terminal.
func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// ciSink renders agent activity for non-interactive runs: the final answer goes
// to stdout (so it is cleanly pipeable), tool activity to stderr (so it stays
// out of the captured result), and nothing at all in quiet mode. In JSON mode
// the answer is withheld from stdout (returned in the JSON object instead).
type ciSink struct {
	out      io.Writer // stdout
	errw     io.Writer // stderr
	quiet    bool      // suppress tool activity
	silent   bool      // JSON mode: don't stream the answer to stdout
	stats    agent.TurnStats
	streamed bool
}

func (s *ciSink) answerW() io.Writer {
	if s.silent {
		return io.Discard
	}
	return s.out
}
func (s *ciSink) toolW() io.Writer {
	if s.quiet || s.silent {
		return io.Discard
	}
	return s.errw
}

func (s *ciSink) OnUserPrompt(string)      {}
func (s *ciSink) OnThinking(string)        {}
func (s *ciSink) OnAssistantText(t string) { fmt.Fprintln(s.answerW(), t) }
func (s *ciSink) OnToolCall(name, args string) {
	fmt.Fprintf(s.toolW(), "● %s(%s)\n", camelName(name), compactArgs(args))
}
func (s *ciSink) OnToolResult(name, result string, ok bool) {
	mark := "✓"
	if !ok {
		mark = "✗"
	}
	fmt.Fprintf(s.toolW(), "  %s %s\n", mark, truncateOneLine(result, 200))
}
func (s *ciSink) OnTurnStats(st agent.TurnStats) { s.stats = st }

// Streaming: write answer fragments live to stdout (unless JSON/silent).
func (s *ciSink) OnThinkingDelta(string)    {}
func (s *ciSink) OnAssistantDelta(t string) { s.streamed = true; fmt.Fprint(s.answerW(), t) }
func (s *ciSink) OnStreamEnd() {
	if s.streamed && !s.silent {
		fmt.Fprintln(s.out)
	}
	s.streamed = false
}

// buildSession wires all dependencies together. This is the composition
// root: the only place where concrete types meet the interfaces they
// satisfy.
func buildSession(cfg *config.Config, workDir string) (*repl.Repl, error) {
	p, err := cfg.BuildProvider()
	if err != nil {
		return nil, err
	}

	// Move any personal scratch data left in the working tree by older
	// versions out to the user-level store (sessions migrate inside
	// NewProjectStore; pasted images migrate here).
	repl.MigratePastes(workDir)

	skillRepo := &skill.FSRepository{Roots: skill.DefaultRoots(workDir)}
	memStore := memory.NewProjectStore(workDir)

	// Optional command sandboxing (defense in depth beneath the permission
	// gate): confines bash commands' writes to the project when a backend is
	// available.
	sbox := sandbox.New(sandbox.Options{Mode: cfg.Sandbox, DenyNetwork: cfg.SandboxDenyNetwork})
	if cfg.Sandbox == "on" && !sbox.Available() {
		fmt.Fprintf(os.Stderr, "warning: sandbox requested but unavailable — %s\n", sbox.Reason())
	}

	// Web tools: web_search finds current docs/APIs/errors, web_fetch reads a
	// page. The search backend is keyless DuckDuckGo by default; Brave/Tavily
	// via config. web_fetch's optional prompt is answered by distilling the
	// fetched page with the model (Claude Code style).
	searcher := webtool.NewSearcher(cfg.WebSearch.Provider, cfg.WebSearchKey(), nil)
	extract := func(ctx context.Context, prompt, content string) (string, error) {
		resp, err := p.Chat(ctx, provider.Request{
			Model: cfg.Model,
			Messages: []provider.Message{
				{Role: provider.RoleSystem, Content: "You extract the parts of a fetched web page that are relevant to the user's request. Quote exact code, signatures, values, and version numbers verbatim. Be concise; if nothing is relevant, say so."},
				{Role: provider.RoleUser, Content: "Request: " + prompt + "\n\n" + content},
			},
		})
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	}

	// buildBaseTools produces one fresh set of the built-in tools. It is used
	// both for the main registry and to give each subagent its own isolated
	// tools — it deliberately excludes the "task" tool so a subagent cannot
	// spawn further subagents (bounding delegation depth to one).
	buildBaseTools := func() []tool.Tool {
		return []tool.Tool{
			&tool.Bash{WorkDir: workDir, Sandbox: sbox, DenyNetwork: cfg.SandboxDenyNetwork},
			&tool.ReadFile{WorkDir: workDir},
			&tool.WriteFile{WorkDir: workDir},
			&tool.EditFile{WorkDir: workDir},
			&tool.Glob{WorkDir: workDir},
			&tool.Grep{WorkDir: workDir},
			&tool.ListDir{WorkDir: workDir},
			&tool.UseSkill{Repo: skillRepo},
			&tool.Remember{Store: memStore},
			&tool.ForgetMemory{Store: memStore},
			&webtool.WebSearch{Searcher: searcher},
			&webtool.WebFetch{Extract: extract},
			&tool.TodoWrite{},
		}
	}

	registry := tool.NewRegistry(buildBaseTools()...)

	// Wire task delegation: the "task" tool spawns isolated subagents (own
	// context + Task-free tools) and the agent core runs multiple task calls
	// in one turn concurrently, giving parallel sub-task processing.
	spawner := &subagent.Spawner{
		Provider:    p,
		Model:       cfg.Model,
		BuildTools:  buildBaseTools,
		Definitions: subagentDefs(cfg),
		MaxTurns:    cfg.MaxTurns,
	}
	registry.Register(&subagent.Task{Spawner: spawner})

	systemPrompt := (&agent.PromptBuilder{
		WorkDir: workDir,
		Skills:  skillRepo,
		Memory:  memStore,
	}).Build()

	// Connect any configured MCP servers and merge their tools into the
	// registry before the agent's system prompt is built, so MCP tools are
	// part of the advertised tool set. Servers that fail to connect are
	// recorded on the manager (see /mcp) but never block startup.
	mcpMgr := mcp.Connect(context.Background(), cfg.MCPServers, registry)
	for _, s := range mcpMgr.Status {
		if !s.OK() {
			fmt.Fprintf(os.Stderr, "warning: MCP server %q failed: %v\n", s.Name, s.Err)
		}
	}

	// Cross-session usage/cost tracking, shared with subagents so delegated
	// work counts toward the project totals. Prices come from models.dev
	// (loaded from cache instantly, refreshed in the background), with
	// config-supplied prices as a backstop and a built-in table for offline.
	usage.InitModelsDev(home.Path("models-dev-prices.json"))
	if len(cfg.Prices) > 0 {
		reg := make(map[string]usage.Price, len(cfg.Prices))
		for model, p := range cfg.Prices {
			reg[model] = usage.Price{InputPerM: p.Input, OutputPerM: p.Output}
		}
		usage.RegisterPrices(reg)
	}
	usageRec := usage.NewRecorder(session.UsagePath(workDir))
	spawner.Usage = usageRec

	ag := agent.New(p, cfg.Model, registry, systemPrompt, newTerminalEvents(), cfg.MaxTurns)
	ag.AutoCompact = cfg.AutoCompact != "off"
	ag.ContextLimit = cfg.ContextLimit
	ag.Usage = usageRec
	// Supply today's date as a small note after the (byte-stable) system
	// prompt, so time-sensitive answers are correct without churning the
	// cacheable prefix.
	ag.Now = time.Now
	// Subagents inherit the parent's current provider/model, so a mid-session
	// /provider or /model switch applies to delegated work too.
	spawner.Parent = ag
	r := &repl.Repl{
		Agent:         ag,
		Cfg:           cfg,
		Skills:        skillRepo,
		Memory:        memStore,
		Tools:         registry,
		MCP:           mcpMgr,
		Spawner:       spawner,
		Policy:        buildPolicy(cfg, workDir),
		Audit:         permission.NewAuditLogger(session.AuditLogPath(workDir)),
		SandboxActive: sbox.Available(),
		Hooks:         buildHooks(cfg),
		Sessions:      session.NewProjectStore(workDir),
		WorkDir:       workDir,
		In:            os.Stdin,
		Out:           os.Stdout,
	}
	// The REPL adapts the hook runner to the agent's PreToolUse/PostToolUse
	// extension points.
	ag.Hooks = r
	if sbox.Available() {
		fmt.Fprintf(os.Stderr, "\033[2m● sandbox: %s\033[0m\n", sbox.Reason())
	}
	// The REPL is the permission gate: dangerous tool calls are confirmed
	// (HITL) or audit-logged (bypass) before execution — for the main agent
	// and, through the spawner, for subagents too.
	ag.Gate = r
	spawner.Gate = r
	// Config-file behavior settings take effect at startup.
	if cfg.PermissionMode != "" {
		r.Mode = permission.Mode(cfg.PermissionMode)
	}
	r.GoalMaxRounds = cfg.GoalMaxRounds
	return r, nil
}

// buildHooks assembles the lifecycle hook runner from config. Invalid hooks
// are reported to stderr but do not abort startup.
func buildHooks(cfg *config.Config) *hook.Runner {
	var hooks []hook.Hook
	for event, hcs := range cfg.Hooks {
		for _, hc := range hcs {
			hooks = append(hooks, hook.Hook{
				Event:   hook.Event(event),
				Matcher: hc.Matcher,
				Command: hc.Command,
				Timeout: time.Duration(hc.TimeoutSeconds) * time.Second,
			})
		}
	}
	runner, errs := hook.New(hooks)
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warning: invalid hook: %v\n", err)
	}
	return runner
}

// buildPolicy assembles the permission policy from config: the bash posture
// plus any user-defined approval rules. An invalid rule is reported to stderr
// but does not abort startup.
func buildPolicy(cfg *config.Config, workDir string) *permission.Policy {
	posture := permission.PostureStandard
	if cfg.BashPolicy == string(permission.PostureStrict) {
		posture = permission.PostureStrict
	}
	rules := make([]permission.Rule, 0, len(cfg.PermissionRules))
	for _, pr := range cfg.PermissionRules {
		rules = append(rules, permission.Rule{
			Tool:    pr.Tool,
			Command: pr.Command,
			Path:    pr.Path,
			Action:  permission.Action(pr.Action),
		})
	}
	pol, errs := permission.NewPolicy(posture, rules)
	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warning: invalid permission rule: %v\n", err)
	}
	return pol
}

// subagentDefs builds the subagent type table: the built-in general-purpose
// type plus any custom types declared under "subagents" in the config.
func subagentDefs(cfg *config.Config) map[string]subagent.Definition {
	defs := map[string]subagent.Definition{
		subagent.DefaultDefinition.Name: subagent.DefaultDefinition,
	}
	for name, sc := range cfg.Subagents {
		defs[name] = subagent.Definition{
			Name:        name,
			Description: sc.Description,
			Prompt:      sc.Prompt,
			Tools:       sc.Tools,
		}
	}
	return defs
}

// terminalWidth reports the output width for list rendering, falling back
// to a sane default when stdout is not a terminal.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w - 2
	}
	return 98
}

// ANSI fragments used by the terminal renderer.
const (
	ansiReset  = "\033[0m"
	ansiDim    = "\033[2m"
	ansiItalic = "\033[3m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
)

// terminalEvents renders agent activity to the terminal in the Claude Code
// idiom: "● ToolName(args)" headers whose dot color reflects status, "⎿"
// result previews, and visually distinct thinking output.
type terminalEvents struct {
	// out is the render target (stdout in production, a buffer in tests).
	out io.Writer
	// lastCall is the rendered "ToolName(args)" of the in-flight call, kept
	// so the result handler can rewrite the header with the outcome color.
	lastCall string
	// streamState tracks what a streamed completion is currently emitting:
	// 0 = nothing yet, 1 = thinking, 2 = answer text.
	streamState int
}

func newTerminalEvents() *terminalEvents { return &terminalEvents{out: os.Stdout} }

// OnThinkingDelta opens the thinking style on the first fragment and prints
// fragments as they arrive, visually identical to the non-streamed block.
func (e *terminalEvents) OnThinkingDelta(text string) {
	if e.streamState != 1 {
		fmt.Fprintf(e.out, "\n%s%s✻ Thinking…%s\n%s%s", ansiDim, ansiItalic, ansiReset, ansiDim, ansiItalic)
		e.streamState = 1
	}
	fmt.Fprint(e.out, text)
}

// OnAssistantDelta switches from thinking to answer styling when needed and
// prints answer fragments live.
func (e *terminalEvents) OnAssistantDelta(text string) {
	if e.streamState == 1 {
		fmt.Fprint(e.out, ansiReset+"\n")
	}
	if e.streamState != 2 {
		fmt.Fprint(e.out, "\n")
		e.streamState = 2
	}
	fmt.Fprint(e.out, text)
}

// OnStreamEnd closes styling and spacing after a streamed completion.
func (e *terminalEvents) OnStreamEnd() {
	if e.streamState == 1 {
		fmt.Fprint(e.out, ansiReset)
	}
	if e.streamState != 0 {
		fmt.Fprintln(e.out)
	}
	e.streamState = 0
}

// OnUserPrompt re-renders a user input line during transcript replay, in
// the same collapsed "❯ input" form a submitted editor line leaves in
// scrollback.
func (e *terminalEvents) OnUserPrompt(text string) {
	fmt.Fprintf(e.out, "\n\033[36m❯\033[0m %s\n", text)
}

// OnThinking renders chain-of-thought dim and italic under a "✻ Thinking"
// header, clearly separated from the final answer.
func (e *terminalEvents) OnThinking(text string) {
	fmt.Fprintf(e.out, "\n%s%s✻ Thinking…%s\n", ansiDim, ansiItalic, ansiReset)
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fmt.Fprintf(e.out, "%s%s  %s%s\n", ansiDim, ansiItalic, line, ansiReset)
	}
}

func (e *terminalEvents) OnAssistantText(text string) {
	fmt.Fprintln(e.out, "\n"+text)
}

// OnToolCall prints the header with a yellow "running" dot. Nothing else
// writes to the terminal until OnToolResult, which repaints the dot.
func (e *terminalEvents) OnToolCall(name, args string) {
	e.lastCall = fmt.Sprintf("%s(%s)", camelName(name), compactArgs(args))
	fmt.Fprintf(e.out, "\n%s●%s %s\n", ansiYellow, ansiReset, e.lastCall)
}

// OnToolResult moves the cursor back onto the header line, repaints the dot
// green (success) or red (failure), then prints a dim result preview.
func (e *terminalEvents) OnToolResult(name, result string, ok bool) {
	dot := ansiGreen + "●" + ansiReset
	if !ok {
		dot = ansiRed + "●" + ansiReset
	}
	// \033[1A: up one line; \r: to column 0; \033[2K: erase the line.
	fmt.Fprintf(e.out, "\033[1A\r\033[2K%s %s\n", dot, e.lastCall)

	lines := strings.Split(result, "\n")
	// The todo list is meant to be read in full — show every line.
	if name == "todo_write" {
		for _, line := range strings.Split(strings.TrimRight(result, "\n"), "\n") {
			fmt.Fprintf(e.out, "  %s\n", line)
		}
		return
	}
	// File edits report a unified diff; render it in full and colorized
	// rather than collapsing it to a one-line preview.
	if body := diffBody(lines); body != nil {
		fmt.Fprintf(e.out, "  %s⎿ %s%s\n", ansiDim, lines[0], ansiReset)
		e.renderDiff(body)
		return
	}

	preview := lines[0]
	if len(preview) > 160 {
		preview = preview[:160] + "…"
	}
	if len(lines) > 1 {
		preview += fmt.Sprintf(" (+%d lines)", len(lines)-1)
	}
	fmt.Fprintf(e.out, "  %s⎿ %s%s\n", ansiDim, preview, ansiReset)
}

// diffLineRe matches a rendered unified-diff line: right-aligned line number,
// a space, then the ' ', '-' or '+' marker.
var diffLineRe = regexp.MustCompile(`^\s*\d+ [ +-] `)

// maxDiffLines bounds how much of a large diff is printed.
const maxDiffLines = 40

// diffBody returns the diff portion of a tool result, or nil when the result
// is not a diff. A result qualifies when it has a summary header followed by
// at least one numbered diff line.
func diffBody(lines []string) []string {
	if len(lines) < 2 {
		return nil
	}
	for _, l := range lines[1:] {
		if diffLineRe.MatchString(l) {
			return lines[1:]
		}
	}
	return nil
}

// renderDiff prints diff lines with additions in green, removals in red and
// context dimmed, indented under the result marker.
func (e *terminalEvents) renderDiff(body []string) {
	shown := body
	if len(shown) > maxDiffLines {
		shown = shown[:maxDiffLines]
	}
	for _, l := range shown {
		color := ansiDim
		if m := diffLineRe.FindString(l); m != "" {
			switch m[len(m)-2] {
			case '+':
				color = ansiGreen
			case '-':
				color = ansiRed
			}
		}
		fmt.Fprintf(e.out, "    %s%s%s\n", color, l, ansiReset)
	}
	if n := len(body) - len(shown); n > 0 {
		fmt.Fprintf(e.out, "    %s… %d more diff line(s)%s\n", ansiDim, n, ansiReset)
	}
}

// OnTurnStats prints a dim one-line summary after each turn: elapsed time,
// this turn's token split, the current context occupancy, and session
// totals.
func (e *terminalEvents) OnTurnStats(s agent.TurnStats) {
	fmt.Fprintf(e.out, "%s⏱ %s · turn: %s in + %s out (%d round%s) · context: %s tok · session: %s tok, %s%s\n",
		ansiDim,
		formatDuration(s.Duration),
		formatTokens(s.PromptTokens), formatTokens(s.CompletionTokens),
		s.Rounds, plural(s.Rounds),
		formatTokens(s.ContextTokens),
		formatTokens(s.SessionTokens), formatDuration(s.SessionDuration),
		ansiReset)
}

// OnCompaction prints a dim notice when the conversation is compacted, so the
// user understands why earlier turns are no longer verbatim in context.
func (e *terminalEvents) OnCompaction(s agent.CompactionStats) {
	fmt.Fprintf(e.out, "%s⊙ Compacted context (%s): summarized %d earlier message%s into ~%s chars; %d → %d messages%s\n",
		ansiDim,
		s.Trigger,
		s.SummarizedMessages, plural(s.SummarizedMessages),
		formatTokens(s.SummaryChars),
		s.MessagesBefore, s.MessagesAfter,
		ansiReset)
}

// formatTokens renders counts with thousands separators (12,345).
func formatTokens(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// formatDuration renders durations at human granularity (4.2s, 1m03s).
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// camelName converts a snake_case tool name to CamelCase for display, e.g.
// "read_file" → "ReadFile".
func camelName(name string) string {
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// compactArgs renders tool arguments one-line and short, preferring the
// primary argument (command, path, pattern, name) over the full JSON blob.
func compactArgs(args string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil || len(m) == 0 {
		return truncateOneLine(args, 70)
	}
	for _, key := range []string{"command", "path", "pattern", "name"} {
		if v, ok := m[key].(string); ok {
			return truncateOneLine(v, 70)
		}
	}
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s: %v", k, v))
	}
	sort.Strings(parts)
	return truncateOneLine(strings.Join(parts, ", "), 70)
}

func truncateOneLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// --- skill subcommand -------------------------------------------------------

func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent skill <list|install|remove|show> [args]")
	}
	workDir, _ := os.Getwd()
	repo := &skill.FSRepository{Roots: skill.DefaultRoots(workDir)}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	installer := &skill.Installer{TargetDir: filepath.Join(home, ".agent", "skills")}

	switch args[0] {
	case "list":
		skills, err := repo.List()
		if err != nil {
			return err
		}
		if len(skills) == 0 {
			fmt.Println("No skills installed. Install one with: agent skill install <dir-or-git-url>")
			return nil
		}
		rows := make([][2]string, len(skills))
		for i, s := range skills {
			rows[i] = [2]string{s.Name, s.Description}
		}
		textwidth.WriteList(os.Stdout, rows, terminalWidth(), 2)
		return nil
	case "install":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent skill install <local-dir-or-git-url>")
		}
		names, err := installer.Install(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Installed: %s\n", strings.Join(names, ", "))
		return nil
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent skill remove <name>")
		}
		if err := installer.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("Removed %s\n", args[1])
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent skill show <name>")
		}
		s, err := repo.Load(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("name: %s\ndescription: %s\ndirectory: %s\n\n%s\n", s.Name, s.Description, s.Dir, s.Body)
		return nil
	}
	return fmt.Errorf("unknown skill command %q", args[0])
}

// --- memory subcommand ------------------------------------------------------

func runMemory(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent memory <list|show|delete> [args]")
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	store := memory.NewProjectStore(workDir)

	switch args[0] {
	case "list":
		entries, err := store.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No project memories saved yet.")
			return nil
		}
		rows := make([][2]string, len(entries))
		for i, e := range entries {
			first, _, _ := strings.Cut(e.Content, "\n")
			rows[i] = [2]string{e.Name, first}
		}
		textwidth.WriteList(os.Stdout, rows, terminalWidth(), 1)
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent memory show <name>")
		}
		e, err := store.Get(args[1])
		if err != nil {
			return err
		}
		fmt.Println(e.Content)
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent memory delete <name>")
		}
		if err := store.Delete(args[1]); err != nil {
			return err
		}
		fmt.Printf("Deleted %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown memory command %q", args[0])
}

// --- session subcommand -----------------------------------------------------

// runSession makes recorded sessions traceable from the shell: list them,
// dump a full transcript (including tool calls and results), or delete one.
func runSession(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agent session <list|show|rename|delete> [id]")
	}
	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	store := session.NewProjectStore(workDir)

	switch args[0] {
	case "list":
		metas, err := store.List()
		if err != nil {
			return err
		}
		if len(metas) == 0 {
			fmt.Println("No sessions recorded in this project yet.")
			return nil
		}
		// Fixed-width ASCII columns first, then the free-form title — the
		// same ordering the interactive picker uses, so a CJK title cannot
		// push later columns out of alignment.
		// Cap the model column: a single malformed entry (a URL recorded
		// as a model name, say) must not blow out every other row.
		modelWidth := 0
		for _, m := range metas {
			if w := textwidth.Width(m.Model); w > modelWidth {
				modelWidth = w
			}
		}
		if limit := terminalWidth() / 4; modelWidth > limit {
			modelWidth = limit
		}
		for _, m := range metas {
			fmt.Printf("%-22s %s %s %s\n",
				m.ID,
				m.UpdatedAt.Format("2006-01-02 15:04"),
				textwidth.Pad(m.Model, modelWidth),
				textwidth.Truncate(m.Title, terminalWidth()-24-17-modelWidth))
		}
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent session show <id>")
		}
		sess, err := store.Load(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("session: %s\ntitle: %s\nprovider: %s\nmodel: %s\ncreated: %s\n\n",
			sess.ID, sess.Title, sess.Provider, sess.Model, sess.CreatedAt.Format(time.RFC3339))
		for _, m := range sess.Messages {
			switch {
			case m.Role == provider.RoleUser:
				fmt.Printf("❯ user:\n%s\n\n", m.Content)
			case m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0:
				for _, c := range m.ToolCalls {
					fmt.Printf("● %s %s\n", camelName(c.Function.Name), c.Function.Arguments)
				}
				if m.Content != "" {
					fmt.Printf("● assistant:\n%s\n", m.Content)
				}
				fmt.Println()
			case m.Role == provider.RoleAssistant:
				fmt.Printf("● assistant:\n%s\n\n", m.Content)
			case m.Role == provider.RoleTool:
				fmt.Printf("  ⎿ %s\n\n", truncateOneLine(m.Content, 200))
			}
		}
		return nil
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("usage: agent session rename <id> <new title>")
		}
		sess, err := store.Load(args[1])
		if err != nil {
			return err
		}
		// Remaining words form the title, so quoting is optional.
		title := repl.CleanTitle(strings.Join(args[2:], " "))
		if title == "" {
			return fmt.Errorf("title must not be empty")
		}
		old := sess.Title
		sess.Title = title
		if err := store.Save(sess); err != nil {
			return err
		}
		fmt.Printf("Renamed %s\n  from: %s\n    to: %s\n", sess.ID, old, title)
		return nil
	case "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: agent session delete <id>")
		}
		if err := store.Delete(args[1]); err != nil {
			return err
		}
		fmt.Printf("Deleted session %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown session command %q", args[0])
}

// --- config subcommand ------------------------------------------------------

func runConfig(args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "show":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		key := "(not set)"
		if cfg.APIKey != "" {
			key = "****" + cfg.APIKey[max(0, len(cfg.APIKey)-4):]
		}
		path, _ := config.Path()
		wd, _ := os.Getwd()
		fmt.Printf("global config: %s\nproject config: %s\nprovider: %s\nmodel: %s\nbase_url: %s\napi_key: %s\nmax_turns: %d\npermission_mode: %s\ngoal_max_rounds: %d\n",
			path, config.ProjectPath(wd), cfg.Provider, cfg.Model,
			orDefault(cfg.BaseURL, "(provider default)"), key, cfg.MaxTurns,
			orDefault(cfg.PermissionMode, "hitl"), orDefaultInt(cfg.GoalMaxRounds, 8))
		if len(cfg.Providers) > 0 {
			fmt.Println("provider profiles:")
			for name, p := range cfg.Providers {
				fmt.Printf("  %-12s %s (model: %s)\n", name, p.BaseURL, orDefault(p.Model, "-"))
			}
		}
		return nil
	case "init":
		cfg := &config.Config{Provider: "deepseek", Model: "deepseek-chat", MaxTurns: 40}
		if err := cfg.Save(); err != nil {
			return err
		}
		path, _ := config.Path()
		fmt.Printf("Wrote %s — edit it or set AGENT_API_KEY / DEEPSEEK_API_KEY / OPENAI_API_KEY.\n", path)
		return nil
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: agent config set <key> <value> [project]  (keys: %v)", config.Keys())
		}
		scope := config.ScopeGlobal
		dir := ""
		if len(args) >= 4 && args[3] == "project" {
			scope = config.ScopeProject
			dir, _ = os.Getwd()
		}
		if err := config.SetScoped(scope, dir, args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("Set %s = %s (%s)\n", args[1], args[2], scope)
		return nil
	}
	return fmt.Errorf("unknown config command %q (use show|init|set)", sub)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orDefaultInt(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}
