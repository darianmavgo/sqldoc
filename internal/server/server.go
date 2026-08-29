// Package server exposes a session of open documents over loopback HTTP. The
// viewer is a web page either way; the native window and the browser talk to
// exactly the same API, so there is only one implementation to keep fast.
package server

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/darianmavgo/sqldoc/internal/doc"
	"github.com/darianmavgo/sqldoc/internal/pick"
	"github.com/darianmavgo/sqldoc/internal/session"
	"github.com/darianmavgo/sqldoc/internal/ui"
)

// Server serves a session of documents.
type Server struct {
	sess  *session.Session
	token string
	mux   *http.ServeMux

	ln   net.Listener
	http *http.Server
	once sync.Once

	// pickMu serialises the native open dialog. Two dialogs racing would leave
	// one of them orphaned behind the other.
	pickMu sync.Mutex

	// Activate, when set, brings the front end's own window back to the front.
	//
	// The open dialog has to be owned by an application that is allowed to come
	// forward, or it opens behind the window that asked for it (see
	// internal/pick). The cost of that is that the owning application is still
	// frontmost when the dialog closes, and the viewer's window is left buried
	// behind it: you choose a database and appear to get nothing. Only the front
	// end knows how to undo that, so the server asks rather than guessing.
	Activate func()
}

// New builds a server for a session.
func New(sess *session.Session) *Server {
	s := &Server{sess: sess, token: newToken(), mux: http.NewServeMux()}
	s.routes()
	return s
}

// Session exposes the documents this server is serving.
func (s *Server) Session() *session.Session { return s.sess }

// Token is the per-session key that API requests must carry.
func (s *Server) Token() string { return s.token }

// Listen binds to loopback. Port 0 picks a free port, which is what the native
// window uses so two open documents never collide.
func (s *Server) Listen(port int) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return err
	}
	s.ln = ln
	s.http = &http.Server{
		Handler:           s.guard(s.mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return nil
}

// Addr is the bound host:port.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// URL is the address to open, token included.
func (s *Server) URL() string {
	return "http://" + s.Addr() + "/?k=" + s.token
}

// Serve runs until the listener closes.
func (s *Server) Serve() error { return s.http.Serve(s.ln) }

// Close stops the server.
func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		if s.ln != nil {
			err = s.ln.Close()
		}
	})
	return err
}

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleShell)
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/api/session", s.json(s.handleSession))
	s.mux.HandleFunc("/api/open", s.json(s.handleOpen))
	s.mux.HandleFunc("/api/pick", s.json(s.handlePick))
	s.mux.HandleFunc("/api/upload", s.json(s.handleUpload))
	s.mux.HandleFunc("/api/close", s.json(s.handleClose))
	s.mux.HandleFunc("/api/doc", s.json(s.handleDoc))
	s.mux.HandleFunc("/api/rows", s.json(s.handleRows))
	s.mux.HandleFunc("/api/count", s.json(s.handleCount))
	s.mux.HandleFunc("/api/colwidths", s.json(s.handleColumnHints))
	s.mux.HandleFunc("/api/find", s.json(s.handleFind))
	s.mux.HandleFunc("/api/ordinal", s.json(s.handleOrdinal))
	s.mux.HandleFunc("/api/export", s.handleExport)
	s.mux.Handle("/static/", http.StripPrefix("/static/", ui.Handler()))
}

// guard enforces the two things that keep a loopback server from being a hole:
// the request must be addressed to loopback by name, which defeats DNS
// rebinding, and it must carry this session's token.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		// /healthz answers without the session token so that a viewer can be
		// checked from outside the page that owns it — which is the only way to
		// tell a window that loaded a database from one that came up empty. It
		// reports counts and nothing else: no names, no paths, no data.
		if !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/healthz" &&
			r.URL.Query().Get("k") != s.token {
			http.Error(w, "bad or missing key", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealth reports whether this viewer has anything open.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	open := s.sess.List()
	tables := 0
	if len(open) > 0 {
		tables = len(open[0].Doc.Tables())
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"version": Version,
		"docs":    len(open),
		"tables":  tables,
	})
}

