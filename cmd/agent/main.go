// Command agent is a Claude Code-style coding agent CLI.
//
// Usage:
//
//	agent                       start an interactive session
//	agent -p "prompt"           run one prompt and exit
//	agent config show|init|set <key> <value> [project]
//	agent provider list|use <name> [model] [--anthropic|--openai]
//	agent session list|show|rename|delete ...
//	agent skill list|install|remove|show ...
//	agent memory list|show|delete ...
//	agent uninstall [--purge] [--yes]
//	agent version
//
// "agent -h" prints the same list (see printUsage) plus every flag, and
// "agent <command> -h" explains a single command.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/agent"
	"github.com/xautjzd/agent-cli/internal/catalog"
	"github.com/xautjzd/agent-cli/internal/checkpoint"
	"github.com/xautjzd/agent-cli/internal/command"
	"github.com/xautjzd/agent-cli/internal/config"
	"github.com/xautjzd/agent-cli/internal/home"
	"github.com/xautjzd/agent-cli/internal/hook"
	"github.com/xautjzd/agent-cli/internal/log"
	"github.com/xautjzd/agent-cli/internal/lsp"
	"github.com/xautjzd/agent-cli/internal/mcp"
	"github.com/xautjzd/agent-cli/internal/mdstream"
	"github.com/xautjzd/agent-cli/internal/memory"
	"github.com/xautjzd/agent-cli/internal/permission"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/repl"
	"github.com/xautjzd/agent-cli/internal/sandbox"
	"github.com/xautjzd/agent-cli/internal/session"
	"github.com/xautjzd/agent-cli/internal/skill"
	"github.com/xautjzd/agent-cli/internal/subagent"
	"github.com/xautjzd/agent-cli/internal/textwidth"
	"github.com/xautjzd/agent-cli/internal/theme"
	"github.com/xautjzd/agent-cli/internal/uninstall"
	"github.com/xautjzd/agent-cli/internal/usage"
	"github.com/xautjzd/agent-cli/internal/version"
	"github.com/xautjzd/agent-cli/internal/webtool"

	"github.com/xautjzd/agent-cli/internal/tool"
	"github.com/xautjzd/agent-cli/internal/update"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// subcommand documents one command dispatched before flag parsing. Grammar is
// the one-liner "agent -h" shows; actions are the individual forms that
// "agent <name> -h" explains.
type subcommand struct {
	name    string
	grammar string
	actions [][2]string
}

// subcommands is the single source of truth for both help levels: a command
// added to the switch in run() must be listed here or it stays invisible to
// "agent -h" (TestUsageCoversEverySubcommandAndFlag enforces this).
var subcommands = []subcommand{
	{"config", "show | init | set <key> <value> [project]", [][2]string{
		{"show", "print the resolved configuration and where it came from"},
		{"init", "write a starter global config file"},
		{"set <key> <value> [project]", "set one key globally, or in this project's config"},
	}},
	{"provider", "list | use <name> [model] | add <name> --base-url <url> | remove <name>", [][2]string{
		{"list", "list config profiles and built-in presets, with credential status"},
		{"use <name> [model]", "persist a provider (and model) as the default"},
		{"  --anthropic / --openai", "pick the wire for a vendor that serves both"},
		{"add [<name> --base-url <url>]", "define a custom provider; asks for each field when given no flags"},
		{"  --model <id>", "the model it serves"},
		{"  --anthropic / --openai", "which API style the endpoint speaks (default openai)"},
		{"  --api-key <key>", "store the key; omit it to read $<NAME>_API_KEY instead"},
		{"remove <name>", "delete a custom provider"},
	}},
	{"session", "list | show <id> | rename <id> <title> | delete <id>", [][2]string{
		{"list", "list this project's recorded sessions"},
		{"show <id>", "print a full transcript, including tool calls and results"},
		{"rename <id> <title>", "retitle a session (quoting optional)"},
		{"delete <id>", "delete a session"},
	}},
	{"skill", "list | install <dir-or-git-url> | show <name> | remove <name>", [][2]string{
		{"list", "list installed skills"},
		{"install <dir-or-git-url>", "install a skill from a directory or git repository"},
		{"show <name>", "print a skill's metadata and body"},
		{"remove <name>", "uninstall a skill"},
	}},
	{"memory", "list | show <name> | delete <name>", [][2]string{
		{"list", "list this project's saved memories"},
		{"show <name>", "print one memory"},
		{"delete <name>", "delete one memory"},
	}},
	{"uninstall", "[--purge] [--yes]", [][2]string{
		{"--purge", "also remove config.json and the projects cache"},
		{"--yes", "confirm without prompting"},
	}},
	{"version", "print the version and exit", nil},
}

