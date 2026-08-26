package doc

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Count is what the viewer knows about a table's size right now. A viewer that
// waits for an exact count before painting is a viewer that stares at a spinner
// on any large table, so an inexact count is published immediately and refined
// in the background.
type Count struct {
	Rows  int64 `json:"rows"`
	Exact bool  `json:"exact"`
	// Known is false only when even an estimate was impossible, in which case
	// the viewer shows an open-ended document until the exact count lands.
	Known bool `json:"known"`
}

type counter struct {
	mu      sync.Mutex
	c       Count
	started bool
}

// Count returns the best known size of a table without blocking, and starts an
// exact count in the background the first time it is asked.
func (d *Doc) Count(name string) Count {
	d.mu.Lock()
	ct, ok := d.counts[name]
	if !ok {
		ct = &counter{}
		d.counts[name] = ct
	}
	d.mu.Unlock()

	ct.mu.Lock()
	defer ct.mu.Unlock()

	if !ct.started {
		ct.started = true
		ct.c = d.estimate(name)
		go d.exactCount(name, ct)
	}
	return ct.c
}

// estimate produces a row count in O(1), or gives up. It never scans.
func (d *Doc) estimate(name string) Count {
	// ANALYZE leaves a row-count estimate in sqlite_stat1 as the first token of
	// the stat column. If the document was analysed, this is free and good.
	if n, ok := d.statEstimate(name); ok {
		return Count{Rows: n, Known: true}
	}

	// Otherwise, for a rowid table, max(rowid) is an index seek and an exact
	// upper bound. On a table that was never deleted from it is the true count.
	if t, ok := d.Table(name); ok && t.HasRowID {
		lo, hi, ok := d.bounds(name)
		if ok {
			return Count{Rows: hi - lo + 1, Known: true}
		}
	}

	return Count{Known: false}
}

func (d *Doc) statEstimate(name string) (int64, bool) {
	var stat string
	err := d.fg.QueryRow(
		`SELECT stat FROM sqlite_stat1 WHERE tbl = ? AND idx IS NULL LIMIT 1`, name).Scan(&stat)
	if err != nil {
		// idx IS NULL is the table-level row; fall back to any index's row,
		// whose first token is also the table row count.
		if err = d.fg.QueryRow(
			`SELECT stat FROM sqlite_stat1 WHERE tbl = ? LIMIT 1`, name).Scan(&stat); err != nil {
			return 0, false
		}
	}
	first, _, _ := strings.Cut(stat, " ")
	n, err := strconv.ParseInt(first, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// exactCount runs COUNT(*) on the background handle. It is the only query in
// the program allowed to take time proportional to the table, so it yields to
// the foreground first: on a large cold file the scan and the first window
// compete for the same disk, and the window has to win.
func (d *Doc) exactCount(name string, ct *counter) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := d.waitIdle(ctx); err != nil {
		return
	}

	var n int64
	q := "SELECT COUNT(*) FROM " + quoteIdent(name)
	if err := d.bg.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return
	}
	ct.mu.Lock()
	ct.c = Count{Rows: n, Exact: true, Known: true}
	ct.mu.Unlock()
}

// bounds returns min and max rowid, cached.
//
// The two aggregates are deliberately written as separate scalar subqueries.
// SQLite's min/max optimisation, which turns the aggregate into a single index
// seek, only fires for a query with exactly one aggregate: asking for
// "min(rowid), max(rowid)" together silently degrades into a full table scan.
// On a 5M-row table that is the difference between 5ms and 350ms, and it would
// be paid before the first row could be drawn.
func (d *Doc) bounds(name string) (lo, hi int64, ok bool) {
	ts := d.state(name)
	ts.boundsOnce.Do(func() {
		var a, b sql.NullInt64
		q := "SELECT (SELECT min(rowid) FROM " + quoteIdent(name) + ")," +
			" (SELECT max(rowid) FROM " + quoteIdent(name) + ")"
		if err := d.fg.QueryRow(q).Scan(&a, &b); err != nil {
			return
		}
		if !a.Valid || !b.Valid {
			return // empty table
		}
		ts.minID, ts.maxID, ts.hasBounds = a.Int64, b.Int64, true
	})
	return ts.minID, ts.maxID, ts.hasBounds
}

// Ordinal reports the position of a rowid within the table, which is what the
// viewer needs to scroll to a search hit. On a dense table it is arithmetic; on
// a sparse one it costs an index walk, which is acceptable because it happens
// once per jump rather than once per frame.
func (d *Doc) Ordinal(ctx context.Context, table string, rowid int64) (int64, error) {
	t, ok := d.Table(table)
	if !ok || !t.HasRowID {
		return 0, nil
	}
	lo, hi, ok := d.bounds(table)
	if !ok {
		return 0, nil
	}
	if c := d.Count(table); c.Known && c.Exact && c.Rows == hi-lo+1 {
		return rowid - lo, nil
	}
	var n int64
	q := "SELECT COUNT(*) FROM " + quoteIdent(table) + " WHERE rowid < ?"
	if err := d.bg.QueryRowContext(ctx, q, rowid).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
