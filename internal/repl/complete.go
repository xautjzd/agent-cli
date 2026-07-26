package repl

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xautjzd/agent-cli/internal/catalog"
	"github.com/xautjzd/agent-cli/internal/provider"
)

// candidate is one completion suggestion shown in the popup.
type candidate struct {
	// text replaces the current token when the candidate is accepted,
	// e.g. "/model" or "@internal/repl/repl.go".
	text string
	// desc is the dimmed explanation rendered next to the text.
	desc string
	// current marks the candidate that equals the setting's active value, so
	// the popup opens with it highlighted (e.g. the running /effort level)
	// instead of always landing on the first row.
	current bool
}

// maxCandidates bounds how many candidates are collected. The popup shows a
// scrolling window of popupRows at a time, so every command and skill stays
// reachable even when the list is long.
const maxCandidates = 50

// popupRows is the visible height of the completion popup.
const popupRows = 8

// completionSkipDirs mirrors the search tools' skip list: directories that
// would only pollute @-path suggestions.
var completionSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".idea": true, ".vscode": true, "dist": true, "build": true,
}

// runePosToByte converts a rune index — bubbles' textinput stores its value as
// []rune and reports the cursor as a rune offset — into a byte offset into
// value. The token helpers below index bytes, so every cursor position that
// enters them must pass through here first; otherwise a line beginning with a
// multi-byte rune (e.g. CJK text before "@") misaligns the whole scan.
func runePosToByte(value string, runePos int) int {
	if runePos <= 0 {
		return 0
	}
	n := 0
	for i := range value { // ranges by rune; i is the rune's byte offset
		if n == runePos {
			return i
		}
		n++
	}
	return len(value)
}

// bytePosToRune is the inverse of runePosToByte: it maps a byte offset back to
// a rune index so a new cursor position can be handed to textinput.SetCursor.
func bytePosToRune(value string, bytePos int) int {
	n := 0
	for i := range value {
		if i >= bytePos {
			return n
		}
		n++
	}
	return n
}

// tokenBounds returns the [start, end) byte range of the whitespace-delimited
// token containing the cursor. With the cursor on a space, the token to its
// left is chosen so completion keeps working right after typing a token.
func tokenBounds(value string, pos int) (int, int) {
	if pos > len(value) {
		pos = len(value)
	}
	start := pos
	for start > 0 && value[start-1] != ' ' && value[start-1] != '\t' {
		start--
	}
	end := pos
	for end < len(value) && value[end] != ' ' && value[end] != '\t' {
		end++
	}
	return start, end
}

// completionsFor computes popup candidates for the text before the cursor.
//
// Key flow: only the token under the cursor matters. A leading-"/" token at
// the start of the line completes commands and skills; an "@" reference under
// the cursor completes project file paths. Everything else yields no popup.
func (r *Repl) completionsFor(value string, pos int) []candidate {
	pos = runePosToByte(value, pos)
	start, _ := tokenBounds(value, pos)
	if start == 0 && strings.HasPrefix(value[start:pos], "/") {
		return r.commandCandidates(value[start+1 : pos])
	}
	// "@" may sit mid-token — CJK prompts like "看一下@main" carry no space
	// before it — so refBounds, not the whitespace token, locates the ref.
	if at, _, ok := refBounds(value, pos); ok {
		return r.fileCandidates(value[at+1 : pos])
	}
	// Argument position: some commands know their own valid arguments, so
	// "/provider gl<tab>" and "/model cla<tab>" complete too.
	if cands := r.argumentCandidates(value, start, pos); cands != nil {
		return cands
	}
	return nil
}

// isRefWordByte reports whether b is an ASCII word byte, matching \w in
// fileRefRe. An "@" preceded by such a byte (e.g. "user@host") is not a
// reference; one preceded by whitespace, punctuation, or a CJK rune's bytes is.
func isRefWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// refBounds locates the @-file-reference the cursor sits in. It returns the
// byte range [at, end) spanning "@" through the end of the ref body and whether
// one was found. Unlike tokenBounds it does not require whitespace before "@",
// mirroring fileRefRe: "@" must begin the line or follow a non-word rune, and
// the body holds no whitespace or second "@".
func refBounds(value string, pos int) (at, end int, ok bool) {
	if pos > len(value) {
		pos = len(value)
	}
	// Walk back over the ref body to its "@".
	at = pos
	for at > 0 {
		c := value[at-1]
		if c == ' ' || c == '\t' || c == '@' {
			break
		}
		at--
	}
	if at == 0 || value[at-1] != '@' {
		return 0, 0, false
	}
	at--
	if at > 0 && isRefWordByte(value[at-1]) {
		return 0, 0, false
	}
	// Extend forward to the end of the ref body.
	end = pos
	for end < len(value) {
		c := value[end]
		if c == ' ' || c == '\t' || c == '@' {
			break
		}
		end++
	}
	return at, end, true
}

