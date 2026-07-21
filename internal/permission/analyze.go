package permission

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Robust command analysis. A raw-string deny-list is easy to evade — `\rm`,
// `/bin/rm`, `"rm"`, `sh -c 'rm -rf /'`, `$(rm ...)` all slip past naive
// pattern matching. Instead of matching the whole command string, we split it
// into the individual commands it will actually run (across ; | && $() `` and
// quotes), normalize each command's executable name (strip the directory, a
// leading backslash, and surrounding quotes), and classify the normalized
// name. Obfuscation of the command name is itself treated as dangerous;
// command wrappers (env, xargs, timeout, …) are unwrapped and their payload
// re-analyzed; and shell interpreters (sh -c, eval, …) are dangerous because
// they can run anything. When the parser cannot confidently understand the
// command it fails closed (dangerous) rather than waving it through.

// dangerousCommands maps a normalized executable name to why it is dangerous.
var dangerousCommands = map[string]string{
	"rm":       "file deletion (rm)",
	"rmdir":    "directory deletion (rmdir)",
	"unlink":   "file deletion (unlink)",
	"shred":    "irreversible file shredding (shred)",
	"sudo":     "privilege escalation (sudo)",
	"doas":     "privilege escalation (doas)",
	"su":       "switching user (su)",
	"chmod":    "permission change (chmod)",
	"chown":    "ownership change (chown)",
	"chgrp":    "group-ownership change (chgrp)",
	"chattr":   "file-attribute change (chattr)",
	"kill":     "process termination (kill)",
	"pkill":    "process termination (pkill)",
	"killall":  "process termination (killall)",
	"dd":       "raw disk/file writing (dd)",
	"mkfs":     "filesystem formatting (mkfs)",
	"fdisk":    "disk partitioning (fdisk)",
	"parted":   "disk partitioning (parted)",
	"shutdown": "system power control (shutdown)",
	"reboot":   "system power control (reboot)",
	"halt":     "system power control (halt)",
	"poweroff": "system power control (poweroff)",
	"truncate": "file truncation (truncate)",
	"crontab":  "scheduling jobs (crontab)",
}

// shellInterpreters run whatever they are given, so they can smuggle any
// command past name-based checks; always dangerous.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "ksh": true, "dash": true,
	"fish": true, "eval": true, "source": true, "csh": true, "tcsh": true,
}

// wrappers run another command passed as their arguments. They are unwrapped
// (their own leading flags/values skipped) and the wrapped command is
// re-analyzed, so `timeout 30 go test` stays safe while `xargs rm` does not.
var wrappers = map[string]bool{
	"env": true, "xargs": true, "timeout": true, "nohup": true,
	"setsid": true, "exec": true, "watch": true, "command": true,
	"builtin": true, "stdbuf": true, "nice": true, "ionice": true,
}

// safeCommands are common read-only / build commands auto-approved by the
// strict ("allow-list") posture; everything else there requires approval.
var safeCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true,
	"grep": true, "rg": true, "egrep": true, "fgrep": true, "find": true, "fd": true,
	"echo": true, "printf": true, "pwd": true, "wc": true, "sort": true, "uniq": true,
	"cut": true, "tr": true, "awk": true, "sed": true, "diff": true, "stat": true,
	"file": true, "which": true, "type": true, "date": true, "whoami": true, "tree": true,
	"du": true, "df": true, "ps": true, "git": true, "go": true, "gofmt": true,
	"node": true, "npm": true, "yarn": true, "pnpm": true, "python": true, "python3": true,
	"pip": true, "pip3": true, "ruby": true, "cargo": true, "make": true, "jq": true,
	"true": true, "false": true, "test": true, "basename": true, "dirname": true,
}

// gitDangerousSub lists dangerous git subcommands with their reasons.
var gitDangerousSub = []struct {
	match  []string
	reason string
}{
	{[]string{"push", "--force"}, "force-pushing (git push --force)"},
	{[]string{"push", "-f"}, "force-pushing (git push -f)"},
	{[]string{"push"}, "publishing to a remote (git push)"},
	{[]string{"reset", "--hard"}, "discarding work (git reset --hard)"},
	{[]string{"clean"}, "deleting untracked files (git clean)"},
	{[]string{"branch", "-D"}, "force-deleting a branch (git branch -D)"},
}

