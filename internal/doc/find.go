package doc

import (
	"context"
	"strings"
	"time"
)

// Match is one hit from a find.
type Match struct {
	RowID  int64 `json:"rowid"`
	Column int   `json:"column"`
	Row    []any `json:"row"`
}

// FindResult is one increment of a search. A find over a large table is not
// answered in one call: it walks a bounded slice of the rowid axis, returns
// what it found, and hands back a cursor. The viewer can render hits as they
// arrive and show honest progress instead of a spinner of unknown length.
type FindResult struct {
	Matches []Match `json:"matches"`
	// Next is the rowid to resume from. Done reports that the table is
	// exhausted and Next is meaningless.
	Next     int64   `json:"next"`
	Done     bool    `json:"done"`
	Scanned  int64   `json:"scanned"`
	Progress float64 `json:"progress"` // 0..1 across the rowid axis
	Micros   int64   `json:"micros"`
}

// findChunk is how much of the rowid axis one Find call is allowed to touch.
// Small enough that any single call returns promptly on a cold cache, large
// enough that a dense table is covered in few round trips.
const findChunk = 250_000

// Find searches every column of a table for a substring, case-insensitively,
// resuming from a rowid cursor.
func (d *Doc) Find(ctx context.Context, table, q string, from int64, limit int) (*FindResult, error) {
	start := time.Now()

	t, ok := d.Table(table)
	if !ok || q == "" {
		return &FindResult{Done: true}, nil
	}
	ts := d.state(table)
	if ts.err != nil {
		return nil, ts.err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// Matches is always a list, never null: a nil slice marshals to JSON null,
	// and a client iterating the result would fault on the first empty chunk of
	// a long search.
	res := &FindResult{Matches: []Match{}}

	if !t.HasRowID {
		// No rowid axis to walk, so this is a single bounded pass. Views are
		// usually small; if one is not, the LIMIT still keeps it interactive.
		rows, err := d.scanFind(ctx, ts, "", limit, nil, q)
		if err != nil {
			return nil, err
		}
		res.Matches, res.Done, res.Progress = nonNil(rows), true, 1
		res.Micros = time.Since(start).Microseconds()
		return res, nil
	}

	lo, hi, ok := d.bounds(table)
	if !ok {
		return &FindResult{Done: true, Progress: 1}, nil
	}
	if from < lo-1 {
		from = lo - 1
	}
	upper := from + findChunk
	if upper >= hi {
		upper = hi
		res.Done = true
	}

	where := "rowid > ? AND rowid <= ? AND "
	args := []any{from, upper}
	matches, err := d.scanFind(ctx, ts, where, limit, args, q)
	if err != nil {
		return nil, err
	}

	res.Matches = nonNil(matches)
	res.Next = upper
	res.Scanned = upper - from
	if span := hi - lo + 1; span > 0 {
		res.Progress = float64(upper-lo+1) / float64(span)
	}
	if res.Progress > 1 {
		res.Progress = 1
	}
	res.Micros = time.Since(start).Microseconds()
	return res, nil
}

// scanFind builds and runs the OR-of-LIKEs. Every column is cast to text so
// numeric and blob columns match on their rendered form, which is what someone
// typing into a find bar expects.
func (d *Doc) scanFind(ctx context.Context, ts *tableState, where string, limit int, args []any, q string) ([]Match, error) {
	var b strings.Builder
	b.WriteString("SELECT rowid," + ts.colExpr + " FROM " + ts.quoted + " WHERE ")
	b.WriteString(where)
	b.WriteByte('(')
	pattern := "%" + escapeLike(q) + "%"
	for i, c := range ts.cols {
		if i > 0 {
			b.WriteString(" OR ")
		}
		b.WriteString("CAST(" + quoteIdent(c.Name) + " AS TEXT) LIKE ? ESCAPE '\\'")
		args = append(args, pattern)
	}
	b.WriteString(") ORDER BY rowid LIMIT ?")
	args = append(args, limit)

	// Search runs on the background handle so a long scan never queues in front
	// of the scroll requests keeping the grid painted.
	rows, err := d.bg.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	n := len(ts.cols)
	vals := make([]any, n+1)
	dest := make([]any, n+1)
	for i := range vals {
		dest[i] = &vals[i]
	}

	needle := strings.ToLower(q)
	var out []Match
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		m := Match{Column: -1, Row: make([]any, n)}
		if id, ok := vals[0].(int64); ok {
			m.RowID = id
		}
		for i, v := range vals[1:] {
			m.Row[i] = cell(v)
			if m.Column < 0 && containsFold(v, needle) {
				m.Column = i
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// nonNil keeps an empty result marshalling as [] rather than null.
func nonNil(m []Match) []Match {
	if m == nil {
		return []Match{}
	}
	return m
}

func containsFold(v any, needle string) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.Contains(strings.ToLower(s), needle)
}

// escapeLike neutralises the wildcards in a user's search text so typing "50%"
// looks for the literal string rather than matching everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
