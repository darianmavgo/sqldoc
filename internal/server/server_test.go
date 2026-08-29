package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darianmavgo/sqldoc/internal/doc"
	"github.com/darianmavgo/sqldoc/internal/pick"
	"github.com/darianmavgo/sqldoc/internal/session"
	_ "modernc.org/sqlite"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return buildTestServer(t,
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t VALUES (1,'alpha'),(2,'bravo'),(3,'charlie')`,
	)
}

// buildTestServer runs arbitrary SQL against a scratch database and opens it
// as the server's one document, for tests that need a specific shape of
// document rather than the default single small table.
func buildTestServer(t *testing.T, stmts ...string) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	sess := session.New(doc.Options{})
	if _, err := sess.Open(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.CloseAll)
	return New(sess)
}

func do(t *testing.T, s *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", target, nil)
	r.Host = "127.0.0.1:1234"
	// The file dialog is refused to anything that is not physically on
	// loopback, and httptest's default RemoteAddr is not.
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	s.guard(s.mux).ServeHTTP(w, r)
	return w
}

// Anything on loopback is reachable by any process and by any web page the user
// visits, so the token is the only thing standing between a stray request and
// the contents of the database.
func TestTokenIsRequired(t *testing.T) {
	s := newTestServer(t)
	for _, target := range []string{
		"/api/doc",
		"/api/rows?table=t",
		"/api/doc?k=wrong",
		"/api/export?table=t",
		"/",
	} {
		if got := do(t, s, target).Code; got != http.StatusForbidden {
			t.Errorf("%s without a token: status %d, want 403", target, got)
		}
	}
	if got := do(t, s, "/api/doc?k="+s.Token()).Code; got != http.StatusOK {
		t.Errorf("with the right token: status %d, want 200", got)
	}
}

// A page on the open internet can point a hostname at 127.0.0.1 and make the
// browser send same-origin requests here. Requiring the Host header to name
// loopback shuts that down.
func TestRejectsForeignHost(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest("GET", "/api/doc?k="+s.Token(), nil)
	r.Host = "evil.example.com"
	w := httptest.NewRecorder()
	s.guard(s.mux).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("foreign Host header: status %d, want 403", w.Code)
	}
}

// The sort parameter is interpolated into SQL, so it must be checked against
// the real column list rather than escaped and hoped for.
func TestSortRejectsUnknownColumn(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "/api/rows?table=t&sort=name%22+--&k="+s.Token())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("injected sort column: status %d, want 400", w.Code)
	}
	if w := do(t, s, "/api/rows?table=t&sort=name&k="+s.Token()); w.Code != http.StatusOK {
		t.Errorf("legitimate sort column: status %d, want 200", w.Code)
	}
}

func TestExportStreamsCSV(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "/api/export?table=t&k="+s.Token())
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"id,name", "1,alpha", "3,charlie"} {
		if !strings.Contains(body, want) {
			t.Errorf("CSV is missing %q:\n%s", want, body)
		}
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "t.csv") {
		t.Errorf("Content-Disposition = %q", cd)
	}
}

func TestRowsShape(t *testing.T) {
	s := newTestServer(t)
	w := do(t, s, "/api/rows?table=t&limit=2&k="+s.Token())
	var p struct {
		Columns []doc.Column `json:"columns"`
		Rows    [][]any      `json:"rows"`
		RowIDs  []int64      `json:"rowids"`
		Path    string       `json:"path"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 2 || len(p.Columns) != 2 {
		t.Fatalf("got %d rows and %d columns, want 2 and 2", len(p.Rows), len(p.Columns))
	}
	if len(p.RowIDs) != 2 {
		t.Errorf("rowids missing; the viewer needs them to anchor the next page")
	}
	if p.Rows[0][1] != "alpha" {
		t.Errorf("first row = %v", p.Rows[0])
	}
}