// helpRequested reports whether a subcommand's arguments ask for its help
// rather than an action, so every command answers "-h" the way the top level
// does instead of rejecting it as an unknown action.
func helpRequested(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// printSubcommandUsage prints the help for one subcommand: its grammar and
// every action it accepts.
func printSubcommandUsage(w io.Writer, name string) {
	for _, c := range subcommands {
		if c.name != name {
			continue
		}
		// A command with no actions (version) has no grammar to spell out, so
		// its one-liner becomes the description instead of a usage suffix.
		if len(c.actions) == 0 {
			fmt.Fprintf(w, "Usage: agent %s\n\n  %s\n", c.name, c.grammar)
			return
		}
		fmt.Fprintf(w, "Usage: agent %s %s\n\n", c.name, c.grammar)
		width := 0
		for _, a := range c.actions {
			if len(a[0]) > width {
				width = len(a[0])
			}
		}
		for _, a := range c.actions {
			fmt.Fprintf(w, "  %-*s  %s\n", width, a[0], a[1])
		}
		return
	}
}

// printUsage renders the full help: invocation forms, subcommands, then the
// flags. Go's default usage prints flags only, which hid every subcommand.
// Everything goes to the flag set's own output so the sections cannot end up
// split across two streams (PrintDefaults always writes there).
func printUsage(fs *flag.FlagSet) {
	w := fs.Output()
	fmt.Fprint(w, `agent — an agentic coding CLI

Usage:
  agent [flags]                    start an interactive session
  agent -p "<prompt>" [flags]      run one prompt, print the answer, exit
  agent <command> [args]           run a command (no flags; see below)

Commands:
`)
	for _, c := range subcommands {
		fmt.Fprintf(w, "  %-9s %s\n", c.name, c.grammar)
	}
	fmt.Fprint(w, "\n\"agent <command> -h\" explains one command.\n")
	fmt.Fprint(w, "\nFlags:\n")
	fs.PrintDefaults()
	fmt.Fprintf(w, "\nConfig keys for \"agent config set\":\n  %s\n", strings.Join(config.Keys(), ", "))
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
		case "provider":
			return runProvider(args[1:])
		case "uninstall":
			return runUninstall(args[1:])
		case "version":
			if helpRequested(args[1:]) {
				printSubcommandUsage(os.Stdout, "version")
				return nil
			}
			fmt.Println("agent-cli " + version.Version)
			return nil
		}
	}

	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.Usage = func() { printUsage(fs) }
	prompt := fs.String("p", "", "run a single prompt non-interactively and exit (\"-\" reads the prompt from stdin)")
	// The preset names come from the catalog so this line cannot drift as
	// vendors are added or renamed.
	providerFlag := fs.String("provider", "", "override provider: a config profile or a preset ("+strings.Join(catalog.Names(), ", ")+")")
	formatFlag := fs.String("format", "", "wire format for vendors serving both: openai (default) or anthropic")
	modelFlag := fs.String("model", "", "override model")
	bypass := fs.Bool("bypass", false, "permission bypass mode: no confirmations, dangerous operations are audit-logged")
	output := fs.String("output", "text", "non-interactive output format: text or json")
	quiet := fs.Bool("q", false, "non-interactive: print only the final answer (suppress tool activity)")
	if err := fs.Parse(args); err != nil {
		// "-h" is a request, not a failure: flag has already printed the
		// usage, so exit cleanly instead of appending an "error:" line.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.Debug("main", "config loaded: provider=%s model=%s", cfg.Provider, cfg.Model)
	if *providerFlag != "" {
		// LoadFor re-targets the whole configuration at the requested
		// vendor — default model, endpoint and credentials — instead of
		// leaving settings bound to the previous provider behind.
		if cfg, err = config.LoadForWire(*providerFlag, *formatFlag); err != nil {
			return err
		}
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}
	// A missing model is only fatal where nobody can fix it: an interactive
	// session opens and /provider picks both provider and model.
	if cfg.Model == "" && *prompt != "" {
		if cfg.Provider == "" {
			return fmt.Errorf("no provider configured: run \"agent provider use <name>\" (see \"agent provider list\"), or pass -provider")
		}
		return fmt.Errorf("no model configured for provider %q; pass -model or set AGENT_MODEL", cfg.Provider)
	}
	// Apply the configured color theme before anything renders.
	if cfg.Theme != "" {
		theme.Set(cfg.Theme)
	}

	if shouldCheckForUpdate(*prompt == "", isTTY(os.Stdin), isTTY(os.Stdout), os.Getenv("AGENT_NO_UPDATE_CHECK"), version.Version) {
		proceed := handleStartupUpdate(
			context.Background(),
			version.Version,
			update.NewChecker(),
			update.NewInstaller(),
			update.Prompt,
			os.Stdin,
			os.Stdout,
		)
		if !proceed {
			return nil
		}
	}

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	log.Info("main", "startup: agent-cli %s, workDir=%s, provider=%s, model=%s", version.Version, workDir, cfg.Provider, cfg.Model)
	sess, err := buildSession(cfg, workDir, *prompt == "")
	if sess != nil {
		// Terminate MCP child processes and connections on exit.
		defer sess.MCP.Close()
		// Shut down any language servers started during the session.
		defer sess.LSP.Close()
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

func runUninstall(args []string) error {
	if helpRequested(args) {
		printSubcommandUsage(os.Stdout, "uninstall")
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	return uninstall.Run(
		args,
		os.Stdin,
		os.Stdout,
		executable,
		home.Dir(),
		isTTY(os.Stdin),
		version.Version,
	)
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
	log.Info("main", "non-interactive: provider=%s model=%s format=%s", cfg.Provider, cfg.Model, format)
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

type releaseChecker interface {
	Latest(context.Context, string) (update.Release, bool, error)
}

type releaseInstaller interface {
	Install(context.Context, update.Release) error
}

type updatePrompt func(io.Reader, io.Writer, string, update.Release) (update.Action, error)

func shouldCheckForUpdate(interactive, stdinTTY, stdoutTTY bool, disabled, current string) bool {
	if !interactive || !stdinTTY || !stdoutTTY || disabled == "1" {
		return false
	}
	// Latest treats non-semver builds as development builds without accessing
	// the network. Keep this cheap gate explicit so tests and startup behavior
	// do not depend on an HTTP client being constructed.
	return current != "" && current != "dev"
}

// handleStartupUpdate returns whether startup should continue. Lookup failures
// are intentionally non-fatal; explicit Exit and a successful replacement end
// the current process before any session resources are constructed.
func handleStartupUpdate(
	ctx context.Context,
	current string,
	checker releaseChecker,
	installer releaseInstaller,
	prompt updatePrompt,
	in io.Reader,
	out io.Writer,
) bool {
	release, available, err := checker.Latest(ctx, current)
	if err != nil || !available {
		if err != nil {
			log.Debug("update", "latest release check failed: %v", err)
		}
		return true
	}
	action, err := prompt(in, out, current, release)
	if err != nil {
		log.Debug("update", "update prompt failed: %v", err)
		return true
	}
	switch action {
	case update.ActionSkip:
		return true
	case update.ActionExit:
		return false
	case update.ActionUpdate:
		fmt.Fprintf(out, "Downloading and verifying agent-cli %s…\n", release.Version)
		if err := installer.Install(ctx, release); err != nil {
			fmt.Fprintf(out, "Update failed: %v\n", err)
			fmt.Fprintf(out, "Manual update:\n  %s\n\n", update.ManualCommand(release.Version))
			return true
		}
		fmt.Fprintf(out, "✓ Updated agent-cli %s → %s. Run agent again to use the new version.\n", current, release.Version)
		return false
	default:
		return true
	}
}

// countOK counts how many MCP server statuses report a successful connection.
func countOK(statuses []mcp.ServerStatus) int {
	n := 0
	for _, s := range statuses {
		if s.OK() {
			n++
		}
	}
	return n
}

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
// buildSession assembles the session. interactive marks a run that has a
// human at the keyboard: those start even when the provider cannot be built,
// because /provider inside the session is how a missing key gets fixed. A
// one-shot run has nobody to fix it, so it fails outright.
func buildSession(cfg *config.Config, workDir string, interactive bool) (*repl.Repl, error) {
	p, err := cfg.BuildProvider()
	if err != nil {
		if !interactive {
			return nil, err
		}
		if cfg.Provider == "" {
			log.Warn("main", "no provider configured — run /provider to pick one")
			fmt.Fprintln(os.Stderr, "No provider configured yet — run /provider to pick one (it prompts for the API key and saves it).")
		} else {
			log.Warn("main", "provider %s build failed: %v", cfg.Provider, err)
			fmt.Fprintf(os.Stderr, "warning: %v\n         set one with /provider %s, or export the variable and restart.\n",
				err, cfg.Provider)
		}
		p = provider.Unconfigured(err)
	}

	// Move any personal scratch data left in the working tree by older
	// versions out to the user-level store (sessions migrate inside
	// NewProjectStore; pasted images migrate here).
	repl.MigratePastes(workDir)

	skillRepo := &skill.FSRepository{Roots: skill.DefaultRoots(workDir)}
	cmdRepo := command.NewRepository(workDir)
	memStore := memory.NewProjectStore(workDir)

	// Optional command sandboxing (defense in depth beneath the permission
	// gate): confines bash commands' writes to the project when a backend is
	// available.
	sbox := sandbox.New(sandbox.Options{Mode: cfg.Sandbox, DenyNetwork: cfg.SandboxDenyNetwork})
	if cfg.Sandbox == "on" && !sbox.Available() {
		log.Warn("main", "sandbox requested but unavailable: %s", sbox.Reason())
		fmt.Fprintf(os.Stderr, "warning: sandbox requested but unavailable — %s\n", sbox.Reason())
	}

	// Web tools: web_search finds current docs/APIs/errors, web_fetch reads a
	// page. The search backend is keyless DuckDuckGo by default; other engines
	// (Bing, Bing CN, Baidu, Yahoo) and the API-key ones (Brave, Tavily) come
	// from config. It is wrapped so /config can switch engines live: the same
	// value goes into every subagent's tool set, so one swap reaches them all.
	// web_fetch's optional prompt is answered by distilling the fetched page
	// with the model (Claude Code style).
	searcher := webtool.NewSwitchable(webtool.NewSearcher(cfg.WebSearch.Provider, cfg.WebSearchCredentials(), nil))
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
	// Checkpoints capture the working tree before each turn's file edits so
	// /rewind can undo them. The same manager is shared with subagents (their
	// tools are built by this closure too), so delegated edits are undoable as
	// well.
	checkpoints := checkpoint.NewManager()
	// Language servers back the code-navigation tools; shared across the main
	// agent and subagents. Servers start lazily on first use.
	lspMgr := lsp.NewManager(workDir, lspOverrides(cfg))
	buildBaseTools := func() []tool.Tool {
		tools := []tool.Tool{
			&tool.Bash{WorkDir: workDir, Sandbox: sbox, DenyNetwork: cfg.SandboxDenyNetwork},
			&tool.ReadFile{WorkDir: workDir},
			&tool.WriteFile{WorkDir: workDir, Snapshot: checkpoints},
			&tool.EditFile{WorkDir: workDir, Snapshot: checkpoints},
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
		return append(tools, lsp.Tools(lspMgr, workDir)...)
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

	// Connect any configured MCP servers and merge their tools into the registry
	// before the system prompt is built, so the deferred-tool catalog is part of
	// the (cacheable) prompt. MCP tools register as deferred: advertised by name
	// only and loaded on demand via search_tools. Servers that fail to connect
	// are recorded on the manager (see /mcp) but never block startup.
	mcpMgr := mcp.Connect(context.Background(), cfg.MCPServers, registry)
	for _, s := range mcpMgr.Status {
		if !s.OK() {
			log.Warn("main", "MCP server %q failed: %v", s.Name, s.Err)
			fmt.Fprintf(os.Stderr, "warning: MCP server %q failed: %v\n", s.Name, s.Err)
		}
	}
	log.Info("main", "session wired: %d tools, %d MCP servers connected", len(registry.All()), countOK(mcpMgr.Status))

	// Enable deferred tool loading when tools were actually deferred (MCP tools
	// present) and it is not disabled. The search_tools meta-tool is a core tool
	// so the model can always reach it; nil activation keeps the historical
	// "advertise every tool" behavior for sessions with no MCP tools.
	var activation *tool.Activation
	if cfg.LazyTools != "off" && len(registry.Deferred()) > 0 {
		activation = &tool.Activation{}
		registry.Register(&tool.SearchTools{Registry: registry, Activation: activation})
	}

	systemPrompt := (&agent.PromptBuilder{
		WorkDir: workDir,
		Skills:  skillRepo,
		Memory:  memStore,
		Tools:   registry,
	}).Build()

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
	ag.Activation = activation
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
		Commands:      cmdRepo,
		Memory:        memStore,
		Tools:         registry,
		MCP:           mcpMgr,
		Spawner:       spawner,
		Search:        searcher,
		Policy:        buildPolicy(cfg, workDir),
		Audit:         permission.NewAuditLogger(session.AuditLogPath(workDir)),
		SandboxActive: sbox.Available(),
		Hooks:         buildHooks(cfg),
		Sessions:      session.NewProjectStore(workDir),
		Checkpoints:   checkpoints,
		LSP:           lspMgr,
		WorkDir:       workDir,
		In:            os.Stdin,
		Out:           os.Stdout,
	}
	// The REPL adapts the hook runner to the agent's PreToolUse/PostToolUse
	// extension points.
	ag.Hooks = r
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
		log.Warn("main", "invalid hook: %v", err)
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
		log.Warn("main", "invalid permission rule: %v", err)
		fmt.Fprintf(os.Stderr, "warning: invalid permission rule: %v\n", err)
	}
	return pol
}

// subagentDefs builds the subagent type table: the built-in general-purpose
// type plus any custom types declared under "subagents" in the config.
// lspOverrides translates the configured language servers into the lsp
// package's server definitions, which are merged over its built-in defaults.
func lspOverrides(cfg *config.Config) []lsp.ServerDef {
	var defs []lsp.ServerDef
	for lang, s := range cfg.LSPServers {
		defs = append(defs, lsp.ServerDef{
			Lang:       lang,
			Command:    s.Command,
			Args:       s.Args,
			Env:        s.Env,
			Extensions: s.Extensions,
			Disabled:   s.Disabled,
		})
	}
	return defs
}

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
	// render is the Markdown renderer for the in-flight answer stream: it
	// colorizes diff blocks and syntax-highlights code as fragments arrive.
	render *mdstream.Renderer
}

func newTerminalEvents() *terminalEvents { return &terminalEvents{out: os.Stdout} }

// OnThinkingDelta opens the thinking style on the first fragment and prints
// fragments as they arrive, visually identical to the non-streamed block.
func (e *terminalEvents) OnThinkingDelta(text string) {
	th := theme.Current()
	if e.streamState != 1 {
		fmt.Fprintf(e.out, "\n%s✻ Thinking…%s\n%s", th.Thinking, th.Reset, th.Thinking)
		e.streamState = 1
	}
	fmt.Fprint(e.out, text)
}

// OnAssistantDelta switches from thinking to answer styling when needed and
// prints answer fragments live.
func (e *terminalEvents) OnAssistantDelta(text string) {
	th := theme.Current()
	if e.streamState == 1 {
		fmt.Fprint(e.out, th.Reset+"\n")
	}
	if e.streamState != 2 {
		fmt.Fprint(e.out, "\n") // blank line before the answer body
		// Route answer text through the Markdown renderer so diff blocks and
		// code are colorized as they stream in.
		e.render = mdstream.New(e.out, th.Text)
		e.streamState = 2
	}
	e.render.Write(text)
}

// OnStreamEnd closes styling and spacing after a streamed completion.
func (e *terminalEvents) OnStreamEnd() {
	if e.render != nil {
		e.render.Close()
		e.render = nil
	}
	if e.streamState != 0 {
		fmt.Fprint(e.out, theme.Current().Reset)
		fmt.Fprintln(e.out)
	}
	e.streamState = 0
}

// OnUserPrompt re-renders a user input line during transcript replay, in
// the same collapsed "❯ input" form a submitted editor line leaves in
// scrollback.
func (e *terminalEvents) OnUserPrompt(text string) {
	th := theme.Current()
	fmt.Fprintf(e.out, "\n%s\n", th.Paint(th.Accent, "❯ "+text))
}

// OnThinking renders chain-of-thought dim and italic under a "✻ Thinking"
// header, clearly separated from the final answer.
func (e *terminalEvents) OnThinking(text string) {
	th := theme.Current()
	fmt.Fprintf(e.out, "\n%s\n", th.Paint(th.Thinking, "✻ Thinking…"))
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		fmt.Fprintf(e.out, "%s\n", th.Paint(th.Thinking, "  "+line))
	}
}

func (e *terminalEvents) OnAssistantText(text string) {
	fmt.Fprintln(e.out) // blank line separating the answer from prior output
	r := mdstream.New(e.out, theme.Current().Text)
	r.Write(text)
	r.Close()
}

// OnToolCall prints the header with a yellow "running" dot and the tool name
// in the accent color. Nothing else writes to the terminal until OnToolResult,
// which repaints the dot.
func (e *terminalEvents) OnToolCall(name, args string) {
	th := theme.Current()
	e.lastCall = fmt.Sprintf("%s(%s)", th.Paint(th.Accent, camelName(name)), compactArgs(args))
	fmt.Fprintf(e.out, "\n%s %s\n", th.Paint(th.Warning, "●"), e.lastCall)
}

// OnToolResult moves the cursor back onto the header line, repaints the dot
// green (success) or red (failure), then prints a dim result preview.
func (e *terminalEvents) OnToolResult(name, result string, ok bool) {
	th := theme.Current()
	dot := th.Paint(th.Success, "●")
	if !ok {
		dot = th.Paint(th.Error, "●")
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
	// File edits report a numbered unified diff; render it in full and
	// colorized rather than collapsing it to a one-line preview.
	if header, body, isDiff := mdstream.FileEditDiff(result); isDiff {
		fmt.Fprintf(e.out, "  %s\n", th.Paint(th.Muted, "⎿ "+header))
		for _, l := range strings.Split(mdstream.RenderFileEditDiff(header, body, maxDiffLines), "\n") {
			fmt.Fprintf(e.out, "    %s\n", l)
		}
		return
	}

	preview := lines[0]
	if len(preview) > 160 {
		preview = preview[:160] + "…"
	}
	if len(lines) > 1 {
		preview += fmt.Sprintf(" (+%d lines)", len(lines)-1)
	}
	fmt.Fprintf(e.out, "  %s\n", th.Paint(th.Muted, "⎿ "+preview))
}

// maxDiffLines bounds how much of a large diff is printed.
const maxDiffLines = 40

// OnTurnStats prints a dim one-line summary after each turn: elapsed time,
// this turn's token split, the current context occupancy, and session
// totals.
func (e *terminalEvents) OnTurnStats(s agent.TurnStats) {
	th := theme.Current()
	fmt.Fprintf(e.out, "%s\n", th.Paint(th.Muted, fmt.Sprintf(
		"⏱ %s · turn: %s in + %s out (%d round%s) · context: %s · session: %s tok, %s",
		formatDuration(s.Duration),
		formatTokens(s.PromptTokens), formatTokens(s.CompletionTokens),
		s.Rounds, plural(s.Rounds),
		formatContext(s),
		formatTokens(s.SessionTokens), formatDuration(s.SessionDuration))))
}

// OnCompaction prints a dim notice when the conversation is compacted, so the
// user understands why earlier turns are no longer verbatim in context.
func (e *terminalEvents) OnCompaction(s agent.CompactionStats) {
	th := theme.Current()
	fmt.Fprintf(e.out, "%s\n", th.Paint(th.Muted, fmt.Sprintf(
		"⊙ Compacted context (%s): summarized %d earlier message%s into ~%s chars; %d → %d messages",
		s.Trigger,
		s.SummarizedMessages, plural(s.SummarizedMessages),
		formatTokens(s.SummaryChars),
		s.MessagesBefore, s.MessagesAfter)))
}

// formatContext renders context occupancy as "used/window tok (pct%)" when the
// window size is known, or just "used tok" when it is not — the fraction of the
// model's context window mainstream coding agents surface after each turn.
func formatContext(s agent.TurnStats) string {
	if pct := s.ContextPercent(); pct >= 0 {
		return fmt.Sprintf("%s/%s tok (%d%%)",
			formatTokens(s.ContextTokens), formatTokens(s.ContextLimit), pct)
	}
	return formatTokens(s.ContextTokens) + " tok"
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
	if len(args) == 0 || helpRequested(args) {
		printSubcommandUsage(os.Stdout, "skill")
		return nil
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
	return fmt.Errorf("unknown skill command %q (try: agent skill -h)", args[0])
}

// --- memory subcommand ------------------------------------------------------

func runMemory(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printSubcommandUsage(os.Stdout, "memory")
		return nil
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
	return fmt.Errorf("unknown memory command %q (try: agent memory -h)", args[0])
}

// --- session subcommand -----------------------------------------------------

// runSession makes recorded sessions traceable from the shell: list them,
// dump a full transcript (including tool calls and results), or delete one.
func runSession(args []string) error {
	if len(args) == 0 || helpRequested(args) {
		printSubcommandUsage(os.Stdout, "session")
		return nil
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
	return fmt.Errorf("unknown session command %q (try: agent session -h)", args[0])
}

// --- provider subcommand ----------------------------------------------------

// runProvider makes provider selection answerable from the shell: which
// vendors exist, whether their credential is exported, and which one the next
// session will use. It shares its rendering and argument grammar with the
// interactive /provider command so the two cannot drift apart.
func runProvider(args []string) error {
	if helpRequested(args) {
		printSubcommandUsage(os.Stdout, "provider")
		return nil
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		repl.WriteProviderList(os.Stdout, cfg, terminalWidth()-2, "agent provider use <name> [model]")
		return nil
	case "add", "custom":
		// With no flags the shell has nothing to go on either, so it asks —
		// same fields, same order as the in-session flow.
		name, def, err := providerDefinitionFrom(args[1:])
		if err != nil {
			return err
		}
		if err := config.SaveProviderProfile(config.ScopeGlobal, "", name, def); err != nil {
			return err
		}
		fmt.Printf("Added %s\n\nUse it with: agent provider use %s\n", repl.DescribeProviderDefinition(name, def), name)
		return nil
	case "remove":
		if len(args) != 2 {
			return fmt.Errorf("usage: agent provider remove <name>")
		}
		if err := config.RemoveProviderProfile(config.ScopeGlobal, "", args[1]); err != nil {
			return err
		}
		fmt.Printf("Removed custom provider %s\n", args[1])
		return nil
	case "use":
		name, model, wire, err := repl.ParseProviderArgs(strings.Join(args[1:], " "))
		if err != nil {
			return fmt.Errorf("usage: agent provider use <name> [model] [--anthropic|--openai] (%w)", err)
		}
		cfg, err := config.LoadForWire(name, wire)
		if err != nil {
			return err
		}
		if model != "" {
			cfg.Model = model
		}
		if cfg.Model == "" {
			return fmt.Errorf("no default model for provider %q; use: agent provider use %s <model>", name, name)
		}
		if err := config.SaveProviderChoice(cfg, model); err != nil {
			return err
		}
		target := cfg.Provider
		if cfg.Format != "" {
			target += " (" + cfg.Format + " wire)"
		}
		fmt.Printf("Set provider = %s, model = %s\n", target, cfg.Model)
		// Switching without a resolvable credential is allowed — the key may
		// come from an environment variable in the shell that runs the agent —
		// but saying so now beats a failure on the first request.
		if _, err := cfg.BuildProvider(); err != nil {
			fmt.Printf("warning: %v\n", err)
		}
		return nil
	}
	return fmt.Errorf("unknown provider command %q (try: agent provider -h)", sub)
}

// providerDefinitionFrom builds a custom provider from flags, or asks for the
// fields when none are given. Typing a flag string is a poor first experience
// for something with five fields, half of which have defaults.
func providerDefinitionFrom(args []string) (string, config.ProviderConfig, error) {
	if len(args) > 0 {
		name, def, err := repl.ParseProviderDefinition(args)
		if err != nil {
			return "", def, fmt.Errorf("usage: agent provider add [<name> --base-url <url> [--model <id>] [--anthropic|--openai] [--api-key <key>]] (%w)", err)
		}
		return name, def, nil
	}
	var def config.ProviderConfig
	in := bufio.NewScanner(os.Stdin)
	name, ok := promptRequired(in, "Name (shown in the provider list)", "A name is required — it is how you select this provider.")
	if !ok {
		return "", def, fmt.Errorf("cancelled")
	}
	def.BaseURL, ok = promptRequired(in, "Base URL (e.g. https://llm.example.com/v1)",
		"A base URL is required — the endpoint to send requests to, including any /v1.")
	if !ok {
		return "", def, fmt.Errorf("cancelled")
	}
	def.Format, _ = prompt(in, "API style — openai or anthropic [openai]")
	def.Format = strings.ToLower(def.Format)
	def.Model, _ = prompt(in, "Model")
	def.APIKey = promptSecret(in, fmt.Sprintf("API key (Enter to read $%s from the environment instead)", config.EnvKeyName(name)))
	return name, def, nil
}

// prompt reads one answer. ok is false at end of input, which cancels.
func prompt(in *bufio.Scanner, label string) (string, bool) {
	fmt.Printf("%s: ", label)
	if !in.Scan() {
		fmt.Println()
		return "", false
	}
	return strings.TrimSpace(in.Text()), true
}

// promptRequired re-asks until the field is filled: a blank line is a slip,
// not a decision to abandon the definition.
func promptRequired(in *bufio.Scanner, label, requirement string) (string, bool) {
	for {
		value, ok := prompt(in, label)
		if !ok {
			return "", false
		}
		if value != "" {
			return value, true
		}
		fmt.Println(requirement)
	}
}

// promptSecret reads a credential without echoing it when stdin is a terminal,
// so a key does not end up in the scrollback or a screen recording.
func promptSecret(in *bufio.Scanner, label string) string {
	fmt.Printf("%s: ", label)
	if isTTY(os.Stdin) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}
	if !in.Scan() {
		return ""
	}
	return strings.TrimSpace(in.Text())
}

// --- config subcommand ------------------------------------------------------

func runConfig(args []string) error {
	if helpRequested(args) {
		printSubcommandUsage(os.Stdout, "config")
		return nil
	}
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
		// No vendor is chosen here: that is the user's call, and writing one
		// in only produced a credential error for a provider they never
		// picked. The file carries the neutral defaults; the next line says
		// how to choose.
		cfg := &config.Config{MaxTurns: 40}
		if err := cfg.Save(); err != nil {
			return err
		}
		path, _ := config.Path()
		fmt.Printf("Wrote %s\n\nPick a provider next:\n  agent provider list\n  agent provider use <name>\n\nOr start the session and run /provider — it prompts for the API key and saves it.\n", path)
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
	return fmt.Errorf("unknown config command %q (try: agent config -h)", sub)
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
