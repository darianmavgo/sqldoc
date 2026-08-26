//go:build cgosqlite

package doc

import (
	"net/url"

	_ "github.com/mattn/go-sqlite3"
)

// driverName is the database/sql driver registered by mattn/go-sqlite3.
const driverName = "sqlite3"

// DriverLabel identifies the active SQLite build in --version and /api/meta.
const DriverLabel = "mattn/go-sqlite3 (cgo)"

// dsn builds a read-only DSN. mattn only recognises a fixed set of underscore
// parameters, so the shared pragmas are applied by applyPragmas after Open
// instead of being encoded here.
func dsn(path string, immutable bool) string {
	q := url.Values{}
	q.Set("mode", "ro")
	if immutable {
		q.Set("immutable", "1")
	}
	q.Set("_query_only", "true")
	return "file:" + url.PathEscape(path) + "?" + q.Encode()
}
