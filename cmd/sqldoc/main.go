// Command sqldoc opens a SQLite file as a document.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/darianmavgo/sqldoc/internal/doc"
	"github.com/darianmavgo/sqldoc/internal/server"
	"github.com/darianmavgo/sqldoc/internal/session"
)

const usage = `sqldoc — open a SQLite database as a document

Usage:
  sqldoc                        open the start page: recent files, drag and drop
  sqldoc <file.db> [more.db...] open one or more databases in your browser
  sqldoc serve [file.db...]     serve them and print the URL
  sqldoc info  <file.db>        print what the document contains
  sqldoc bench <file.db>        measure how fast it reads

Flags:
  -p <port>     port to listen on (default: any free port)
  -immutable    promise the file will not change while open; skips locking and
                WAL recovery for a faster cold start. Unsafe if anything else
                writes the file.
  -no-open      do not launch a browser
  -version      print version and the SQLite driver in use
`

var version = "0.2.0"

func main() {
	log := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

	var (
		port      = flag.Int("p", 0, "port")
		immutable = flag.Bool("immutable", false, "open immutable")
		noOpen    = flag.Bool("no-open", false, "do not launch a browser")
		showVer   = flag.Bool("version", false, "print version")
	)
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }

	// The subcommand is lifted out before flags are parsed so that flags may be
	// written on either side of it, which is what people actually type.
	cmd, rest := "open", os.Args[1:]
	for i, a := range rest {
		switch a {
		case "serve", "info", "bench", "open":
			cmd = a
			rest = append(append([]string{}, rest[:i]...), rest[i+1:]...)
		}
		if cmd != "open" {
			break
		}
	}
	flag.CommandLine.Parse(rest)

	if *showVer {
		fmt.Printf("sqldoc %s · %s\n", version, doc.DriverLabel)
		return
	}

	args := flag.Args()
	opt := doc.Options{Immutable: *immutable}

	switch cmd {
	case "info", "bench":
		// These inspect one database and print; there is nothing to open.
		if len(args) == 0 {
			flag.Usage()
			os.Exit(2)
		}
		var err error
		if cmd == "info" {
			err = info(args[0], opt)
		} else {
			err = bench(args[0], opt)
		}
		if err != nil {
			log("sqldoc: %v", err)
			os.Exit(1)
		}
	default:
		// No arguments is not an error: it opens the start page, the same way
		// a browser with no URL still gives you somewhere to start.
		if err := serve(args, opt, *port, !*noOpen); err != nil {
			log("sqldoc: %v", err)
			os.Exit(1)
		}
	}
}