func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	// The shell is tiny and inlines its own CSS, so the first paint needs one
	// round trip and no blocking subresource. It is the same page whether or
	// not anything is open; with nothing open it shows the start page.
	style := doc.Style{Theme: "auto", Accent: "#2563eb"}
	name := "sqldoc"
	if e, ok := s.docFromQuery(r); ok {
		style, name = e.Doc.Style(), e.Name
	}
	w.Write(ui.Shell(s.token, style, name))
}

// docFromQuery resolves the document a request is about: the one named by the
// doc parameter, or the first one open when none is named.
func (s *Server) docFromQuery(r *http.Request) (*session.Entry, bool) {
	if id := r.URL.Query().Get("doc"); id != "" {
		return s.sess.Get(id)
	}
	return s.sess.First()
}

// requireDoc is docFromQuery for handlers that cannot proceed without one.
func (s *Server) requireDoc(r *http.Request) (*doc.Doc, error) {
	e, ok := s.docFromQuery(r)
	if !ok {
		return nil, errNoDocument
	}
	return e.Doc, nil
}

var errNoDocument = fmt.Errorf("no document is open")

type sessionInfo struct {
	Docs    []docSummary     `json:"docs"`
	Recents []session.Recent `json:"recents"`
	CanPick bool             `json:"canPick"`
	Driver  string           `json:"driver"`
	Version string           `json:"version"`
}

type docSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Version is shown on the start page.
var Version = "dev"

func (s *Server) handleSession(r *http.Request) (any, error) {
	open := s.sess.List()
	docs := make([]docSummary, 0, len(open))
	for _, e := range open {
		docs = append(docs, docSummary{ID: e.ID, Name: e.Name, Path: e.Path, Size: e.Size})
	}
	rec := s.sess.Recents()
	if rec == nil {
		rec = []session.Recent{}
	}
	return sessionInfo{
		Docs: docs, Recents: rec,
		CanPick: pick.Available(), Driver: doc.DriverLabel, Version: Version,
	}, nil
}