var (
	pipeToShellRe = regexp.MustCompile(`(?:curl|wget|fetch)\b[^|]*\|\s*(?:[a-z/]*/)?(?:ba|z|k|da)?sh\b`)
	redirectAbsRe = regexp.MustCompile(`>>?\s*("?)/`)
	assignRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// analyzeCommand reports whether a shell command is dangerous and why. It is
// the robust replacement for the old whole-string regex list.
func analyzeCommand(cmd, workDir string) (bool, string) {
	if pipeToShellRe.MatchString(cmd) {
		return true, "piping a download into a shell"
	}
	if loc := redirectAbsRe.FindStringIndex(cmd); loc != nil {
		if target := redirectTarget(cmd[loc[0]:]); target != "" && !withinDir(target, workDir) {
			return true, "redirecting output outside the project (" + target + ")"
		}
	}
	for _, seg := range shellSegments(cmd) {
		if dangerous, reason := classifySegment(seg, workDir, 0); dangerous {
			return true, reason
		}
	}
	return false, ""
}

// classifySegment classifies a single command segment, unwrapping wrapper
// commands recursively (bounded depth). depth guards against pathological
// nesting.
func classifySegment(seg, workDir string, depth int) (bool, string) {
	if depth > 6 {
		return true, "deeply nested command"
	}
	name, args, obfuscated := baseCommand(seg)
	if name == "" {
		return false, ""
	}
	if obfuscated {
		return true, "possibly obfuscated command (" + strings.TrimSpace(seg) + ")"
	}
	if shellInterpreters[name] {
		return true, "arbitrary code execution (" + name + ")"
	}
	if wrappers[name] {
		// Re-analyze the wrapped command: skip the wrapper's own leading flags
		// and values, then classify what remains.
		if inner := stripWrapperArgs(name, args); inner != "" {
			return classifySegment(inner, workDir, depth+1)
		}
		return false, ""
	}
	if strings.HasPrefix(name, "mkfs") {
		return true, "filesystem formatting (mkfs)"
	}
	if reason, ok := dangerousCommands[name]; ok {
		return true, reason
	}
	if name == "mv" {
		if movesOutside(args, workDir) {
			return true, "moving files outside the project (mv)"
		}
		return false, ""
	}
	if name == "git" {
		if reason, ok := gitDanger(args); ok {
			return true, reason
		}
	}
	return false, ""
}

// isKnownSafe reports whether every command in the line is on the safe
// allow-list AND none is independently dangerous — used by the strict posture
// to auto-approve routine commands. workDir scopes path-sensitive checks.
func isKnownSafe(cmd, workDir string) bool {
	segs := shellSegments(cmd)
	if len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if dangerous, _ := classifySegment(seg, workDir, 0); dangerous {
			return false // e.g. "git push" — git is listed, but the subcommand is not
		}
		name, args, obfuscated := baseCommand(seg)
		if obfuscated || name == "" {
			return false
		}
		if wrappers[name] {
			if inner := stripWrapperArgs(name, args); inner != "" {
				if !isKnownSafe(inner, workDir) {
					return false
				}
				continue
			}
		}
		if !safeCommands[name] {
			return false
		}
	}
	return true
}

