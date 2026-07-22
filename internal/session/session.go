// Package session persists conversation history so sessions can be listed,
// inspected, and resumed later — the /resume feature.
//
// Each session is one JSON file under <project>/.agent/sessions, keeping
// history project-scoped and human-inspectable, mirroring how memory and
// skills are stored.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xautjzd/agent-cli/internal/home"
	"github.com/xautjzd/agent-cli/internal/provider"
	"github.com/xautjzd/agent-cli/internal/textwidth"
)

// Meta is the listable summary of a session.
type Meta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// MessageCount excludes the system prompt.
	MessageCount int `json:"message_count"`
	// Goal is the active /goal condition, if any, so resuming a session
	// restores its pending goal.
	Goal string `json:"goal,omitempty"`
}

// Record is one stored message plus display-only metadata. Display holds
// what the user actually typed for user messages (the wire Content may
// carry expanded @file blocks or embedded skill instructions the user never
// saw), so a resumed transcript can be replayed exactly as it looked live.
// The embedded Message flattens into the same JSON keys as before, keeping
// old session files loadable.
type Record struct {
	provider.Message
	Display string `json:"display,omitempty"`
}

// Session is a full recorded conversation, without the system prompt (the
// prompt is rebuilt fresh on resume so new memories and skills apply).
type Session struct {
	Meta
	Messages []Record `json:"messages"`
}

// ProviderMessages strips display metadata for feeding history back to the
// agent.
func (s *Session) ProviderMessages() []provider.Message {
	out := make([]provider.Message, len(s.Messages))
	for i, r := range s.Messages {
		out[i] = r.Message
	}
	return out
}

// DisplayInputs returns what the user typed for each user-role message, in
// order, falling back to the wire content for records saved before Display
// existed.
func (s *Session) DisplayInputs() []string {
	var out []string
	for _, r := range s.Messages {
		if r.Role == provider.RoleUser {
			if r.Display != "" {
				out = append(out, r.Display)
			} else {
				out = append(out, r.Content)
			}
		}
	}
	return out
}

// Store persists sessions. The REPL depends on this interface (DIP).
type Store interface {
	// List returns session summaries, most recently updated first.
	List() ([]Meta, error)
	Load(id string) (*Session, error)
	Save(s *Session) error
	Delete(id string) error
}

// FileStore keeps one JSON file per session under Dir.
type FileStore struct {
	Dir string
}

// NewProjectStore returns the session store for a project.
//
// Sessions live under the *user's* agent home, keyed by project path —
// never inside the project itself. A repository is shared with a team,
// while conversation history is personal: storing it in the working tree
// would leak one developer's transcripts to everyone and clutter the repo.
// This mirrors where Claude Code and codex keep their history.
//
// Any history left at the old project-local path is migrated on first use
// so upgrading does not orphan it.
func NewProjectStore(projectDir string) *FileStore {
	dir := ProjectDir(projectDir)
	migrateLegacyStore(projectDir, dir)
	return &FileStore{Dir: dir}
}

// ProjectDir returns the user-level directory holding one project's
// sessions.
func ProjectDir(projectDir string) string {
	return home.Path("projects", EncodeProjectPath(projectDir), "sessions")
}

// AuditLogPath returns the user-level audit-log file for one project, kept
// beside its sessions (out of the shared working tree).
func AuditLogPath(projectDir string) string {
	return home.Path("projects", EncodeProjectPath(projectDir), "audit.log")
}

// UsagePath returns the user-level usage/cost totals file for one project.
func UsagePath(projectDir string) string {
	return home.Path("projects", EncodeProjectPath(projectDir), "usage.json")
}

