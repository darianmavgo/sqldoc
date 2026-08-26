package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/darianmavgo/sqldoc/internal/doc"
	"github.com/darianmavgo/sqldoc/internal/session"
	_ "modernc.org/sqlite"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)`,
		`INSERT INTO t VALUES (1,'alpha'),(2,'bravo'),(3,'charlie')`,
	} {
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
