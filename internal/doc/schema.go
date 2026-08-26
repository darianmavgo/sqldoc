package doc

import (
	"database/sql"
	"sort"
	"strings"
	"sync"
)

// Table is the cheap, always-available description of one table or view.
// Anything costing more than a sqlite_master scan lives in tableState and is
// loaded on first access instead.
type Table struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"` // "table" or "view"
	Hidden   bool   `json:"hidden"`
	HasRowID bool   `json:"hasRowid"`
}

// Column describes one column of a table.
type Column struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	NotNull bool   `json:"notNull"`
	PK      bool   `json:"pk"`
	Numeric bool   `json:"numeric"`
}

// Style is presentation metadata the document carries about itself, read from
// the optional _style / _head key-value tables.
type Style struct {
	Title  string `json:"title"`
	Accent string `json:"accent"`
	Theme  string `json:"theme"` // auto | light | dark
}

// tableState holds the per-table facts that cost a query to learn. Each is
// loaded once, on first access, under a sync.Once so a burst of scroll requests
// at startup does not stampede.
type tableState struct {
	once    sync.Once
	cols    []Column
	quoted  string
	colExpr string
	err     error

	boundsOnce sync.Once
	minID      int64
	maxID      int64
	hasBounds  bool
}

// discover reads every table and view in a single scan of sqlite_master. This
// is the only schema work done before first paint.
func (d *Doc) discover() ([]Table, error) {
	rows, err := d.fg.Query(`
		SELECT name, type, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Table
	for rows.Next() {
		var t Table
		var ddl string
		if err := rows.Scan(&t.Name, &t.Type, &ddl); err != nil {
			return nil, err
		}
		t.Label = humanize(t.Name)
		// Names starting with _ are the document's own metadata, not content.
		t.Hidden = strings.HasPrefix(t.Name, "_")
		t.HasRowID = t.Type == "table" && !strings.Contains(strings.ToUpper(ddl), "WITHOUT ROWID")
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return d.applyNav(out), nil
}

// applyNav lets a document override its own table ordering and labels through
// an optional _nav table, then sorts the rest alphabetically with hidden
// tables last.
func (d *Doc) applyNav(tables []Table) []Table {
	type override struct {
		label  string
		pos    int
		hidden bool
		set    bool
	}
	ov := map[string]override{}

	if d.hasTable(tables, "_nav") {
		rows, err := d.fg.Query(`SELECT table_name, COALESCE(label,''), COALESCE(position,0), COALESCE(hidden,0) FROM _nav`)
		if err == nil {
			for rows.Next() {
				var name string
				var o override
				if err := rows.Scan(&name, &o.label, &o.pos, &o.hidden); err == nil {
					o.set = true
					ov[name] = o
				}
			}
			rows.Close()
		}
	}

	pos := make(map[string]int, len(tables))
	for i := range tables {
		o, ok := ov[tables[i].Name]
		if ok && o.set {
			if o.label != "" {
				tables[i].Label = o.label
			}
			tables[i].Hidden = tables[i].Hidden || o.hidden
			pos[tables[i].Name] = o.pos
		} else {
			pos[tables[i].Name] = 1 << 20
		}
	}

	sort.SliceStable(tables, func(i, j int) bool {
		a, b := tables[i], tables[j]
		if a.Hidden != b.Hidden {
			return !a.Hidden
		}
		if pos[a.Name] != pos[b.Name] {
			return pos[a.Name] < pos[b.Name]
		}
		return a.Name < b.Name
	})
	return tables
}

func (d *Doc) hasTable(tables []Table, name string) bool {
	for _, t := range tables {
		if t.Name == name {
			return true
		}
	}
	return false
}

// loadStyle reads the optional _style and _head key-value tables. A document
// with neither gets sensible defaults, so this never fails.
func (d *Doc) loadStyle() Style {
	s := Style{Theme: "auto", Accent: "#2563eb"}
	for _, tbl := range []string{"_style", "_head"} {
		if !d.hasTable(d.tables, tbl) {
			continue
		}
		rows, err := d.fg.Query(`SELECT key, COALESCE(value,'') FROM "` + tbl + `"`)
		if err != nil {
			continue
		}
		for rows.Next() {
			var k, v string
			if rows.Scan(&k, &v) != nil || v == "" {
				continue
			}
			switch strings.ToLower(k) {
			case "title":
				s.Title = v
			case "accent", "accent_color", "accentcolor":
				s.Accent = v
			case "theme", "dark_mode", "darkmode", "color_scheme":
				s.Theme = v
			}
		}
		rows.Close()
	}
	return s
}

// state returns the lazily-loaded state for a table, loading columns on the
// first call only.
func (d *Doc) state(name string) *tableState {
	d.mu.Lock()
	if d.states == nil {
		d.states = map[string]*tableState{}
	}
	ts, ok := d.states[name]
	if !ok {
		ts = &tableState{}
		d.states[name] = ts
	}
	d.mu.Unlock()

	ts.once.Do(func() {
		ts.quoted = quoteIdent(name)
		ts.cols, ts.err = d.columns(ts.quoted)
		if ts.err != nil {
			return
		}
		parts := make([]string, len(ts.cols))
		for i, c := range ts.cols {
			parts[i] = quoteIdent(c.Name)
		}
		ts.colExpr = strings.Join(parts, ",")
	})
	return ts
}

func (d *Doc) columns(quoted string) ([]Column, error) {
	rows, err := d.fg.Query("PRAGMA table_info(" + quoted + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []Column
	for rows.Next() {
		var cid int
		var name string
		var ctype sql.NullString
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		t := strings.ToUpper(ctype.String)
		cols = append(cols, Column{
			Name:    name,
			Type:    ctype.String,
			NotNull: notnull != 0,
			PK:      pk != 0,
			// Right-align anything with a numeric affinity, the way a
			// spreadsheet would.
			Numeric: strings.Contains(t, "INT") || strings.Contains(t, "REAL") ||
				strings.Contains(t, "NUM") || strings.Contains(t, "DEC") ||
				strings.Contains(t, "DOUB") || strings.Contains(t, "FLOA"),
		})
	}
	return cols, rows.Err()
}

// Columns returns the columns of a table, loading them if needed.
func (d *Doc) Columns(name string) ([]Column, error) {
	ts := d.state(name)
	return ts.cols, ts.err
}

// quoteIdent wraps an identifier in double quotes, escaping any it contains.
// Every identifier reaching SQL goes through here: table names like
// "1_recent_files" or "order" are only valid quoted.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// humanize turns snake_case table names into display labels.
func humanize(s string) string {
	t := strings.TrimPrefix(s, "_")
	t = strings.ReplaceAll(t, "_", " ")
	if t == "" {
		return s
	}
	words := strings.Fields(t)
	for i, w := range words {
		if len(w) > 1 && strings.ToUpper(w) == w {
			continue // keep acronyms
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