// EncodeProjectPath turns an absolute project path into a single directory
// name by replacing path separators with dashes, so the origin stays
// legible when browsing the agent home.
//
// Distinct paths can in principle collide (a directory literally named
// with a dash), which is the same tradeoff Claude Code makes; legibility
// is worth more here than collision-proof hashing.
func EncodeProjectPath(projectDir string) string {
	if abs, err := filepath.Abs(projectDir); err == nil {
		projectDir = abs
	}
	projectDir = filepath.Clean(projectDir)
	var b strings.Builder
	for _, r := range projectDir {
		switch r {
		case '/', '\\', ':':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" || name == "-" {
		return "root"
	}
	return name
}

// migrateLegacyStore moves history from the old in-project location to the
// user-level one, once. It is best-effort: a failure leaves the old files
// untouched rather than risking data loss, and the new store simply starts
// empty.
func migrateLegacyStore(projectDir, newDir string) {
	if projectDir == "" {
		return
	}
	legacy := filepath.Join(projectDir, ".agent", "sessions")
	entries, err := os.ReadDir(legacy)
	if err != nil || len(entries) == 0 {
		return
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return
	}
	moved := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		src := filepath.Join(legacy, e.Name())
		dst := filepath.Join(newDir, e.Name())
		if _, err := os.Stat(dst); err == nil {
			continue // already migrated; never overwrite
		}
		if err := os.Rename(src, dst); err != nil {
			// Fall back to copying across filesystems.
			data, rerr := os.ReadFile(src)
			if rerr != nil || os.WriteFile(dst, data, 0o644) != nil {
				continue
			}
			os.Remove(src)
		}
		moved++
	}
	if moved > 0 {
		// Remove the directory only when nothing was left behind.
		if rest, err := os.ReadDir(legacy); err == nil && len(rest) == 0 {
			os.Remove(legacy)
		}
	}
}

// NewID mints a sortable, collision-resistant session identifier like
// "20260720-153012-a1b2".
func NewID(now time.Time) string {
	suffix := make([]byte, 2)
	rand.Read(suffix)
	return fmt.Sprintf("%s-%s", now.Format("20060102-150405"), hex.EncodeToString(suffix))
}

// TitleFrom derives a session title from the first user input: first line,
// trimmed to a listing-friendly display width. Truncation is width-aware so
// multi-byte text is never cut mid-rune.
func TitleFrom(input string) string {
	title, _, _ := strings.Cut(strings.TrimSpace(input), "\n")
	return textwidth.Truncate(title, 60)
}

// Save writes the session file, stamping UpdatedAt.
func (s *FileStore) Save(sess *Session) error {
	if sess.ID == "" {
		return fmt.Errorf("session has no ID")
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	sess.UpdatedAt = time.Now()
	sess.MessageCount = len(sess.Messages)
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(sess.ID), append(data, '\n'), 0o644)
}

// List reads every session's metadata, newest update first.
func (s *FileStore) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		sess, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue // a corrupt file should not break listing
		}
		out = append(out, sess.Meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Load reads one session. A unique ID prefix is accepted, so users can type
// the short form shown in listings.
func (s *FileStore) Load(id string) (*Session, error) {
	data, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		if full, ferr := s.resolvePrefix(id); ferr == nil {
			data, err = os.ReadFile(s.path(full))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("session %q not found", id)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session %q is corrupt: %w", id, err)
	}
	return &sess, nil
}

// Delete removes one session by ID (exact match only, to be safe).
func (s *FileStore) Delete(id string) error {
	if err := os.Remove(s.path(id)); err != nil {
		return fmt.Errorf("session %q not found", id)
	}
	return nil
}

// resolvePrefix finds the single session ID starting with prefix.
func (s *FileStore) resolvePrefix(prefix string) (string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return "", err
	}
	var match string
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasPrefix(id, prefix) {
			if match != "" {
				return "", fmt.Errorf("ambiguous session prefix %q", prefix)
			}
			match = id
		}
	}
	if match == "" {
		return "", fmt.Errorf("no session matches %q", prefix)
	}
	return match, nil
}

func (s *FileStore) path(id string) string {
	return filepath.Join(s.Dir, id+".json")
}