func serve(paths []string, opt doc.Options, port int, launch bool) error {
	start := time.Now()

	sess := session.New(opt)
	defer sess.CloseAll()

	// One bad path among several should not stop the rest from opening; the
	// failures are reported and the viewer still comes up.
	var failed int
	for _, p := range paths {
		e, err := sess.Open(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sqldoc: %v\n", err)
			failed++
			continue
		}
		fmt.Printf("%s  %s  (%d tables)\n",
			e.Name, humanBytes(e.Size), len(e.Doc.Tables()))
	}
	if failed > 0 && failed == len(paths) {
		return fmt.Errorf("nothing could be opened")
	}

	server.Version = version
	s := server.New(sess)
	if err := s.Listen(port); err != nil {
		return err
	}
	defer s.Close()

	url := s.URL()
	if len(paths) == 0 {
		fmt.Println("sqldoc — no database given, opening the start page")
	} else {
		fmt.Printf("ready in %s\n", time.Since(start).Round(100*time.Microsecond))
	}
	fmt.Println(url)

	go func() {
		if launch {
			openBrowser(url)
		}
	}()

	// Ctrl-C shuts the server down instead of leaving a listener behind.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() { errc <- s.Serve() }()

	select {
	case <-sig:
		fmt.Println()
		return nil
	case err := <-errc:
		return err
	}
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

func info(path string, opt doc.Options) error {
	d, err := doc.Open(path, opt)
	if err != nil {
		return err
	}
	defer d.Close()

	fmt.Printf("%s\n%s · %s · %s\n\n", d.Path, humanBytes(d.Size),
		d.Modified.Format("2006-01-02 15:04"), doc.DriverLabel)

	fmt.Printf("%-28s %-6s %10s  %s\n", "TABLE", "TYPE", "ROWS", "COLUMNS")
	for _, t := range d.Tables() {
		cols, _ := d.Columns(t.Name)
		c := d.Count(t.Name)
		rows := "?"
		if c.Known {
			rows = fmt.Sprintf("%d", c.Rows)
			if !c.Exact {
				rows += "~"
			}
		}
		name := t.Name
		if t.Hidden {
			name += " (meta)"
		}
		fmt.Printf("%-28s %-6s %10s  %d\n", trunc(name, 28), t.Type, rows, len(cols))
	}
	return nil
}

// bench measures the four numbers that decide whether this feels like a
// document viewer: how long until something is on screen, how fast a screen of
// rows arrives while scrolling, how long a jump to an arbitrary position takes,
// and what the naive alternative would have cost.
func bench(path string, opt doc.Options) error {
	ctx := context.Background()

	t0 := time.Now()
	d, err := doc.Open(path, opt)
	if err != nil {
		return err
	}
	defer d.Close()
	openMS := time.Since(t0)

	// Pick the largest table by estimate; that is the one worth measuring.
	var target string
	var best int64 = -1
	for _, t := range d.Tables() {
		if t.Hidden {
			continue
		}
		if c := d.Count(t.Name); c.Known && c.Rows > best {
			best, target = c.Rows, t.Name
		} else if target == "" {
			target = t.Name
		}
	}
	if target == "" {
		return fmt.Errorf("no tables to benchmark")
	}

	t1 := time.Now()
	first, err := d.Rows(ctx, doc.Window{Table: target, Limit: 100})
	if err != nil {
		return err
	}
	firstPaint := time.Since(t1)

	fmt.Printf("%s · %s · %s\n", filepath.Base(d.Path), humanBytes(d.Size), doc.DriverLabel)
	fmt.Printf("table %q, %d columns\n\n", target, len(first.Columns))

	fmt.Printf("  open (metadata only)      %8s\n", round(openMS))
	fmt.Printf("  first window (100 rows)   %8s   [%s]\n", round(firstPaint), first.Path)

	c := d.Count(target)
	label := "estimate"
	if c.Exact {
		label = "exact"
	}
	fmt.Printf("  row count at open         %8s   [%s: %s rows]\n", "0s", label, comma(c.Rows))

	// Wait for the background exact count so the seek tests have a real axis.
	t2 := time.Now()
	for i := 0; i < 1200 && !d.Count(target).Exact; i++ {
		time.Sleep(25 * time.Millisecond)
	}
	settle := time.Since(t2)
	total := d.Count(target).Rows
	if total == 0 {
		fmt.Println("\n  (table is empty)")
		return nil
	}
	fmt.Printf("  exact count settled after %8s   [%s rows, in the background]\n\n",
		round(settle), comma(total))

	// Sequential scroll: the path taken while someone holds the scroll wheel.
	seq := sample(60, func(i int) time.Duration {
		after := int64(0)
		if i > 0 {
			after = int64(i) * 100
		}
		s := time.Now()
		d.Rows(ctx, doc.Window{Table: target, Limit: 100, After: after, UseAfter: after > 0})
		return time.Since(s)
	})
	report("scroll (keyset, 100 rows)", seq)

	// Random seeks: the path taken when the scrollbar is dragged.
	rnd := rand.New(rand.NewSource(7))
	seek := sample(40, func(int) time.Duration {
		off := rnd.Int63n(total)
		s := time.Now()
		d.Rows(ctx, doc.Window{Table: target, Limit: 100, Offset: off})
		return time.Since(s)
	})
	report("seek to random position", seek)

	// The same seeks done the way a naive viewer does them, for contrast.
	deep := total * 9 / 10
	naive := sample(5, func(int) time.Duration {
		s := time.Now()
		d.Rows(ctx, doc.Window{Table: target, Limit: 100, Offset: deep, ForceOffset: true})
		return time.Since(s)
	})
	report(fmt.Sprintf("same seek, plain LIMIT/OFFSET"), naive)

	fmt.Println()
	return nil
}

func sample(n int, f func(int) time.Duration) []time.Duration {
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, f(i))
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func report(label string, d []time.Duration) {
	if len(d) == 0 {
		return
	}
	p50 := d[len(d)/2]
	p99 := d[min(len(d)-1, len(d)*99/100)]
	fmt.Printf("  %-32s p50 %8s   p99 %8s\n", label, round(p50), round(p99))
}

func round(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000)
	default:
		return d.Round(time.Millisecond).String()
	}
}

func comma(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
