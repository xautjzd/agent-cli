// Package checkpoint provides undo/redo restore points for a session — the
// /rewind feature that mainstream coding agents expose.
//
// A checkpoint is opened before each user turn. It records where the
// conversation stood (so history can be trimmed back) and, lazily, the
// original contents of every file the turn is about to modify (so edits can
// be rolled back). Rewinding to a checkpoint restores both: the working tree
// is returned to its pre-turn state and the conversation is truncated to the
// message that started it.
//
// The manager depends on nothing in the agent or REPL: the file-mutating
// tools call SnapshotFile before they write, and the REPL drives Begin and
// Rewind. This keeps the checkpoint concern isolated (SRP) and lets the tools
// stay usable without it (a nil snapshotter is a no-op).
package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// fileState is a file's contents captured before a turn changed it. When the
// file did not exist, existed is false and restoring it means deleting it.
type fileState struct {
	existed bool
	content []byte
	mode    os.FileMode
}

// Checkpoint is one restore point, created before a user turn.
type Checkpoint struct {
	// ID is a stable, human-facing sequence number (1-based).
	ID int
	// Label is the user input that started the turn, for display.
	Label string
	// Time is when the checkpoint was opened.
	Time time.Time
	// MsgCount is the agent history length (including the system prompt)
	// before the turn ran, so the conversation can be trimmed back to it.
	MsgCount int
	// InputCount is the number of recorded raw user inputs before the turn,
	// kept in step with MsgCount so replay bookkeeping stays aligned.
	InputCount int

	// files holds the pre-change contents of every path this turn modified,
	// captured the first time each is touched.
	files map[string]*fileState
}

// FilesChanged reports how many distinct files this checkpoint's turn modified.
func (c *Checkpoint) FilesChanged() int { return len(c.files) }

// Manager owns the ordered list of checkpoints for one session. It is safe for
// concurrent use: subagents may snapshot files from parallel goroutines.
type Manager struct {
	mu  sync.Mutex
	cps []*Checkpoint
	seq int
}

// NewManager returns an empty checkpoint manager.
func NewManager() *Manager { return &Manager{} }

// Begin opens a new checkpoint recording the conversation position before a
// turn, and returns it. Subsequent SnapshotFile calls attach to it until the
// next Begin.
func (m *Manager) Begin(label string, msgCount, inputCount int) *Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	cp := &Checkpoint{
		ID:         m.seq,
		Label:      label,
		Time:       time.Now(),
		MsgCount:   msgCount,
		InputCount: inputCount,
		files:      map[string]*fileState{},
	}
	m.cps = append(m.cps, cp)
	return cp
}

// SnapshotFile records path's current contents into the active checkpoint, at
// most once per path per checkpoint. path must be absolute. With no active
// checkpoint it is a no-op, so file edits outside a turn are simply not
// tracked. Implements tool.FileSnapshotter.
func (m *Manager) SnapshotFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cps) == 0 {
		return
	}
	cp := m.cps[len(m.cps)-1]
	if _, done := cp.files[path]; done {
		return
	}
	st := &fileState{mode: 0o644}
	if data, err := os.ReadFile(path); err == nil {
		st.existed = true
		st.content = data
		if fi, serr := os.Stat(path); serr == nil {
			st.mode = fi.Mode().Perm()
		}
	}
	cp.files[path] = st
}

// List returns the checkpoints, oldest first.
func (m *Manager) List() []*Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Checkpoint, len(m.cps))
	copy(out, m.cps)
	return out
}

// Len reports how many checkpoints exist.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cps)
}

// FileEffect describes what rewinding will do to one file: restore it to
// Content, or delete it (Delete true) when it did not exist at the checkpoint.
type FileEffect struct {
	Path    string
	Delete  bool
	Content string // the contents restored; empty when Delete
}

// RewindPlan reports what Rewind(idx) would do, without changing anything: the
// per-file effect (restore to which contents, or delete) and how many turns
// are discarded. This lets the caller describe a rewind by the STATE it
// returns to — the contents each file is restored to — rather than by the
// message being undone, which is the intuitive way to choose a restore point.
//
// A file touched across several discarded turns is restored from its earliest
// snapshot at or after idx (the true pre-idx contents), matching Rewind.
func (m *Manager) RewindPlan(idx int) (effects []FileEffect, turns int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.cps) {
		return nil, 0, fmt.Errorf("checkpoint index %d out of range", idx)
	}
	seen := map[string]bool{}
	for i := idx; i < len(m.cps); i++ {
		for path, st := range m.cps[i].files {
			if seen[path] {
				continue
			}
			seen[path] = true
			effects = append(effects, FileEffect{
				Path:    path,
				Delete:  !st.existed,
				Content: string(st.content),
			})
		}
	}
	sort.Slice(effects, func(a, b int) bool { return effects[a].Path < effects[b].Path })
	return effects, len(m.cps) - idx, nil
}

// Rewind rolls the working tree back to the state captured at checkpoint idx
// (0-based, oldest first), then drops that checkpoint and every later one. It
// returns the target checkpoint — whose MsgCount/InputCount the caller uses to
// trim the conversation — and the number of files restored.
//
// For a file touched across several of the discarded turns, the earliest
// snapshot holds the correct pre-rewind contents, so paths are restored from
// their first occurrence at or after idx.
func (m *Manager) Rewind(idx int) (*Checkpoint, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx < 0 || idx >= len(m.cps) {
		return nil, 0, fmt.Errorf("checkpoint index %d out of range", idx)
	}
	target := m.cps[idx]

	seen := map[string]bool{}
	restored := 0
	var firstErr error
	for i := idx; i < len(m.cps); i++ {
		for path, st := range m.cps[i].files {
			if seen[path] {
				continue
			}
			seen[path] = true
			if err := restoreFile(path, st); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			restored++
		}
	}
	m.cps = m.cps[:idx]
	return target, restored, firstErr
}

// restoreFile returns one file to its captured state: a file that did not
// exist is removed, otherwise its original contents and permissions are
// rewritten.
func restoreFile(path string, st *fileState) error {
	if !st.existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, st.content, st.mode)
}