// The open dialog belongs to another application, because one owned by the
// viewer opens behind it (see internal/pick). That application is still
// frontmost when the dialog closes, so choosing a database used to look like
// nothing happening at all: the document opened into a window that stayed
// buried behind whatever had been showing the dialog. Getting the window back
// is the front end's job, and it only happens if the server asks.
//
// Cancelling counts. A dismissed dialog that leaves the window behind Finder is
// the same failure as a successful one that does.
func TestPickBringsTheWindowBack(t *testing.T) {
	real := pickOpen
	t.Cleanup(func() { pickOpen = real })

	chosen := filepath.Join(t.TempDir(), "second.db")
	db, err := sql.Open("sqlite", "file:"+chosen)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for _, tc := range []struct {
		name string
		path string
		err  error
	}{
		{"chosen", chosen, nil},
		{"cancelled", "", pick.ErrCancelled},
		{"failed", "", errors.New("no dialog available")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pickOpen = func(context.Context, string) (string, error) { return tc.path, tc.err }

			s := newTestServer(t)
			activated := 0
			s.Activate = func() { activated++ }

			do(t, s, "/api/pick?k="+s.Token())
			if activated != 1 {
				t.Errorf("window activated %d times, want 1", activated)
			}
		})
	}
}

// A front end with no window to raise - the browser one - must not be a nil
// call in the middle of the pick handler.
func TestPickWithoutAnActivateHook(t *testing.T) {
	real := pickOpen
	t.Cleanup(func() { pickOpen = real })
	pickOpen = func(context.Context, string) (string, error) { return "", pick.ErrCancelled }

	s := newTestServer(t)
	if got := do(t, s, "/api/pick?k="+s.Token()).Code; got != http.StatusOK {
		t.Errorf("status %d, want 200", got)
	}
}

func docDefaultView(t *testing.T, s *Server) string {
	t.Helper()
	w := do(t, s, "/api/doc?k="+s.Token())
	var info struct {
		DefaultView string `json:"defaultView"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	return info.DefaultView
}

func tinyTable(name string) string {
	return `CREATE TABLE ` + name + ` (id INTEGER PRIMARY KEY, v TEXT); ` +
		`INSERT INTO ` + name + ` VALUES (1,'a'),(2,'b')`
}

// Several small lookup tables and nothing else is exactly what the gallery
// view is for.
func TestDefaultViewGalleryForSeveralSmallTables(t *testing.T) {
	var stmts []string
	for _, n := range []string{"a", "b", "c", "d"} {
		stmts = append(stmts, strings.Split(tinyTable(n), "; ")...)
	}
	s := buildTestServer(t, stmts...)
	if got := docDefaultView(t, s); got != "gallery" {
		t.Errorf("defaultView = %q, want gallery", got)
	}
}

// One big table mixed in with small ones is a real schema, not a handful of
// lookup tables, regardless of what else is in the document.
func TestDefaultViewTableWhenOneTableIsBig(t *testing.T) {
	stmts := []string{
		`CREATE TABLE big (id INTEGER PRIMARY KEY, v TEXT)`,
		`WITH RECURSIVE c(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM c WHERE n<500)
		 INSERT INTO big SELECT n, 'v'||n FROM c`,
	}
	for _, n := range []string{"a", "b"} {
		stmts = append(stmts, strings.Split(tinyTable(n), "; ")...)
	}
	s := buildTestServer(t, stmts...)
	if got := docDefaultView(t, s); got != "table" {
		t.Errorf("defaultView = %q, want table", got)
	}
}

// A document with more tables than are worth checking (galleryCap) falls back
// to Table view without estimating the rest of them.
func TestDefaultViewTableBeyondGalleryCap(t *testing.T) {
	var stmts []string
	for i := 0; i < galleryCap+3; i++ {
		stmts = append(stmts, strings.Split(tinyTable(fmt.Sprintf("t%d", i)), "; ")...)
	}
	s := buildTestServer(t, stmts...)
	if got := docDefaultView(t, s); got != "table" {
		t.Errorf("defaultView = %q, want table (beyond galleryCap=%d)", got, galleryCap)
	}
}

// Fewer than three non-hidden tables isn't "several" no matter how small they
// are.
func TestDefaultViewTableWhenTooFewTables(t *testing.T) {
	var stmts []string
	for _, n := range []string{"a", "b"} {
		stmts = append(stmts, strings.Split(tinyTable(n), "; ")...)
	}
	s := buildTestServer(t, stmts...)
	if got := docDefaultView(t, s); got != "table" {
		t.Errorf("defaultView = %q, want table (only 2 tables)", got)
	}
}
