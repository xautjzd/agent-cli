package repl

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xautjzd/agent-cli/internal/checkpoint"
	"github.com/xautjzd/agent-cli/internal/textwidth"
)

// cmdRewind implements /rewind: pick an earlier checkpoint and roll both the
// working tree and the conversation back to just before that message.
//
// Checkpoints are opened before every turn (see runPrompt), so each entry is a
// message the user sent. Rewinding to one undoes that message and everything
// after it: file edits made since are restored from their snapshots and the
// history is truncated to where it stood before the message was sent.
func (r *Repl) cmdRewind(_ context.Context, args string) error {
	if r.Checkpoints == nil {
		return fmt.Errorf("checkpoints are disabled")
	}
	cps := r.Checkpoints.List()
	if len(cps) == 0 {
		return fmt.Errorf("no checkpoints yet — one is created before each message you send")
	}

	// Present newest first: display index d maps to manager index
	// len(cps)-1-d. Entries are described by the STATE they restore to (the
	// file contents), so choosing "the version2 state" is unambiguous.
	labels := r.rewindLabels(cps)

	idx, ok := r.selectIndex(
		"Rewind — pick the state to restore (later turns are discarded):", labels)
	if !ok {
		return nil
	}
	target := len(cps) - 1 - idx

	// Show exactly what will change and confirm, so a file created after the
	// chosen point is never silently deleted.
	if !r.confirmRewind(target, cps[idx].Label) {
		return nil
	}
	return r.rewindTo(target)
}

// confirmRewind previews the file effects of rewinding to manager index mi and
// asks the user to proceed. It returns true when there is nothing to lose
// (no file changes) or the user approves.
func (r *Repl) confirmRewind(mi int, label string) bool {
	effects, _, err := r.Checkpoints.RewindPlan(mi)
	if err != nil {
		fmt.Fprintln(r.Out, err)
		return false
	}
	if len(effects) == 0 {
		return true // conversation-only rewind; nothing on disk to warn about
	}

	fmt.Fprintln(r.Out, "\033[36mThis will change on disk:\033[0m")
	for _, e := range effects {
		if e.Delete {
			// Deletion is the surprising case (the file was created after
			// this point), so flag it in red.
			fmt.Fprintf(r.Out, "  \033[31m✗ delete\033[0m  %s \033[2m(created after this point)\033[0m\n", r.relPath(e.Path))
		} else {
			fmt.Fprintf(r.Out, "  \033[33m↺ restore\033[0m %s \033[2m→\033[0m %s\n",
				r.relPath(e.Path), contentPreview(e.Content))
		}
	}
	answer, ok := r.readInput("Proceed? [y]es / [N]o ")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		fmt.Fprintln(r.Out, "Rewind cancelled.")
		return false
	}
}

// relPath renders an absolute path relative to the working directory for
// display, falling back to the absolute path when it lies outside.
func (r *Repl) relPath(p string) string {
	if rel, err := filepath.Rel(r.WorkDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

// rewindTo restores files and conversation to checkpoint mi (manager index)
// and re-persists the trimmed session.
func (r *Repl) rewindTo(mi int) error {
	target, restored, err := r.Checkpoints.Rewind(mi)
	if err != nil {
		return err
	}

	// Trim the conversation. History()[0] is the system prompt; Restore
	// re-adds it, so hand it the non-system messages up to the checkpoint.
	full := r.Agent.History()
	if target.MsgCount >= 1 && target.MsgCount <= len(full) {
		r.Agent.Restore(full[1:target.MsgCount])
	}
	if target.InputCount >= 0 && target.InputCount <= len(r.rawInputs) {
		r.rawInputs = r.rawInputs[:target.InputCount]
	}
	r.syncSession()

	fileNote := "no files changed"
	if restored == 1 {
		fileNote = "restored 1 file"
	} else if restored > 1 {
		fileNote = fmt.Sprintf("restored %d files", restored)
	}
	fmt.Fprintf(r.Out, "\033[36m↩ Rewound\033[0m — %s, conversation trimmed to %d message(s).\n",
		fileNote, target.MsgCount-1)
	return nil
}

// rewindLabels renders the checkpoint list newest first, describing each entry
// by the STATE it restores to — the file contents — so the user chooses a
// state ("the version2 one") rather than decoding which message gets undone.
// When a checkpoint changed no files it falls back to the message it discards.
func (r *Repl) rewindLabels(cps []*checkpoint.Checkpoint) []string {
	labels := make([]string, len(cps))
	for i := range cps {
		di := len(cps) - 1 - i // manager index for this newest-first row
		cp := cps[di]
		head := fmt.Sprintf("%-8s ", relativeTime(cp.Time))

		// The plan describes what this rewind restores files to.
		var effect string
		if effects, _, err := r.Checkpoints.RewindPlan(di); err == nil && len(effects) > 0 {
			e := effects[0]
			name := r.relPath(e.Path)
			if e.Delete {
				effect = fmt.Sprintf("%s → deleted", name)
			} else {
				effect = fmt.Sprintf("%s = %s", name, contentPreview(e.Content))
			}
			if len(effects) > 1 {
				effect += fmt.Sprintf(" (+%d more)", len(effects)-1)
			}
		} else {
			// No file change: describe the conversation turn being dropped.
			effect = "undo: " + firstLine(cp.Label, 40)
		}
		labels[i] = head + effect
	}
	return labels
}

// contentPreview renders a short, single-line preview of file contents for the
// rewind menu and confirmation.
func contentPreview(s string) string {
	s = strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "⏎")
	return "\"" + textwidth.Truncate(s, 40) + "\""
}

// selectIndex offers a list and returns the chosen 0-based index. It prefers
// the arrow-navigable TUI overlay and falls back to a numbered prompt, so it
// behaves like /resume in both environments.
func (r *Repl) selectIndex(title string, labels []string) (int, bool) {
	if r.tuiSelect != nil {
		items := make([]pickerItem, len(labels))
		for i, l := range labels {
			items[i] = pickerItem{label: l, filterText: l}
		}
		return r.tuiSelect(title, items)
	}
	fmt.Fprintln(r.Out, title)
	for i, l := range labels {
		fmt.Fprintf(r.Out, "  %2d. %s\n", i+1, l)
	}
	line, ok := r.readInput("Select (Enter to cancel): ")
	if !ok {
		return 0, false
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		return 0, false
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(labels) {
		fmt.Fprintf(r.Out, "invalid selection %q\n", choice)
		return 0, false
	}
	return n - 1, true
}