// argumentCandidates completes the argument of a slash command whose
// options are known — provider and model names. It returns nil when the
// cursor is not in such a position.
func (r *Repl) argumentCandidates(value string, start, pos int) []candidate {
	if !strings.HasPrefix(value, "/") {
		return nil
	}
	cmd, rest, found := strings.Cut(value[1:], " ")
	if !found {
		return nil
	}
	argStart := 1 + len(cmd) + 1

	// Second argument: only "/provider <name> <model>" completes one — the
	// chosen provider's model list. Every other command stops after arg one.
	if start != argStart {
		if cmd != "provider" {
			return nil
		}
		name, _, hasSecond := strings.Cut(rest, " ")
		if !hasSecond || start != argStart+len(name)+1 {
			return nil
		}
		query := strings.ToLower(value[start:pos])
		// Completing the second argument (a model): pre-select the running
		// model when it appears in this provider's list.
		return markCurrent(filterCandidates(r.modelOptions(name), query), r.Cfg.Model)
	}

	query := strings.ToLower(value[argStart:pos])

	var options [][2]string // name, description
	switch cmd {
	case "provider":
		// User profiles first (sorted — they come from a map), then the
		// built-ins in the catalog's own order; a profile shadows a preset of
		// the same name, so the preset is skipped to avoid a duplicate.
		names := make([]string, 0, len(r.Cfg.Providers))
		for name := range r.Cfg.Providers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			options = append(options, [2]string{name, "your config · " + r.Cfg.Providers[name].BaseURL})
		}
		for _, p := range catalog.All() {
			desc := p.Label + " · " + p.DefaultModel
			if p.AnthropicBaseURL != "" {
				desc += " · --anthropic available"
			}
			// A preset whose name a profile has taken is still listed — it is
			// a real vendor — but the row says so, and names the alias that
			// still reaches it.
			if note := overrideNote(r.Cfg.Providers, p); note != "" {
				desc += " · " + note
			}
			options = append(options, [2]string{p.Name, desc})
		}
		// Defining a provider comes last: it is an action, not a provider, so
		// it must not push the actual choices down. It is still a candidate of
		// its own rather than a verb ("/provider add …") that nothing can
		// complete and only the documentation knows about.
		options = append(options, [2]string{customProviderChoice, "define a custom endpoint — asks for each field"})
	case "model":
		options = r.modelOptions(r.Cfg.Provider)
	// /theme deliberately has no inline value completion: it is selected via its
	// own full-screen picker (opened by submitting "/theme"), which previews
	// each theme live as the highlight moves — something the inline popup, which
	// only applies on submit, cannot do.
	case "mode":
		options = append(options,
			[2]string{"hitl", "dangerous operations require confirmation (default)"},
			[2]string{"bypass", "no confirmations; dangerous operations are audit-logged"},
		)
	case "effort":
		// Only the levels the active model accepts — the same list /effort
		// prints, so completion cannot suggest something it would reject.
		for _, e := range provider.EffortsFor(r.Cfg.Model) {
			options = append(options, [2]string{string(e), e.Describe()})
		}
	default:
		return nil
	}

	// Some option sets carry a meaningful order of their own: the effort
	// ladder runs by strength, and providers follow the catalog's order.
	// Alphabetizing either would scramble a sequence the user reads as one.
	switch cmd {
	case "effort", "provider":
		return markCurrent(filterCandidatesInOrder(options, query), r.currentArgValue(cmd))
	}
	return markCurrent(filterCandidates(options, query), r.currentArgValue(cmd))
}

// commandCompletesArgs reports whether "/cmd <arg>" has a known set of value
// candidates (e.g. /effort, /model), so the TUI can open the value picker when
// the bare command is submitted instead of running it with no argument.
func (r *Repl) commandCompletesArgs(cmd string) bool {
	probe := "/" + cmd + " "
	return r.argumentCandidates(probe, len(probe), len(probe)) != nil
}

// currentArgValue returns the active value a "/cmd <arg>" completion should
// open highlighted, or "" when the command has no single current value. For
// effort the stored value is normalized (empty → adaptive) so it matches a
// listed level.
func (r *Repl) currentArgValue(cmd string) string {
	switch cmd {
	case "effort":
		e, _ := provider.ParseEffort(r.Cfg.Thinking)
		return string(e)
	case "provider":
		return r.Cfg.Provider
	case "model":
		return r.Cfg.Model
	case "mode":
		return string(r.permMode())
	}
	return ""
}

// markCurrent flags the candidate whose text equals current so the popup can
// open with it selected. A blank current or no match leaves the list unmarked.
func markCurrent(cands []candidate, current string) []candidate {
	if current == "" {
		return cands
	}
	for i := range cands {
		if cands[i].text == current {
			cands[i].current = true
		}
	}
	return cands
}

