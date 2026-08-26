//go:build cgo

// Command sqldoc-view opens a SQLite file in a native window. It is the same
// viewer the browser gets, hosted in the system's own WebKit rather than in a
// tab, so a database can be a document you double-click.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/darianmavgo/sqldoc/internal/doc"
	"github.com/darianmavgo/sqldoc/internal/pick"
	"github.com/darianmavgo/sqldoc/internal/server"
	"github.com/darianmavgo/sqldoc/internal/session"
	webview "github.com/webview/webview_go"
)

// version is reported by /healthz, which is how a running window can be checked
// from outside the page it is showing.
const version = "0.2.0"

func main() {
	var paths []string
	immutable := false
	for _, a := range os.Args[1:] {
		switch {
		case a == "-immutable" || a == "--immutable":
			immutable = true
		case strings.HasPrefix(a, "-psn_"):
			// Launch Services passes a process serial number on older macOS.
		case strings.HasPrefix(a, "-"):
			// Ignore unknown flags rather than refusing to start: this binary
			// is launched by the OS, which passes arguments of its own.
		default:
			paths = append(paths, a)
		}
	}

	sess := session.New(doc.Options{Immutable: immutable})
	defer sess.CloseAll()

	var failures []string
	for _, p := range paths {
		if _, err := sess.Open(p); err != nil {
			failures = append(failures, err.Error())
		}
	}

	// Double-clicked with nothing to show: offer the picker straight away, and
	// if that is dismissed still open the window on the start page rather than
	// flashing and vanishing.
	if len(sess.List()) == 0 && len(paths) == 0 {
		if p, err := pick.Open(context.Background(), "Open a SQLite database"); err == nil {
			if _, err := sess.Open(p); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}
	if len(failures) > 0 {
		alert(strings.Join(failures, "\n\n"))
	}

	// The window talks to the same loopback server the browser does. Sharing
	// one implementation is what keeps the two front ends from drifting.
	server.Version = version
	s := server.New(sess)
	if err := s.Listen(0); err != nil {
		alert(err.Error())
		os.Exit(1)
	}
	defer s.Close()
	go s.Serve()

	title := "sqldoc"
	if e, ok := sess.First(); ok {
		if t := e.Doc.Style().Title; t != "" {
			title = t
		} else {
			title = e.Name
		}
	}

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(1200, 800, webview.HintNone)
	w.Navigate(s.URL())
	activate()
	w.Run()
}

// alert reports a failure where someone launching from Finder will see it,
// rather than on a stderr nobody is watching.
func alert(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	if runtime.GOOS != "darwin" {
		return
	}
	esc := strings.ReplaceAll(msg, `"`, `\"`)
	exec.Command("osascript", "-e",
		`display alert "sqldoc" message "`+esc+`" as critical`).Run()
}