// handleOpen opens a database by path. This is the endpoint behind the path
// box, the recent list, and a drop that carried a real filename.
func (s *Server) handleOpen(r *http.Request) (any, error) {
	path := r.URL.Query().Get("path")
	if path == "" && r.Body != nil {
		var body struct {
			Path string `json:"path"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body) == nil {
			path = body.Path
		}
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("no path given")
	}
	// Accept what a file manager or a terminal actually hands over.
	if u, err := url.Parse(path); err == nil && u.Scheme == "file" {
		path = u.Path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	e, err := s.sess.Open(path)
	if err != nil {
		return nil, err
	}
	return docSummary{ID: e.ID, Name: e.Name, Path: e.Path, Size: e.Size}, nil
}

// pickOpen is the open dialog, named here so a test can stand in for it. What
// happens around the dialog matters as much as the dialog, and none of it is
// checkable with a person in front of a screen.
var pickOpen = pick.Open

// handlePick shows the operating system's open dialog and opens what comes
// back. It is refused from anywhere but loopback: a dialog appearing on
// someone's screen because of a remote request would be indefensible.
func (s *Server) handlePick(r *http.Request) (any, error) {
	if !isLoopback(r) {
		return nil, fmt.Errorf("the file dialog is only available locally")
	}
	s.pickMu.Lock()
	defer s.pickMu.Unlock()

	// However the dialog ends — chosen, dismissed or failed — the window that
	// was in front before it opened has to be the window in front after it
	// closes. This runs last, once the document is open, so the window that
	// comes forward is already showing the database that was just chosen.
	defer func() {
		if s.Activate != nil {
			s.Activate()
		}
	}()

	path, err := pickOpen(r.Context(), "Open a SQLite database")
	if err == pick.ErrCancelled {
		return map[string]any{"cancelled": true}, nil
	}
	if err != nil {
		return nil, err
	}
	e, err := s.sess.Open(path)
	if err != nil {
		return nil, err
	}
	return docSummary{ID: e.ID, Name: e.Name, Path: e.Path, Size: e.Size}, nil
}

// maxUpload caps a dropped database. Dropping a file onto a web page hands over
// bytes and never a path, so the only way to open one is to write a copy first;
// the cap keeps a stray drop from filling the disk.
const maxUpload = 4 << 30 // 4 GiB

// handleUpload accepts a database dropped onto the window and opens the copy.
func (s *Server) handleUpload(r *http.Request) (any, error) {
	if r.Method != http.MethodPost {
		return nil, fmt.Errorf("upload requires POST")
	}
	name := r.Header.Get("X-Sqldoc-Filename")
	if name == "" {
		name = "dropped.db"
	}
	tmp, err := s.sess.TempFile(name)
	if err != nil {
		return nil, err
	}

	f, err := os.Create(tmp)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, maxUpload+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if n > maxUpload {
		os.Remove(tmp)
		return nil, fmt.Errorf("that file is larger than %d GiB; open it by path instead", maxUpload>>30)
	}

	e, err := s.sess.OpenTemp(tmp)
	if err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return docSummary{ID: e.ID, Name: e.Name, Path: e.Path, Size: e.Size}, nil
}

func (s *Server) handleClose(r *http.Request) (any, error) {
	id := r.URL.Query().Get("doc")
	if id == "" {
		return nil, fmt.Errorf("no document given")
	}
	if err := s.sess.Close(id); err != nil {
		return nil, err
	}
	return map[string]bool{"closed": true}, nil
}

type docInfo struct {
	ID       string      `json:"id"`
	Path     string      `json:"path"`
	Name     string      `json:"name"`
	Size     int64       `json:"size"`
	Modified time.Time   `json:"modified"`
	Driver   string      `json:"driver"`
	Tables   []doc.Table `json:"tables"`
	Style    doc.Style   `json:"style"`
	// DefaultView is "gallery" for a document that is mostly small lookup
	// tables, "table" otherwise. The viewer treats it as a starting point, not
	// a lock: either view stays a click away regardless.
	DefaultView string `json:"defaultView"`
}

func (s *Server) handleDoc(r *http.Request) (any, error) {
	e, ok := s.docFromQuery(r)
	if !ok {
		return nil, errNoDocument
	}
	return docInfo{
		ID:          e.ID,
		Path:        e.Doc.Path,
		Name:        filepath.Base(e.Doc.Path),
		Size:        e.Doc.Size,
		Modified:    e.Doc.Modified,
		Driver:      doc.DriverLabel,
		Tables:      e.Doc.Tables(),
		Style:       e.Doc.Style(),
		DefaultView: defaultView(e.Doc),
	}, nil
}

// galleryMinRows is the point at which a table stops being "a small lookup
// table" for the purpose of picking a default view.
const galleryMinRows = 50

// galleryCap bounds how many tables are worth checking at all. Each check is
// an O(1) estimate (see Doc.EstimateRows), but doing it for every table on a
// document with hundreds of them reintroduces a cost proportional to table
// count on the very request a viewer waits on before its first paint — so
// checking stops once a document plainly isn't "a few small lookup tables"
// regardless of what's in the rest of it.
const galleryCap = 12

// defaultView decides which view a document opens into. Gallery is offered
// when there are several tables and every one of them is small; anything else
// - one big table among many, an ungalleried view, more tables than are worth
// checking - falls back to the ordinary one-table-at-a-time view. Either view
// stays reachable via the toggle regardless of which one this picks.
func defaultView(d *doc.Doc) string {
	n := 0
	for _, t := range d.Tables() {
		if t.Hidden {
			continue
		}
		n++
		if n > galleryCap {
			return "table"
		}
		c := d.EstimateRows(t.Name)
		if !c.Known || c.Rows >= galleryMinRows {
			return "table"
		}
	}
	if n >= 3 {
		return "gallery"
	}
	return "table"
}

func (s *Server) handleRows(r *http.Request) (any, error) {
	q := r.URL.Query()
	w := doc.Window{
		Table:  q.Get("table"),
		Limit:  atoi(q.Get("limit"), 100),
		Offset: atoi64(q.Get("offset"), 0),
		Sort:   q.Get("sort"),
		Desc:   q.Get("desc") == "1",
	}
	if v := q.Get("after"); v != "" {
		w.After, w.UseAfter = atoi64(v, 0), true
	}
	d, err := s.requireDoc(r)
	if err != nil {
		return nil, err
	}
	if w.Sort != "" && !validColumn(d, w.Table, w.Sort) {
		return nil, fmt.Errorf("no such column: %s", w.Sort)
	}
	return d.Rows(r.Context(), w)
}

// validColumn keeps the sort parameter from reaching SQL unless it names a real
// column of the table being sorted.
func validColumn(d *doc.Doc, table, col string) bool {
	cols, err := d.Columns(table)
	if err != nil {
		return false
	}
	for _, c := range cols {
		if c.Name == col {
			return true
		}
	}
	return false
}

func (s *Server) handleCount(r *http.Request) (any, error) {
	d, err := s.requireDoc(r)
	if err != nil {
		return nil, err
	}
	return d.Count(r.URL.Query().Get("table")), nil
}

// handleColumnHints answers with the best column-width sample known so far,
// starting the background scan on first ask. Polled the same way /api/count
// is: a quick answer that isn't final yet, refined by asking again.
func (s *Server) handleColumnHints(r *http.Request) (any, error) {
	d, err := s.requireDoc(r)
	if err != nil {
		return nil, err
	}
	return d.ColumnHints(r.URL.Query().Get("table")), nil
}

func (s *Server) handleOrdinal(r *http.Request) (any, error) {
	d, err := s.requireDoc(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	n, err := d.Ordinal(r.Context(), q.Get("table"), atoi64(q.Get("rowid"), 0))
	if err != nil {
		return nil, err
	}
	return map[string]int64{"ordinal": n}, nil
}

func (s *Server) handleFind(r *http.Request) (any, error) {
	d, err := s.requireDoc(r)
	if err != nil {
		return nil, err
	}
	q := r.URL.Query()
	return d.Find(r.Context(), q.Get("table"), q.Get("q"),
		atoi64(q.Get("from"), 0), atoi(q.Get("limit"), 50))
}

// handleExport streams a table out as CSV. It is the viewer's Save button, and
// it streams rather than buffering so exporting a large table does not put the
// table in memory.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	table := r.URL.Query().Get("table")
	d, derr := s.requireDoc(r)
	if derr != nil {
		http.Error(w, derr.Error(), http.StatusNotFound)
		return
	}
	cols, err := d.Columns(table)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+strings.ReplaceAll(table, `"`, "")+`.csv"`)

	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = c.Name
	}
	cw.Write(header)

	rec := make([]string, len(cols))
	var after int64
	for {
		page, err := d.Rows(r.Context(), doc.Window{
			Table: table, Limit: 1000, After: after, UseAfter: after > 0,
			Offset: 0,
		})
		if err != nil || len(page.Rows) == 0 {
			return
		}
		for _, row := range page.Rows {
			for i, v := range row {
				rec[i] = csvCell(v)
			}
			cw.Write(rec)
		}
		cw.Flush()
		if len(page.RowIDs) == 0 || len(page.Rows) < 1000 {
			return
		}
		after = page.RowIDs[len(page.RowIDs)-1]
	}
}

func csvCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// json wraps a handler that returns a value, adding encoding, error mapping and
// loopback-aware compression.
func (s *Server) json(h func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := h(r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		// Over loopback, gzip costs more CPU than the copy it saves, so it is
		// only used for a client that is actually across a network.
		if !isLoopback(r) && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			defer gz.Close()
			json.NewEncoder(gz).Encode(v)
			return
		}
		json.NewEncoder(w).Encode(v)
	}
}

func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func atoi(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func atoi64(s string, def int64) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
