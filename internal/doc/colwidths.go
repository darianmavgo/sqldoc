package doc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ColumnHint is a best-effort width hint for one table, corrected in the
// background from a sample wider than the first page a viewer has loaded.
// It ships representative strings, not pixel widths: measuring text against a
// font is a rendering concern the client already owns for the first page, and
// this only gives it something further into the table to measure as well.
type ColumnHint struct {
	Table string `json:"table"`
	// Known is false for a view or a WITHOUT ROWID table, which have no rowid
	// axis to seek on — sampling one would mean falling back to something
	// proportional to the table's size, which this package never does.
	Known bool `json:"known"`
	Done  bool `json:"done"`
	// Samples holds the longest formatted value seen per column, in column
	// order. Absent until Done.
	Samples []string `json:"samples,omitempty"`
}

type colWidthState struct {
	mu      sync.Mutex
	hint    ColumnHint
	started bool
}

// sampleAnchors and sampleRowsPerAnchor bound the background scan to a fixed
// cost regardless of table size. Every anchor is an index seek, so the total
// work is sampleAnchors*sampleRowsPerAnchor row reads whether the table has a
// thousand rows or five million.
const (
	sampleAnchors       = 24
	sampleRowsPerAnchor = 12
)

// ColumnHints returns the current best column-width hint without blocking,
// and starts a bounded one-shot background sample the first time it is asked
// about a given table. Mirrors Count: a cheap answer immediately, corrected
// once in the background.
func (d *Doc) ColumnHints(name string) ColumnHint {
	d.mu.Lock()
	if d.colWidths == nil {
		d.colWidths = map[string]*colWidthState{}
	}
	cw, ok := d.colWidths[name]
	if !ok {
		cw = &colWidthState{hint: ColumnHint{Table: name}}
		d.colWidths[name] = cw
	}
	d.mu.Unlock()

	cw.mu.Lock()
	defer cw.mu.Unlock()
	if !cw.started {
		cw.started = true
		go d.sampleColumnWidths(name, cw)
	}
	return cw.hint
}

// sampleColumnWidths walks a bounded number of index-seek anchors spread
// across the table's rowid range on the background connection, tracking the
// longest formatted value seen per column. Unlike exactCount it is a one-shot
// pass, not an open-ended scan: every anchor costs an index seek, so it
// finishes in milliseconds even on a multi-million-row table.
func (d *Doc) sampleColumnWidths(name string, cw *colWidthState) {
	finish := func(h ColumnHint) {
		cw.mu.Lock()
		cw.hint = h
		cw.mu.Unlock()
	}

	t, ok := d.Table(name)
	if !ok || !t.HasRowID {
		finish(ColumnHint{Table: name, Done: true})
		return
	}
	ts := d.state(name)
	if ts.err != nil {
		finish(ColumnHint{Table: name, Done: true})
		return
	}

	lo, hi, ok := d.bounds(name)
	if !ok { // empty table
		finish(ColumnHint{Table: name, Known: true, Done: true})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.waitIdle(ctx); err != nil {
		finish(ColumnHint{Table: name, Done: true})
		return
	}

	longest := make([]string, len(ts.cols))
	q := "SELECT " + ts.colExpr + " FROM " + ts.quoted +
		" WHERE rowid >= ? ORDER BY rowid LIMIT ?"
	span := hi - lo

	for i := 0; i < sampleAnchors; i++ {
		anchor := lo
		if sampleAnchors > 1 {
			anchor = lo + span*int64(i)/int64(sampleAnchors-1)
		}
		if err := d.sampleAnchor(ctx, q, anchor, longest); err != nil {
			break // the document may have been closed mid-sample; report what we have
		}
	}

	finish(ColumnHint{Table: name, Known: true, Done: true, Samples: longest})
}

// sampleAnchor reads one window of rows from the anchor forward and folds
// each cell into the running longest-string-per-column.
func (d *Doc) sampleAnchor(ctx context.Context, q string, anchor int64, longest []string) error {
	rows, err := d.bg.QueryContext(ctx, q, anchor, sampleRowsPerAnchor)
	if err != nil {
		return err
	}
	defer rows.Close()

	n := len(longest)
	vals := make([]any, n)
	dest := make([]any, n)
	for i := range vals {
		dest[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		for i, v := range vals {
			s := sampleText(cell(v))
			if len(s) > len(longest[i]) {
				longest[i] = s
			}
		}
	}
	return rows.Err()
}

// sampleText mirrors how the grid itself renders a cell (writeCell in
// app.js), so a sample is only ever as wide as what would actually appear on
// screen. A value is capped well past the point any column would be allowed
// to grow to, so one enormous text field can't inflate the response.
func sampleText(v any) string {
	const maxLen = 256
	var s string
	switch t := v.(type) {
	case nil:
		return "NULL"
	case blob:
		return "◼ 000 KB" // a fixed placeholder: the grid never shows blob bytes or their exact size in-line
	case string:
		s = t
	default:
		s = fmt.Sprint(t)
	}
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}
