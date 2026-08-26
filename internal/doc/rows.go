package doc

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Window describes the slice of a table the viewer wants. Exactly one of After
// or Offset is used, and which one is chosen decides how fast the answer is.
type Window struct {
	Table string
	Limit int

	// After anchors the window at a rowid. This is the fast path: an index seek
	// regardless of how deep into the table it points. Sequential scrolling
	// uses it, so scrolling stays O(log n) per screen forever.
	After    int64
	UseAfter bool

	// Offset anchors the window at an ordinal position, which is what a
	// scrollbar drag produces. On a rowid table it is converted to a rowid by
	// interpolation and stays O(log n); otherwise it falls back to SQL OFFSET,
	// which is O(offset).
	Offset int64

	// Sort, when set, forces the OFFSET path because rowid order no longer
	// matches display order.
	Sort string
	Desc bool

	// ForceOffset takes the plain SQL OFFSET path even where a faster one
	// exists. Nothing in the viewer sets it; it exists so the benchmark can
	// measure the strategy this package avoids.
	ForceOffset bool
}

// Page is one rendered window of a table.
type Page struct {
	Table   string   `json:"table"`
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
	RowIDs  []int64  `json:"rowids,omitempty"`

	// Start is the ordinal of the first row in Rows. Approx is true when it was
	// interpolated rather than counted, which happens on a scrollbar jump into
	// a table whose rowids have gaps.
	Start  int64 `json:"start"`
	Approx bool  `json:"approx"`

	// Path names the strategy used, so the viewer and the benchmark can both
	// see whether the fast path was taken.
	Path string `json:"path"`
	// Micros is server-side query plus format time.
	Micros int64 `json:"micros"`
}

const maxLimit = 1000

// Rows fetches one window of a table.
func (d *Doc) Rows(ctx context.Context, w Window) (*Page, error) {
	start := time.Now()
	d.touch()
	defer d.touch()

	t, ok := d.Table(w.Table)
	if !ok {
		return nil, fmt.Errorf("no such table: %s", w.Table)
	}
	ts := d.state(w.Table)
	if ts.err != nil {
		return nil, ts.err
	}
	if w.Limit <= 0 || w.Limit > maxLimit {
		w.Limit = 100
	}
	if w.Offset < 0 {
		w.Offset = 0
	}

	p := &Page{Table: w.Table, Columns: ts.cols}

	var (
		query string
		args  []any
	)

	switch {
	case w.Sort != "":
		query, args = d.sortedQuery(ts, w)
		p.Start, p.Path = w.Offset, "sorted-offset"

	case w.ForceOffset:
		query = "SELECT " + ts.colExpr + " FROM " + ts.quoted + " LIMIT ? OFFSET ?"
		args = []any{w.Limit, w.Offset}
		p.Start, p.Path = w.Offset, "offset"

	case t.HasRowID && w.UseAfter:
		// Sequential scroll. Exact and cheap.
		query = "SELECT rowid," + ts.colExpr + " FROM " + ts.quoted +
			" WHERE rowid > ? ORDER BY rowid LIMIT ?"
		args = []any{w.After, w.Limit}
		p.Start, p.Path = w.Offset, "keyset"

	case t.HasRowID:
		// Scrollbar jump. Interpolate the ordinal onto the rowid axis so the
		// seek is an index lookup instead of a scan of everything above it.
		rid, exact := d.rowidForOffset(w.Table, w.Offset)
		query = "SELECT rowid," + ts.colExpr + " FROM " + ts.quoted +
			" WHERE rowid >= ? ORDER BY rowid LIMIT ?"
		args = []any{rid, w.Limit}
		p.Start, p.Approx, p.Path = w.Offset, !exact, "interpolated"

	default:
		// Views and WITHOUT ROWID tables have no cheap ordinal axis.
		query = "SELECT " + ts.colExpr + " FROM " + ts.quoted + " LIMIT ? OFFSET ?"
		args = []any{w.Limit, w.Offset}
		p.Start, p.Path = w.Offset, "offset"
	}

	withRowID := p.Path == "keyset" || p.Path == "interpolated"
	if err := d.scanInto(ctx, p, query, args, withRowID); err != nil {
		return nil, err
	}

	p.Micros = time.Since(start).Microseconds()
	return p, nil
}

func (d *Doc) sortedQuery(ts *tableState, w Window) (string, []any) {
	dir := " ASC"
	if w.Desc {
		dir = " DESC"
	}
	q := "SELECT " + ts.colExpr + " FROM " + ts.quoted +
		" ORDER BY " + quoteIdent(w.Sort) + dir + " LIMIT ? OFFSET ?"
	return q, []any{w.Limit, w.Offset}
}

// scanInto runs the query and formats every cell for display. Buffers are
// reused across rows so a full window costs one allocation per cell rather than
// several.
func (d *Doc) scanInto(ctx context.Context, p *Page, query string, args []any, withRowID bool) error {
	rows, err := d.fg.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := len(p.Columns)
	vals := make([]any, n+boolToInt(withRowID))
	dest := make([]any, len(vals))
	for i := range vals {
		dest[i] = &vals[i]
	}

	p.Rows = make([][]any, 0, 128)
	if withRowID {
		p.RowIDs = make([]int64, 0, 128)
	}

	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		cells := vals
		if withRowID {
			if id, ok := vals[0].(int64); ok {
				p.RowIDs = append(p.RowIDs, id)
			} else {
				p.RowIDs = append(p.RowIDs, 0)
			}
			cells = vals[1:]
		}
		out := make([]any, n)
		for i, v := range cells {
			out[i] = cell(v)
		}
		p.Rows = append(p.Rows, out)
	}
	return rows.Err()
}

// blob is how a binary cell reaches the viewer: its size, never its bytes.
// Shipping blob contents would make a window of a table with thumbnails in it
// arbitrarily large.
type blob struct {
	Bytes int `json:"b"`
}

// cell converts a scanned SQLite value into something JSON-encodable and
// directly renderable.
func cell(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case int64, float64, bool, string:
		return t
	case []byte:
		return blob{Bytes: len(t)}
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return fmt.Sprint(t)
	}
}

// rowidForOffset maps an ordinal onto the rowid axis. When rowids are dense —
// the common case for a table that has not been deleted from — the mapping is
// exact and the jump is as correct as a full scan would have been.
func (d *Doc) rowidForOffset(table string, off int64) (rowid int64, exact bool) {
	lo, hi, ok := d.bounds(table)
	if !ok {
		return 0, true // empty table
	}
	if off <= 0 {
		return lo, true
	}
	span := hi - lo + 1
	c := d.Count(table)
	if c.Known && c.Exact && c.Rows == span {
		return lo + off, true // dense: ordinal and rowid are the same axis
	}
	if !c.Known || c.Rows <= 0 {
		return lo + off, false
	}
	if off >= c.Rows {
		return hi, false
	}
	// Linear interpolation across the rowid range.
	return lo + (span*off)/c.Rows, span == c.Rows
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func atoi64(s string, def int64) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
