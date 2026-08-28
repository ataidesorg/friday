// Package session persists a conversation as a directory under the Friday
// home: an append-only, redacted transcript.jsonl (one Turn per line) and a
// meta.json header, so a chat resumes across launches. Nothing leaves the
// local machine, and the only free text written is passed through
// internal/redact first. Persistence is plain JSON lines, not a database:
// resume needs append and replay, not query.
package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ataidesorg/friday/internal/core"
	"github.com/ataidesorg/friday/internal/redact"
)

const (
	dirPerm        = 0o700
	filePerm       = 0o600
	transcriptFile = "transcript.jsonl"
	metaFile       = "meta.json"
	todosFile      = "todos.json"
)

// Turn is one exchange in a conversation: a user prompt or an assistant
// reply, with the model and accounting that produced it. A Turn is immutable
// once appended.
type Turn struct {
	Role  core.Role       `json:"role"`
	Kind  string          `json:"kind,omitempty"` // "" ordinary; KindSummary compacted context
	Text  string          `json:"text"`
	Model string          `json:"model,omitempty"`
	Usage core.Usage      `json:"usage"`
	Cost  core.CostReport `json:"cost"`
	TS    time.Time       `json:"ts"`
}

// Meta is the header of one session, held in meta.json.
type Meta struct {
	ID           string    `json:"id"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
	Title        string    `json:"title,omitempty"`
	DefaultRoute string    `json:"default_route,omitempty"`
	Mode         string    `json:"mode,omitempty"` // code | plan | ask; empty means code
	Turns        int       `json:"turns"`
}

// Store reads and writes sessions under a sessions/ root. It holds no mutable
// session state; every method returns fresh values.
type Store struct {
	root string
	red  *redact.Redactor
}

// NewStore roots sessions at <home>/sessions. The redactor is required: no
// transcript line is written without passing through it (fail closed).
func NewStore(home string, red *redact.Redactor) (*Store, error) {
	if red == nil {
		return nil, fmt.Errorf("%w: session store requires a redactor", core.ErrInvalidInput)
	}
	if home == "" {
		return nil, fmt.Errorf("%w: session store requires a home directory", core.ErrInvalidInput)
	}
	return &Store{root: filepath.Join(home, "sessions"), red: red}, nil
}

// Root is the sessions/ directory this store manages.
func (s *Store) Root() string { return s.root }

func (s *Store) dir(id string) string { return filepath.Join(s.root, id) }

// Create starts a new session directory and writes its meta header.
func (s *Store) Create(id string, now time.Time) (Meta, error) {
	if id == "" {
		return Meta{}, fmt.Errorf("%w: session id is empty", core.ErrInvalidInput)
	}
	m := Meta{ID: id, Created: now, Updated: now}
	if err := s.writeMeta(m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

// Append writes one redacted Turn to the transcript and advances the meta
// header (Turns, Updated), returning the updated Meta. The caller's Turn is
// never mutated: a redacted copy is what reaches disk.
func (s *Store) Append(id string, t Turn, now time.Time) (Meta, error) {
	m, err := s.loadMeta(id)
	if err != nil {
		return Meta{}, err
	}
	safe := Turn{
		Role:  t.Role,
		Kind:  t.Kind,
		Text:  s.red.Redact(t.Text),
		Model: t.Model,
		Usage: t.Usage,
		Cost:  t.Cost,
		TS:    t.TS,
	}
	line, err := json.Marshal(safe)
	if err != nil {
		return Meta{}, fmt.Errorf("marshal turn: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(s.dir(id), transcriptFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm) //nolint:gosec // transcript under the Friday home
	if err != nil {
		return Meta{}, fmt.Errorf("open transcript: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return Meta{}, fmt.Errorf("write transcript: %w", err)
	}
	if err := f.Close(); err != nil {
		return Meta{}, fmt.Errorf("close transcript: %w", err)
	}
	next := m
	next.Turns = m.Turns + 1
	next.Updated = now
	if err := s.writeMeta(next); err != nil {
		return Meta{}, err
	}
	return next, nil
}

// Load returns a session's meta and every turn in order.
func (s *Store) Load(id string) (Meta, []Turn, error) {
	m, err := s.loadMeta(id)
	if err != nil {
		return Meta{}, nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir(id), transcriptFile))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil, nil // a created-but-empty session has no transcript yet
	}
	if err != nil {
		return Meta{}, nil, fmt.Errorf("read transcript: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	last := -1
	for i, raw := range lines {
		if len(raw) > 0 {
			last = i
		}
	}
	var turns []Turn
	for i, raw := range lines {
		if len(raw) == 0 {
			continue
		}
		var t Turn
		if err := json.Unmarshal(raw, &t); err != nil {
			if i == last {
				// A crash mid-Append can truncate the final line; skip it so a
				// resumable session is not bricked. Interior corruption still
				// hard-fails — it signals real damage, not an aborted write.
				break
			}
			return Meta{}, nil, fmt.Errorf("transcript line %d: %w", i+1, err)
		}
		turns = append(turns, t)
	}
	return m, turns, nil
}

// List returns every session's meta, newest Updated first.
func (s *Store) List() ([]Meta, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}
	out := make([]Meta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := s.loadMeta(e.Name())
		if err != nil {
			continue // a directory without a valid meta is not a session
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Latest returns the most recently updated session, if any exists.
func (s *Store) Latest() (Meta, bool, error) {
	all, err := s.List()
	if err != nil {
		return Meta{}, false, err
	}
	if len(all) == 0 {
		return Meta{}, false, nil
	}
	return all[0], true, nil
}

// Meta returns a session's header without replaying its transcript.
func (s *Store) Meta(id string) (Meta, error) { return s.loadMeta(id) }

// SetDefaultRoute records a per-session route override on the meta header, so
// the next launch of this session resumes on the route the user last chose.
func (s *Store) SetDefaultRoute(id, route string, now time.Time) (Meta, error) {
	m, err := s.loadMeta(id)
	if err != nil {
		return Meta{}, err
	}
	next := m
	next.DefaultRoute = route
	next.Updated = now
	if err := s.writeMeta(next); err != nil {
		return Meta{}, err
	}
	return next, nil
}

// SetMode records the Shift+Tab session mode on the meta header.
func (s *Store) SetMode(id, mode string, now time.Time) (Meta, error) {
	m, err := s.loadMeta(id)
	if err != nil {
		return Meta{}, err
	}
	next := m
	next.Mode = mode
	next.Updated = now
	if err := s.writeMeta(next); err != nil {
		return Meta{}, err
	}
	return next, nil
}

// SetTitle records a manual session title on the meta header.
func (s *Store) SetTitle(id, title string, now time.Time) (Meta, error) {
	m, err := s.loadMeta(id)
	if err != nil {
		return Meta{}, err
	}
	next := m
	next.Title = title
	next.Updated = now
	if err := s.writeMeta(next); err != nil {
		return Meta{}, err
	}
	return next, nil
}

// Truncate rewrites the transcript so only the first keep turns remain.
// keep is clamped to [0, len(turns)].
func (s *Store) Truncate(id string, keep int, now time.Time) (Meta, error) {
	m, turns, err := s.Load(id)
	if err != nil {
		return Meta{}, err
	}
	if keep < 0 {
		keep = 0
	}
	if keep > len(turns) {
		keep = len(turns)
	}
	path := filepath.Join(s.dir(id), transcriptFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm) //nolint:gosec // transcript under the Friday home
	if err != nil {
		return Meta{}, fmt.Errorf("open truncated transcript: %w", err)
	}
	for _, t := range turns[:keep] {
		line, err := json.Marshal(t)
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return Meta{}, fmt.Errorf("marshal turn: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return Meta{}, fmt.Errorf("write transcript: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return Meta{}, fmt.Errorf("close transcript: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return Meta{}, fmt.Errorf("replace transcript: %w", err)
	}
	next := m
	next.Turns = keep
	next.Updated = now
	if err := s.writeMeta(next); err != nil {
		return Meta{}, err
	}
	return next, nil
}

// Fork copies src's transcript into a new session dst.
func (s *Store) Fork(src, dst string, now time.Time) (Meta, error) {
	_, turns, err := s.Load(src)
	if err != nil {
		return Meta{}, err
	}
	m, err := s.Create(dst, now)
	if err != nil {
		return Meta{}, err
	}
	for _, t := range turns {
		m, err = s.Append(dst, t, now)
		if err != nil {
			return Meta{}, err
		}
	}
	return m, nil
}

func (s *Store) writeMeta(m Meta) error {
	if err := os.MkdirAll(s.dir(m.ID), dirPerm); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir(m.ID), metaFile), data, filePerm); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	return nil
}

func (s *Store) loadMeta(id string) (Meta, error) {
	data, err := os.ReadFile(filepath.Join(s.dir(id), metaFile))
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, fmt.Errorf("%w: session %q", core.ErrNotFound, id)
	}
	if err != nil {
		return Meta{}, fmt.Errorf("read meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse meta for session %q: %w", id, err)
	}
	return m, nil
}

// TodoItem is one session task list entry (todo_write).
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// LoadTodos returns the session task list; missing file is empty.
func (s *Store) LoadTodos(id string) ([]TodoItem, error) {
	if _, err := s.loadMeta(id); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir(id), todosFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read todos: %w", err)
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("parse todos: %w", err)
	}
	return items, nil
}

// SaveTodos writes the session task list (redacted content).
func (s *Store) SaveTodos(id string, items []TodoItem) error {
	if _, err := s.loadMeta(id); err != nil {
		return err
	}
	safe := make([]TodoItem, len(items))
	for i, it := range items {
		safe[i] = TodoItem{ID: it.ID, Content: s.red.Redact(it.Content), Status: it.Status}
	}
	data, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal todos: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir(id), todosFile), append(data, '\n'), filePerm)
}

// Delete removes a session directory permanently.
func (s *Store) Delete(id string) error {
	if _, err := s.loadMeta(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.dir(id)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
