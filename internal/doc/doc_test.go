package doc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// build makes a scratch database and returns its path.
func build(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open(driverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return path
}

func open(t *testing.T, path string) *Doc {
	t.Helper()
	d, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// Identifiers that are only legal when quoted are the crash the previous
// viewers hit, so they get a test of their own.
func TestAwkwardIdentifiers(t *testing.T) {
	path := build(t,
		`CREATE TABLE "1_recent_files" ("order" TEXT, "select" INTEGER, "a""b" TEXT)`,
		`INSERT INTO "1_recent_files" VALUES ('x', 1, 'y')`,
		`CREATE TABLE "group by" (x INTEGER)`,
		`INSERT INTO "group by" VALUES (42)`,
	)
	d := open(t, path)

	for _, name := range []string{"1_recent_files", "group by"} {
		if _, ok := d.Table(name); !ok {
			t.Fatalf("table %q not discovered", name)
		}
		if c := d.Count(name); !c.Known || c.Rows != 1 {
			t.Errorf("%q: count = %+v, want 1 row", name, c)
		}
		p, err := d.Rows(context.Background(), Window{Table: name, Limit: 10})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if len(p.Rows) != 1 {
			t.Errorf("%q: got %d rows, want 1", name, len(p.Rows))
		}
	}
}

// Every access path must return the same rows for the same window, or scrolling
// would show different data depending on how the user got there.
func TestPathsAgree(t *testing.T) {
	var rows string
	for i := 1; i <= 500; i++ {
		if i > 1 {
			rows += ","
		}
		rows += fmt.Sprintf("(%d,'name-%d')", i, i)
	}
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t VALUES `+rows,
	)
	d := open(t, path)
	ctx := context.Background()

	const off = 300
	want, err := d.Rows(ctx, Window{Table: "t", Offset: off, Limit: 20, ForceOffset: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.Rows(ctx, Window{Table: "t", Offset: off, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "interpolated" {
		t.Errorf("expected the interpolated path, got %q", got.Path)
	}
	after, err := d.Rows(ctx, Window{Table: "t", After: off, UseAfter: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if after.Path != "keyset" {
		t.Errorf("expected the keyset path, got %q", after.Path)
	}
	for i := range want.Rows {
		if want.Rows[i][0] != got.Rows[i][0] || want.Rows[i][0] != after.Rows[i][0] {
			t.Fatalf("row %d disagrees: offset=%v interpolated=%v keyset=%v",
				i, want.Rows[i][0], got.Rows[i][0], after.Rows[i][0])
		}
	}
}

// A dense table must map ordinals onto rowids exactly; a sparse one must be
// honest that it is guessing.
func TestOrdinalAccuracy(t *testing.T) {
	path := build(t,
		`CREATE TABLE dense (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<1000)
		 INSERT INTO dense SELECT n, 'v'||n FROM c`,
		`CREATE TABLE sparse (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<1000)
		 INSERT INTO sparse SELECT n*7, 'v'||n FROM c`,
		`DELETE FROM sparse WHERE id % 3 = 0`,
	)
	d := open(t, path)
	ctx := context.Background()

	waitExact(t, d, "dense")
	p, err := d.Rows(ctx, Window{Table: "dense", Offset: 500, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Approx {
		t.Error("dense table should not report an approximate position")
	}
	if got := p.Rows[0][0]; got != int64(501) {
		t.Errorf("dense offset 500: got id %v, want 501", got)
	}
	if n, _ := d.Ordinal(ctx, "dense", 501); n != 500 {
		t.Errorf("dense Ordinal(501) = %d, want 500", n)
	}

	waitExact(t, d, "sparse")
	// Ordinal must be exact even where interpolation is not.
	ids := rowIDsAt(t, d, "sparse", 400)
	if n, _ := d.Ordinal(ctx, "sparse", ids); n != 400 {
		t.Errorf("sparse Ordinal(%d) = %d, want 400", ids, n)
	}
	sp, err := d.Rows(ctx, Window{Table: "sparse", Offset: 400, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !sp.Approx {
		t.Error("sparse table should report that its position is approximate")
	}
}

func rowIDsAt(t *testing.T, d *Doc, table string, off int64) int64 {
	t.Helper()
	p, err := d.Rows(context.Background(),
		Window{Table: table, Offset: off, Limit: 1, ForceOffset: true})
	if err != nil || len(p.Rows) == 0 {
		t.Fatalf("rowIDsAt: %v", err)
	}
	return p.Rows[0][0].(int64)
}

func waitExact(t *testing.T, d *Doc, table string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if d.Count(table).Exact {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("exact count for %q never settled", table)
}

// The count must be available immediately, before any scan could have run.
func TestCountDoesNotBlock(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<200000)
		 INSERT INTO t SELECT n, 'value number '||n FROM c`,
	)
	d := open(t, path)

	start := time.Now()
	c := d.Count("t")
	elapsed := time.Since(start)

	if !c.Known {
		t.Fatal("no estimate was available at open")
	}
	if c.Rows != 200000 {
		t.Errorf("estimate = %d, want 200000", c.Rows)
	}
	if elapsed > 25*time.Millisecond {
		t.Errorf("first count took %s; it must not scan", elapsed)
	}
}

// Blobs must never be shipped to the viewer, only their size.
func TestBlobsAreNotShipped(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, data BLOB)`,
		`INSERT INTO t VALUES (1, randomblob(100000))`,
	)
	d := open(t, path)
	p, err := d.Rows(context.Background(), Window{Table: "t", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, ok := p.Rows[0][1].(blob)
	if !ok {
		t.Fatalf("blob cell became %T, want a size marker", p.Rows[0][1])
	}
	if b.Bytes != 100000 {
		t.Errorf("blob size = %d, want 100000", b.Bytes)
	}
}

// Find walks the rowid axis in increments and must eventually cover the table.
func TestFindIsIncrementalAndComplete(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<600000)
		 INSERT INTO t SELECT n, CASE WHEN n%100000=0 THEN 'needle' ELSE 'hay'||n END FROM c`,
	)
	d := open(t, path)
	ctx := context.Background()

	var total int
	var from int64
	for steps := 0; steps < 100; steps++ {
		r, err := d.Find(ctx, "t", "needle", from, 50)
		if err != nil {
			t.Fatal(err)
		}
		total += len(r.Matches)
		if r.Done {
			break
		}
		if r.Next <= from {
			t.Fatal("find cursor did not advance")
		}
		from = r.Next
	}
	if total != 6 {
		t.Errorf("found %d matches, want 6", total)
	}
}

