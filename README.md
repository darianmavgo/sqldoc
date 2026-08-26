# sqldoc

Open a SQLite file the way Chrome opens a PDF: the window is already there, the
first page is already drawn, and scrolling never stops to think.

```
sqldoc data.db          # opens in your browser
sqldoc                  # start page: recent files, drag and drop
sqldoc a.db b.db        # several at once, switch between them
sqldoc-view data.db     # native window
```

Or just double-click a `.db` in Finder.

## Install

```
make && make install && make install-app && make set-default
```

`make install` puts `sqldoc` on your PATH in `~/.local/bin`. `make install-app`
puts `sqldoc.app` in `/Applications` and registers it, which is what adds sqldoc
to Finder's **Open With** menu. `make set-default` goes one step further and
makes it the application a double-clicked database opens.

That last step is deliberately separate, and the bundle claims SQLite files at
rank `Alternate` until you run it: installing a viewer should not quietly seize
a file type out from under whatever already handles it. To undo it, use Finder's
Get Info → Open with → Change All on any `.db`.

## What it is

A read-only document viewer for SQLite. Not an editor, not a SQL console — the
thing you reach for when you just want to *look* at a database. Go on the
backend, no framework on the frontend, one binary.

## Speed

Measured on a 499 MB database with 5,000,000 rows and 9 columns
(`sqldoc bench testdata/big.db`, Apple silicon, warm page cache):

| | pure Go | cgo |
|---|---|---|
| Open (metadata only) | 0.8–3.3 ms | 2.1–2.7 ms |
| First window on screen | ~250 µs | ~250 µs |
| Row count available | immediately | immediately |
| Scroll, per screen | 143 µs | 124 µs |
| Jump to any row | 154 µs | 131 µs |
| *the same jump via `LIMIT/OFFSET`* | *154 ms* | *104 ms* |

Reproduce it yourself: `make testdata && make bench`.

## How it stays fast

Four decisions do nearly all the work.

**Nothing proportional to the database happens before first paint.** Opening
reads one `sqlite_master` scan. Columns, row counts and rowid bounds are loaded
per table, on first use. A database with 400 tables opens as fast as one with
two.

**The row count is estimated, then corrected.** `SELECT COUNT(*)` on a large
table is a full scan, so it never blocks anything. The estimate comes from
`sqlite_stat1` if the database was analysed, otherwise from `max(rowid)`, both
O(1). The exact count runs in the background on a second connection and the
number updates when it lands — the way a PDF viewer shows you page one before it
knows the page count.

**Scrolling seeks by rowid, not by offset.** `LIMIT 100 OFFSET 4500000` makes
SQLite walk 4.5M rows. Anchoring on the previous window's last rowid is an index
seek instead. Dragging the scrollbar interpolates the ordinal onto the rowid
axis, which is exact whenever rowids are dense — the usual case — and honestly
marked approximate when they are not.

**The frontend is not a grid library.** A few hundred lines of vanilla JS render
about 50 row elements and reuse them. There is no framework to parse before the
first row appears; the whole viewer is inlined into one HTML response.

A fifth thing, learned the hard way: `SELECT min(rowid), max(rowid) FROM t`
scans the entire table, because SQLite's min/max optimisation only fires for a
query with exactly one aggregate. Written as two scalar subqueries it is two
index seeks. On the 5M-row table that is 350 ms against 5 ms, and it was being
paid before anything could be drawn.

## Using it

| | |
|---|---|
| `⌘F` | find, incrementally, across every column |
| `⏎` / `⇧⏎` | next / previous match |
| `⌘+` `⌘-` `⌘0` | zoom |
| `PgUp` `PgDn` `Home` `End` | move by screen, jump to either end |
| drag a column edge | resize; double-click to fit |
| click a column header | sort |
| `⤓` | export the table as CSV |

Find walks the table in bounded increments and reports hits and progress as they
arrive, so a search across millions of rows is responsive from the first moment
rather than after it finishes.

## A document can describe itself

Three optional tables change how a database presents. All are hidden from the
viewer, along with anything else whose name starts with `_`.

```sql
CREATE TABLE _style (key TEXT, value TEXT);
INSERT INTO _style VALUES ('title','Q3 Field Report'), ('accent','#0f9d58');

CREATE TABLE _nav (table_name TEXT, label TEXT, position INTEGER, hidden INTEGER);
INSERT INTO _nav VALUES ('readings','Sensor Readings',1,0), ('scratch','',9,1);
```

`_style` takes `title`, `accent` and `theme` (`auto`, `light`, `dark`).
`_nav` renames, reorders and hides tables.

## Commands

```
sqldoc <file.db>            open in the browser
sqldoc serve <file.db>      serve it, print the URL, do not launch anything
sqldoc info  <file.db>      what the document contains
sqldoc bench <file.db>      measure how fast it reads
sqldoc-view  <file.db>      native window (macOS/Linux/Windows WebView)
```

Flags: `-p <port>`, `-no-open`, `-immutable`, `-version`.

`-immutable` promises SQLite the file will not change while open, which skips
locking and WAL recovery entirely. It is the largest cold-start win available
and it is off by default, because it returns garbage if another process writes
the file underneath you.

## Building

```
make            # build both binaries
make test       # unit tests
make race       # tests under the race detector
make bench      # build a 5M-row database and measure against it
```

The default build uses `modernc.org/sqlite`, which is pure Go: no cgo, and
`GOOS=linux go build` just works. `make cgo` swaps in `mattn/go-sqlite3`, which
is roughly 15% faster on the paths that matter and needs a C toolchain.
`sqldoc-view` always needs cgo, because it hosts the system WebView.

## Safety

The viewer opens every database `mode=ro` with `query_only=1`. It cannot write
to your data even if it wanted to.

The loopback server is not open to the machine: every request must carry a
per-session token, and the `Host` header must name loopback, which is what stops
a web page from reaching it by pointing a hostname at 127.0.0.1. Blobs are never
sent to the browser — only their size — so a table full of images stays cheap to
scroll.

## Layout

```
cmd/sqldoc/         CLI: open, serve, info, bench
cmd/sqldoc-view/    native window
internal/doc/       opening, schema, windowed reads, counting, find
internal/server/    loopback HTTP + JSON API
internal/ui/        the viewer: one HTML shell, one CSS file, one JS file
```

`internal/doc` knows nothing about HTTP and `internal/ui` knows nothing about
SQLite. The native window and the browser are the same viewer talking to the
same API, which is what keeps them from drifting apart.
