package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// maxRecents is how many past documents the start page offers.
const maxRecents = 24

// Recent is a document opened at some point in the past.
type Recent struct {
	Path   string    `json:"path"`
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Opened time.Time `json:"opened"`
}

// Recents is the persisted list of previously opened documents. It is stored as
// JSON rather than in a SQLite file of its own: this program's one hard promise
// is that it never writes to a database, and keeping its own state somewhere
// else is the simplest way to keep that promise obviously true.
type Recents struct {
	mu    sync.Mutex
	path  string
	list_ []Recent
}

func recentsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "sqldoc", "recent.json")
}

func loadRecents() *Recents {
	r := &Recents{path: recentsPath()}
	if r.path == "" {
		return r
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return r
	}
	_ = json.Unmarshal(b, &r.list_)
	return r
}

// list returns the recent documents that are still on disk. A file that has
// been moved or deleted is dropped rather than offered as a link that fails.
func (r *Recents) list() []Recent {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Recent, 0, len(r.list_))
	changed := false
	for _, e := range r.list_ {
		st, err := os.Stat(e.Path)
		if err != nil {
			changed = true
			continue
		}
		e.Size = st.Size()
		out = append(out, e)
	}
	if changed {
		r.list_ = out
		r.save()
	}
	sortEntries(out)
	return out
}

func (r *Recents) touch(e *Entry) {
	if r.path == "" || e.Ephemeral {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	next := []Recent{{Path: e.Path, Name: e.Name, Size: e.Size, Opened: time.Now()}}
	for _, old := range r.list_ {
		if old.Path == e.Path {
			continue
		}
		next = append(next, old)
		if len(next) >= maxRecents {
			break
		}
	}
	r.list_ = next
	r.save()
}

func (r *Recents) forget(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.list_[:0]
	for _, e := range r.list_ {
		if e.Path != path {
			out = append(out, e)
		}
	}
	r.list_ = out
	r.save()
}

// save writes the list, ignoring failures: not being able to remember recent
// files is a small loss, and never a reason to interrupt someone reading a
// database.
func (r *Recents) save() {
	if r.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(r.list_, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, r.path)
	}
}