// baseCommand extracts and normalizes the executable name of one command
// segment: it skips leading VAR=value assignments, takes the first token,
// strips a directory prefix and surrounding quotes and a leading backslash,
// and lowercases. obfuscated is true when the token used quoting/escaping/
// expansion to disguise the name (a red flag on its own).
func baseCommand(seg string) (name, args string, obfuscated bool) {
	seg = strings.TrimSpace(seg)
	for {
		tok, rest := firstToken(seg)
		if tok == "" {
			return "", "", false
		}
		if assignRe.MatchString(tok) {
			seg = strings.TrimSpace(rest)
			continue
		}
		break
	}
	tok, rest := firstToken(seg)
	if strings.ContainsAny(tok, "\"'`$*?") {
		obfuscated = true
	}
	clean := strings.NewReplacer(`"`, "", `'`, "", `\`, "").Replace(tok)
	if clean == "" {
		return "", "", obfuscated
	}
	clean = filepath.Base(clean)
	return strings.ToLower(clean), strings.TrimSpace(rest), obfuscated
}

// BaseProgram returns the normalized name of the first command in a shell
// command line (e.g. "npm" for "npm publish"), or "" if none. It is exported
// for the gate's "always allow this program" scoping.
func BaseProgram(cmd string) string {
	segs := shellSegments(cmd)
	if len(segs) == 0 {
		return ""
	}
	name, _, _ := baseCommand(segs[0])
	return name
}

// firstToken returns the first whitespace-delimited token and the remainder.
func firstToken(s string) (tok, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i+1:]
}

// stripWrapperArgs removes a wrapper command's own leading options and values
// and returns the wrapped command. For most wrappers, leading -flags and bare
// numeric/duration values (timeout's seconds) are skipped; the first remaining
// token begins the wrapped command.
func stripWrapperArgs(name, args string) string {
	s := strings.TrimSpace(args)
	for s != "" {
		tok, rest := firstToken(s)
		switch {
		case strings.HasPrefix(tok, "-"):
			s = strings.TrimSpace(rest) // a flag (and we don't consume its value)
		case name == "timeout" && isDurationish(tok):
			s = strings.TrimSpace(rest) // timeout's duration argument
		case name == "nice" || name == "ionice":
			// nice/ionice take numeric priority args; skip pure numbers.
			if isDurationish(tok) {
				s = strings.TrimSpace(rest)
				continue
			}
			return s
		case name == "env" && assignRe.MatchString(tok):
			s = strings.TrimSpace(rest) // env VAR=val ... cmd
		default:
			return s
		}
	}
	return ""
}

func isDurationish(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != 's' && r != 'm' && r != 'h' && r != 'd' {
			return false
		}
	}
	return true
}

// gitDanger classifies a git command by its subcommand arguments.
func gitDanger(args string) (string, bool) {
	fields := strings.Fields(args)
	for _, d := range gitDangerousSub {
		if matchSubcommand(fields, d.match) {
			return d.reason, true
		}
	}
	return "", false
}

// matchSubcommand reports whether fields[0] equals needles[0] (the subcommand)
// and every other needle appears among the fields (flags, order-independent).
func matchSubcommand(fields, needles []string) bool {
	// The subcommand is the first non-flag field.
	var sub string
	var flags []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			flags = append(flags, f)
		} else if sub == "" {
			sub = f
		}
	}
	if sub != needles[0] {
		return false
	}
	for _, n := range needles[1:] {
		found := false
		for _, f := range append(flags, fields...) {
			if f == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// movesOutside reports whether an mv command's destination resolves outside
// workDir (including the filesystem root).
func movesOutside(args, workDir string) bool {
	var positional []string
	for _, f := range strings.Fields(args) {
		if !strings.HasPrefix(f, "-") {
			positional = append(positional, f)
		}
	}
	if len(positional) < 2 {
		return false
	}
	dest := positional[len(positional)-1]
	if dest == "/" {
		return true
	}
	if filepath.IsAbs(dest) {
		return !withinDir(dest, workDir)
	}
	return false
}

// redirectTarget extracts the path following a leading > or >> operator.
func redirectTarget(s string) string {
	s = strings.TrimLeft(s, ">")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	tok, _ := firstToken(s)
	return tok
}

// withinDir reports whether path is inside dir (or equal to it).
func withinDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	clean := filepath.Clean(path)
	root := filepath.Clean(dir)
	return clean == root || strings.HasPrefix(clean, root+string(filepath.Separator))
}

// shellSegments splits a command line into the individual commands it runs,
// breaking on ; | & and newlines outside quotes, and extracting the contents
// of $(...) command substitutions (which execute even inside double quotes).
// Single-quoted spans are kept literal.
func shellSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}

	runes := []rune(cmd)
	inSingle, inDouble := false, false
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(c)
			}
		case inDouble:
			if c == '"' {
				inDouble = false
			} else if c == '$' && i+1 < len(runes) && runes[i+1] == '(' {
				inner, adv := captureSubst(runes[i:])
				segs = append(segs, shellSegments(inner)...)
				i += adv - 1
			} else {
				cur.WriteRune(c)
			}
		default:
			switch c {
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case ';', '\n', '|', '&', '(', ')', '`':
				flush()
			case '$':
				if i+1 < len(runes) && runes[i+1] == '(' {
					inner, adv := captureSubst(runes[i:])
					segs = append(segs, shellSegments(inner)...)
					i += adv - 1
				} else {
					cur.WriteRune(c)
				}
			default:
				cur.WriteRune(c)
			}
		}
	}
	flush()
	return segs
}

// captureSubst reads a $( ... ) span starting at runes[0]=='$'. It returns the
// inner text and how many runes were consumed (including the closing paren).
func captureSubst(runes []rune) (inner string, consumed int) {
	depth := 0
	var b strings.Builder
	for i := 1; i < len(runes); i++ {
		switch runes[i] {
		case '(':
			depth++
			if depth == 1 {
				continue
			}
		case ')':
			depth--
			if depth == 0 {
				return b.String(), i + 1
			}
		}
		if depth >= 1 {
			b.WriteRune(runes[i])
		}
	}
	return b.String(), len(runes)
}