// A search for text containing LIKE wildcards must match literally.
func TestFindEscapesWildcards(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO t VALUES (1,'discount 50% off'), (2,'plain text'), (3,'a_b')`,
	)
	d := open(t, path)
	ctx := context.Background()

	r, err := d.Find(ctx, "t", "50%", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Matches) != 1 {
		t.Errorf("searching for \"50%%\" found %d rows, want 1", len(r.Matches))
	}
	r, _ = d.Find(ctx, "t", "a_b", 0, 50)
	if len(r.Matches) != 1 {
		t.Errorf("searching for \"a_b\" found %d rows, want 1", len(r.Matches))
	}
}

// The document must open and describe itself without touching row data.
func TestViewsAndEmptyTables(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`INSERT INTO t VALUES (1,'a'),(2,'b')`,
		`CREATE TABLE empty (x INTEGER)`,
		`CREATE VIEW v AS SELECT v, count(*) AS n FROM t GROUP BY v`,
		`CREATE TABLE wr (k TEXT PRIMARY KEY, val TEXT) WITHOUT ROWID`,
		`INSERT INTO wr VALUES ('a','1'),('b','2')`,
	)
	d := open(t, path)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		want int
	}{
		{"t", 2}, {"empty", 0}, {"v", 2}, {"wr", 2},
	} {
		p, err := d.Rows(ctx, Window{Table: tc.name, Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(p.Rows) != tc.want {
			t.Errorf("%s: got %d rows, want %d", tc.name, len(p.Rows), tc.want)
		}
	}
	if tb, _ := d.Table("wr"); tb.HasRowID {
		t.Error("WITHOUT ROWID table was reported as having a rowid")
	}
	if tb, _ := d.Table("v"); tb.HasRowID {
		t.Error("view was reported as having a rowid")
	}
}

// Presentation metadata carried in the document must be picked up, and its
// absence must not matter.
func TestStyleAndNav(t *testing.T) {
	path := build(t,
		`CREATE TABLE _style (key TEXT, value TEXT)`,
		`INSERT INTO _style VALUES ('title','My Report'),('accent','#ff0000')`,
		`CREATE TABLE _nav (table_name TEXT, label TEXT, position INTEGER, hidden INTEGER)`,
		`INSERT INTO _nav VALUES ('b','Bravo',1,0),('a','Alpha',2,0)`,
		`CREATE TABLE a (x INTEGER)`,
		`CREATE TABLE b (x INTEGER)`,
	)
	d := open(t, path)

	if got := d.Style().Title; got != "My Report" {
		t.Errorf("title = %q, want %q", got, "My Report")
	}
	tables := d.Tables()
	if tables[0].Name != "b" || tables[0].Label != "Bravo" {
		t.Errorf("_nav ordering ignored: first table is %+v", tables[0])
	}
	for _, tb := range tables {
		if tb.Name == "_style" && !tb.Hidden {
			t.Error("_style should be hidden")
		}
	}

	plain := open(t, build(t, `CREATE TABLE x (a INTEGER)`))
	if plain.Style().Theme != "auto" {
		t.Error("a document with no _style should still get defaults")
	}
}

// Opening something that is not a database must fail with a clear error rather
// than a blank screen.
func TestNotADatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := writeFile(path, "this is just text, repeated many times.\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Options{}); err == nil {
		t.Fatal("opening a text file as a database should fail")
	}
}

// An empty chunk must marshal as [] rather than null: a client walking a long
// search would fault on the first stretch of the table with no hits.
func TestFindResultIsNeverNull(t *testing.T) {
	path := build(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<300000)
		 INSERT INTO t SELECT n, 'hay'||n FROM c`,
	)
	d := open(t, path)

	r, err := d.Find(context.Background(), "t", "needle", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if r.Done {
		t.Fatal("a 300k-row table should take more than one chunk")
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"matches":null`)) {
		t.Errorf("empty chunk marshalled as null:\n%s", b)
	}
	if !bytes.Contains(b, []byte(`"matches":[]`)) {
		t.Errorf("expected an empty list:\n%s", b)
	}
}
