// Package session holds the documents a viewer has open. A viewer that can only
// ever show the one file named on the command line is not something you "open
// files in"; it is something you launch once per file. This is what lets one
// running sqldoc behave like a window you keep around and feed files to.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/darianmavgo/sqldoc/internal/doc"
)

// Entry is one open document.
type Entry struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Opened    time.Time `json:"opened"`
	Ephemeral bool      `json:"ephemeral"` // a dropped copy living in a temp dir

	Doc *doc.Doc `json:"-"`
}

// Session is the set of open documents, plus the list of ones opened before.
type Session struct {
	opt doc.Options

	mu    sync.RWMutex
	docs  map[string]*Entry
	order []string

	recents *Recents
	tempDir string
}

// New creates an empty session.
func New(opt doc.Options) *Session {
	return &Session{
		opt:     opt,
		docs:    map[string]*Entry{},
		recents: loadRecents(),
	}
}

// ID is the stable identifier for a path. The same file opened twice is the
// same document, so a second Open of something already on screen selects it
// rather than loading a duplicate.
func ID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:12]
}

// Open opens a database and adds it to the session, or returns the existing
// entry if that file is already open.
func (s *Session) Open(path string) (*Entry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	id := ID(abs)

	s.mu.RLock()
	if e, ok := s.docs[id]; ok {
		s.mu.RUnlock()
		s.recents.touch(e)
		return e, nil
	}
	s.mu.RUnlock()

	d, err := doc.Open(abs, s.opt)
	if err != nil {
		return nil, err
	}

	e := &Entry{
		ID:     id,
		Path:   abs,
		Name:   filepath.Base(abs),
		Size:   d.Size,
		Opened: time.Now(),
		Doc:    d,
	}

	s.mu.Lock()
	// Another request may have opened the same file while this one was reading
	// the header; keep whichever landed first and discard this handle.
	if existing, ok := s.docs[id]; ok {
		s.mu.Unlock()
		d.Close()
		return existing, nil
	}
	s.docs[id] = e
	s.order = append(s.order, id)
	s.mu.Unlock()

	s.recents.touch(e)
	return e, nil
}

// OpenTemp adopts a file the viewer itself wrote — a database dropped into the
// window, which the browser could only hand over as bytes. It is tracked so it
// can be deleted on close and kept out of the recent list, where a path under
// /tmp would be a dead link tomorrow.
func (s *Session) OpenTemp(path string) (*Entry, error) {
	e, err := s.Open(path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	e.Ephemeral = true
	s.mu.Unlock()
	s.recents.forget(e.Path)
	return e, nil
}

// Get returns an open document.
func (s *Session) Get(id string) (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.docs[id]
	return e, ok
}

// First returns the document to show when none was asked for.
func (s *Session) First() (*Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return nil, false
	}
	e, ok := s.docs[s.order[0]]
	return e, ok
}

// List returns the open documents in the order they were opened.
func (s *Session) List() []*Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Entry, 0, len(s.order))
	for _, id := range s.order {
		if e, ok := s.docs[id]; ok {
			out = append(out, e)
		}
	}
	return out
}

// Close closes one document, deleting it if it was a dropped copy.
func (s *Session) Close(id string) error {
	s.mu.Lock()
	e, ok := s.docs[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("no such document")
	}
	delete(s.docs, id)
	for i, v := range s.order {
		if v == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()

	err := e.Doc.Close()
	if e.Ephemeral {
		// The drop owns its whole subdirectory, so remove that rather than
		// leaving an empty directory behind for every file ever dropped.
		os.RemoveAll(filepath.Dir(e.Path))
	}
	return err
}

// CloseAll releases everything, removing any dropped copies.
func (s *Session) CloseAll() {
	for _, e := range s.List() {
		s.Close(e.ID)
	}
	if s.tempDir != "" {
		os.RemoveAll(s.tempDir)
	}
}

// TempFile returns a path in this session's scratch directory for a dropped
// file. Each drop gets its own subdirectory so the file keeps the name it was
// dropped under: that name is what the viewer shows in the document switcher,
// and a uniquifying prefix on the file itself would be visible there. The whole
// directory is removed when the session ends.
func (s *Session) TempFile(name string) (string, error) {
	s.mu.Lock()
	if s.tempDir == "" {
		dir, err := os.MkdirTemp("", "sqldoc-")
		if err != nil {
			s.mu.Unlock()
			return "", err
		}
		s.tempDir = dir
	}
	root := s.tempDir
	s.mu.Unlock()

	// Only the base name is kept: a dropped file's name is attacker-controlled
	// in the sense that it comes from outside, and joining a path with "../" in
	// it would write outside the scratch directory.
	base := filepath.Base(filepath.FromSlash(name))
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		base = "dropped.db"
	}

	dir, err := os.MkdirTemp(root, "drop-")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, base), nil
}

// Recents returns the recently opened files that still exist.
func (s *Session) Recents() []Recent { return s.recents.list() }

// ForgetRecent drops one entry from the recent list.
func (s *Session) ForgetRecent(path string) { s.recents.forget(path) }

// sortEntries keeps the most recently opened first.
func sortEntries(r []Recent) {
	sort.Slice(r, func(i, j int) bool { return r[i].Opened.After(r[j].Opened) })
}
