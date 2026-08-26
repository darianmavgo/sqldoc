//go:build !cgosqlite

package doc

import (
	"net/url"

	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// DriverLabel identifies the active SQLite build in --version and /api/meta.
const DriverLabel = "modernc.org/sqlite (pure Go)"

// dsn builds a read-only DSN. modernc accepts repeated _pragma parameters and
// applies them as each connection is opened, so the connection is already tuned
// by the time the first query runs.
func dsn(path string, immutable bool) string {
	q := url.Values{}
	q.Set("mode", "ro")
	if immutable {
		q.Set("immutable", "1")
	}
	for _, p := range connectPragmas {
		q.Add("_pragma", p)
	}
	return "file:" + url.PathEscape(path) + "?" + q.Encode()
}
