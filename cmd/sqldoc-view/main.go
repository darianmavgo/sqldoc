//go:build cgo

// Command sqldoc-view opens a SQLite file in a native window. It is the same
// viewer the browser gets, hosted in the system's own WebKit rather than in a
// tab, so a database can be a document you double-click.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/darianmavgo/sqldoc/internal/doc"
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

	// Registered before the server can serve a request that wants a dialog.
	installNativePicker()

	sess := session.New(doc.Options{Immutable: immutable})
	defer sess.CloseAll()

	// The window talks to the same loopback server the browser does. Sharing
	// one implementation is what keeps the two front ends from drifting.
	server.Version = version
	s := server.New(sess)
	// A scripted dialog leaves whichever application owned it in front. The
	// native panel does not, but it is not available on every platform, so the
	// window keeps a way to bring itself back.
	s.Activate = activate
	if err := s.Listen(0); err != nil {
		alert(err.Error())
		os.Exit(1)
	}
	defer s.Close()
	go s.Serve()

	// Nothing about the documents happens before the window exists.
	//
	// Opening them first is the obvious order and it is wrong, because opening
	// a file is not something this process controls the duration of. macOS
	// holds open() inside the kernel while it asks whether an application may
	// read your Documents folder; a file on a network or cloud volume is
	// fetched before the call returns; a database with a hot journal is
	// recovered. Any of those with the window not yet created is an application
	// that launches, shows nothing at all, and looks broken - which is exactly
	// what it was doing.
	//
	// So: window first, always, and the documents arrive in it.
	url := s.URL()
	switch {
	case len(paths) > 0:
		url += "&opening=" + strconv.Itoa(len(paths))
	default:
		// Nothing to show. Ask for a file, but ask through the window rather
		// than before it: a panel this application owns can only be drawn by a
		// run loop that is already turning, so going through the page is what
		// makes it a sheet on the viewer's own window, and what makes
		// dismissing it land on the start page instead of on nothing.
		url += "&pick=1"
	}

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("sqldoc")
	w.SetSize(1200, 800, webview.HintNone)
	w.Navigate(url)
	activate()
	// SetSize above is the size to fall back to; this is the size it opens at
	// where the platform can say what "the screen" means.
	fillScreen()

	// Dispatch runs on the UI thread once the loop is turning, which is the one
	// place the title and the page can be touched from here.
	go func() {
		var failures []string
		for _, p := range paths {
			if _, err := sess.Open(p); err != nil {
				failures = append(failures, err.Error())
			}
		}
		w.Dispatch(func() {
			if e, ok := sess.First(); ok {
				if t := e.Doc.Style().Title; t != "" {
					w.SetTitle(t)
				} else {
					w.SetTitle(e.Name)
				}
			}
			w.Eval("window.sqldocOpened && window.sqldocOpened()")
		})
		// Reported after the window is up, so a database that will not open is
		// a message over a window rather than the only thing that ever appears.
		if len(failures) > 0 {
			alert(strings.Join(failures, "\n\n"))
		}
	}()

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