// modelOptions lists model suggestions for a provider: the configured profile's
// own model first — it may be newer than the catalog knows — then the catalog's
// known models. Used for both "/model" and "/provider <name> <model>".
func (r *Repl) modelOptions(name string) [][2]string {
	var options [][2]string
	seen := map[string]bool{}
	if p, ok := r.Cfg.Providers[name]; ok && p.Model != "" {
		desc := "your config"
		if name == r.Cfg.Provider {
			desc = "current profile"
		}
		options = append(options, [2]string{p.Model, desc})
		seen[p.Model] = true
	}
	for _, m := range catalog.ModelsFor(name) {
		if seen[m] {
			continue
		}
		options = append(options, [2]string{m, name})
	}
	return options
}

// filterCandidates keeps options whose name prefix- or substring-matches query,
// prefix matches ranked first, each group sorted, capped at maxCandidates.
// Sorting is what makes provider and file suggestions deterministic, since
// they are collected from maps and directory walks.
func filterCandidates(options [][2]string, query string) []candidate {
	return filterOptions(options, query, true)
}

// filterCandidatesInOrder is filterCandidates for option sets that carry a
// meaningful order of their own — the effort ladder runs adaptive → max → off,
// and alphabetizing it ("adaptive, high, low, max, medium, off, xhigh") would
// scramble a scale the user reads as a scale.
func filterCandidatesInOrder(options [][2]string, query string) []candidate {
	return filterOptions(options, query, false)
}

func filterOptions(options [][2]string, query string, alphabetical bool) []candidate {
	var prefix, substr []candidate
	for _, o := range options {
		c := candidate{text: o[0], desc: o[1]}
		switch {
		case strings.HasPrefix(strings.ToLower(o[0]), query):
			prefix = append(prefix, c)
		case strings.Contains(strings.ToLower(o[0]), query):
			substr = append(substr, c)
		}
	}
	if alphabetical {
		sort.Slice(prefix, func(i, j int) bool { return prefix[i].text < prefix[j].text })
		sort.Slice(substr, func(i, j int) bool { return substr[i].text < substr[j].text })
	}
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// commandCandidates matches built-in commands and installed skills.
// Prefix matches rank before substring matches so "mo" puts /model first.
func (r *Repl) commandCandidates(query string) []candidate {
	var prefix, substr []candidate
	add := func(name, desc string) {
		c := candidate{text: "/" + name, desc: desc}
		switch {
		case strings.HasPrefix(name, query):
			prefix = append(prefix, c)
		case strings.Contains(name, query):
			substr = append(substr, c)
		}
	}
	for _, c := range commands {
		add(c.name, c.desc)
	}
	if r.Commands != nil {
		if customs, err := r.Commands.List(); err == nil {
			for _, c := range customs {
				add(c.Name, "command: "+c.Description)
			}
		}
	}
	if skills, err := r.Skills.List(); err == nil {
		for _, s := range skills {
			add(s.Name, "skill: "+s.Description)
		}
	}
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// fileCandidates matches project-relative paths against the query. The file
// list is walked once per editor invocation and cached on the Repl until the
// next prompt, keeping keystroke latency flat.
func (r *Repl) fileCandidates(query string) []candidate {
	if r.fileCache == nil {
		r.fileCache = listProjectFiles(r.WorkDir)
	}
	var prefix, substr []candidate
	for _, p := range r.fileCache {
		c := candidate{text: "@" + p}
		base := filepath.Base(p)
		switch {
		case strings.HasPrefix(p, query) || strings.HasPrefix(base, query):
			prefix = append(prefix, c)
		case strings.Contains(p, query):
			substr = append(substr, c)
		}
		if len(prefix) >= popupRows {
			// Enough high-quality matches to fill the visible window.
			break
		}
	}
	out := append(prefix, substr...)
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// listProjectFiles returns up to a few thousand project-relative file paths,
// shortest first so top-level files surface before deeply nested ones.
func listProjectFiles(workDir string) []string {
	const cap = 5000
	var files []string
	filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if completionSkipDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".") && path != workDir) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		if len(files) >= cap {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool {
		if len(files[i]) != len(files[j]) {
			return len(files[i]) < len(files[j])
		}
		return files[i] < files[j]
	})
	return files
}

// acceptCandidate replaces the token containing the cursor with the
// candidate text plus a trailing space, returning the new value and cursor
// position.
func acceptCandidate(value string, pos int, c candidate) (string, int) {
	pos = runePosToByte(value, pos)
	start, end := tokenBounds(value, pos)
	// An "@" reference may start mid-token, so replace only from its "@"
	// rather than clobbering any CJK text glued to the left of it.
	if strings.HasPrefix(c.text, "@") {
		if at, refEnd, ok := refBounds(value, pos); ok {
			start, end = at, refEnd
		}
	}
	sep := " "
	if end < len(value) && (value[end] == ' ' || value[end] == '\t') {
		sep = "" // the following text already provides the separator
	}
	newValue := value[:start] + c.text + sep + value[end:]
	// SetCursor expects a rune index, so map the new byte offset back.
	return newValue, bytePosToRune(newValue, start+len(c.text)+len(sep))
}
