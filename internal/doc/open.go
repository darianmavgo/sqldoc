// Package doc opens a SQLite file as a read-only document and serves windows of
// it to a viewer. Everything here is built around one rule: never do work
// proportional to the size of the database before the first screen is painted.
package doc

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// connectPragmas tune the connection for sequential read throughput. They are
// applied via the DSN on modernc and by applyPragmas on cgo builds.
var connectPragmas = []string{
	"query_only(1)",        // hard guarantee this process never writes
	"cache_size(-65536)",   // 64 MiB of page cache, negative means KiB
	"mmap_size(268435456)", // 256 MiB memory-mapped, avoids read() syscalls
	"temp_store(2)",        // MEMORY
	"busy_timeout(5000)",
}

// Doc is an open, read-only SQLite document.
type Doc struct {
	Path     string
	Size     int64
	Modified time.Time

	// fg serves scroll requests. bg serves counting and search so a slow
	// COUNT(*) can never sit in front of a scroll.
	fg *sql.DB
	bg *sql.DB

	tables []Table
	style  Style

	mu        sync.Mutex
	states    map[string]*tableState
	counts    map[string]*counter
	colWidths map[string]*colWidthState

	// lastRead is when the foreground last ran a query, in Unix nanoseconds.
	// Background work waits for a lull in it so a full-table count can never
	// take disk bandwidth away from the rows someone is trying to look at.
	lastRead atomic.Int64
}

// idleGrace is how quiet the foreground must be before background work starts.
const idleGrace = 300 * time.Millisecond

// touch records foreground activity.
func (d *Doc) touch() { d.lastRead.Store(time.Now().UnixNano()) }

// waitIdle blocks until the foreground has been quiet for idleGrace.
func (d *Doc) waitIdle(ctx context.Context) error {
	for {
		since := time.Since(time.Unix(0, d.lastRead.Load()))
		if since >= idleGrace {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(idleGrace - since):
		}
	}
}

// Options controls how a document is opened.
type Options struct {
	// Immutable promises SQLite that the file will not change while open. It
	// skips locking and WAL recovery entirely and is the single biggest
	// cold-start win, but it corrupts reads if another process writes the file.
	// Off by default; opt in for static documents.
	Immutable bool
}

// Open opens path read-only and reads just enough metadata to paint. It does
// not count rows: see Doc.Count.
func Open(path string, opt Options) (*Doc, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", abs)
	}

	d := &Doc{
		Path:     abs,
		Size:     st.Size(),
		Modified: st.ModTime(),
		states:   map[string]*tableState{},
		counts:   map[string]*counter{},
	}

	if d.fg, err = openHandle(abs, opt.Immutable); err != nil {
		return nil, err
	}
	if d.bg, err = openHandle(abs, opt.Immutable); err != nil {
		d.fg.Close()
		return nil, err
	}

	// One cheap round trip that also proves the file is really a database.
	// A malformed file fails here rather than halfway through rendering.
	var jm string
	if err := d.fg.QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		d.Close()
		return nil, fmt.Errorf("%s is not a SQLite database: %w", filepath.Base(abs), err)
	}

	if d.tables, err = d.discover(); err != nil {
		d.Close()
		return nil, err
	}
	d.style = d.loadStyle()
	return d, nil
}

// openHandle returns a *sql.DB pinned to a single connection. One connection per
// handle keeps the prepared-statement cache hot and means pragmas applied once
// stay applied, which is not true of a multi-connection pool.
func openHandle(path string, immutable bool) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn(path, immutable))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := applyPragmas(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// applyPragmas is a no-op where the DSN already carried the pragmas, and the
// real thing where it could not. Failures are not fatal: every pragma here is a
// performance hint, and a database that rejects one still reads correctly.
func applyPragmas(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, p := range connectPragmas {
		name, arg, ok := splitPragma(p)
		if !ok {
			continue
		}
		_, _ = db.ExecContext(ctx, "PRAGMA "+name+"="+arg)
	}
	return ctx.Err()
}

func splitPragma(p string) (name, arg string, ok bool) {
	i := len(p) - 1
	if i < 0 || p[i] != ')' {
		return "", "", false
	}
	j := 0
	for j < len(p) && p[j] != '(' {
		j++
	}
	if j >= len(p) {
		return "", "", false
	}
	return p[:j], p[j+1 : i], true
}

// Tables returns the document's tables in display order.
func (d *Doc) Tables() []Table { return d.tables }

// Style returns presentation metadata carried by the document itself.
func (d *Doc) Style() Style { return d.style }

// Table looks up a table by name.
func (d *Doc) Table(name string) (Table, bool) {
	for _, t := range d.tables {
		if t.Name == name {
			return t, true
		}
	}
	return Table{}, false
}

// Close releases both handles.
func (d *Doc) Close() error {
	var err error
	if d.fg != nil {
		err = d.fg.Close()
	}
	if d.bg != nil {
		if e := d.bg.Close(); err == nil {
			err = e
		}
	}
	return err
}
