package doc

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func waitColumnHints(t *testing.T, d *Doc, table string) ColumnHint {
	t.Helper()
	for i := 0; i < 400; i++ {
		if h := d.ColumnHints(table); h.Done {
			return h
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("column hints for %q never settled", table)
	return ColumnHint{}
}

// The first call has to answer before any sampling could possibly have run,
// the same guarantee Count makes.
func TestColumnHintsDoesNotBlock(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<200000)
		 INSERT INTO t SELECT n, 'value number '||n FROM c`,
	)
	d := open(t, path)

	start := time.Now()
	d.ColumnHints("t")
	elapsed := time.Since(start)

	if elapsed > 25*time.Millisecond {
		t.Errorf("first call to ColumnHints took %s; it must not scan", elapsed)
	}
}

// The whole point of sampling more than the first page is to catch a value
// nowhere near it. The planted value sits exactly on one of the sampler's own
// anchors, so the test doesn't depend on guessing where a fixed row lands.
func TestColumnHintsFindsValuesBeyondFirstPage(t *testing.T) {
	const n = 1000000
	lo, hi := int64(1), int64(n)
	target := lo + (hi-lo)*int64(sampleAnchors/2)/int64(sampleAnchors-1)
	long := strings.Repeat("z", 120)

	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		fmt.Sprintf(`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<%d)
		 INSERT INTO t SELECT n, 'x' FROM c`, n),
		fmt.Sprintf(`UPDATE t SET v = '%s' WHERE id = %d`, long, target),
	)
	d := open(t, path)

	h := waitColumnHints(t, d, "t")
	if !h.Known {
		t.Fatal("expected a known column hint for a rowid table")
	}
	if len(h.Samples) < 2 || h.Samples[1] != long {
		t.Errorf("Samples[1] = %q, want the value planted at row %d "+
			"(row 100 of the first page never sees it)", h.Samples[1], target)
	}
}

// A view and a WITHOUT ROWID table have no rowid axis to seek on. Sampling
// one would mean falling back to something proportional to the table's size,
// which this package never does, so both must report Known:false immediately.
func TestColumnHintsSkipsViewsAndWithoutRowid(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO t VALUES (1,'a'),(2,'b')`,
		`CREATE VIEW vw AS SELECT v, count(*) AS n FROM t GROUP BY v`,
		`CREATE TABLE wr (k TEXT PRIMARY KEY, val TEXT) WITHOUT ROWID`,
		`INSERT INTO wr VALUES ('a','1'),('b','2')`,
	)
	d := open(t, path)

	for _, name := range []string{"vw", "wr"} {
		h := waitColumnHints(t, d, name)
		if h.Known {
			t.Errorf("%s: Known = true, want false (no rowid axis to seek on)", name)
		}
		if len(h.Samples) != 0 {
			t.Errorf("%s: Samples = %v, want none", name, h.Samples)
		}
	}
}
