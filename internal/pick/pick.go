// Package pick shows the operating system's own open-file dialog.
//
// A web page cannot learn the path of a file the user chooses — browsers
// deliberately hide it, and hand over bytes instead. Since this server is the
// user's own machine, it can ask the OS directly and get a real path, which is
// what makes "Open…" as cheap as passing a filename on the command line even
// for a database of hundreds of megabytes.
package pick

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrCancelled is returned when the dialog was dismissed without a choice.
var ErrCancelled = errors.New("cancelled")

// ErrUnsupported is returned where no dialog program could be found.
var ErrUnsupported = errors.New("no file dialog available on this system")

// Databases is the file filter offered by the dialog.
var Databases = []string{"db", "sqlite", "sqlite3", "db3", "s3db", "sl3"}

// Native, when set, is an open dialog belonging to the running application
// itself, and it is used in preference to everything below.
//
// The scripted dialogs here are owned by some other program, because that is
// the only kind a process without an event loop can put on screen. They work,
// and they are what a command-line front end has to use, but they end badly on
// macOS: the application that owned the dialog is still in front when it
// closes, and the window the database was chosen for is left behind it. A front
// end with a real window can do what every other Mac application does and run
// the panel itself, attached to that window; when one can, it registers it
// here. See cmd/sqldoc-view/openpanel_darwin.m.
var Native func(ctx context.Context, prompt string) (string, error)

// Open shows an open-file dialog and returns the chosen path.
func Open(ctx context.Context, prompt string) (string, error) {
	// The dialog is modal and waits on a person, so the only deadline that
	// makes sense is a generous one that stops a forgotten dialog from pinning
	// the process forever.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if Native != nil {
		return Native(ctx, prompt)
	}

	switch runtime.GOOS {
	case "darwin":
		return darwin(ctx, prompt)
	case "windows":
		return windows(ctx, prompt)
	default:
		return unix(ctx, prompt)
	}
}

func darwin(ctx context.Context, prompt string) (string, error) {
	// Two things about this script are load-bearing.
	//
	// The dialog is asked for inside a Finder tell block that activates first.
	// osascript launched from a background server has no window layer that can
	// come forward on its own, so an unwrapped "choose file" opens *behind* the
	// browser — the button appears to do nothing at all, which is the worst
	// possible failure for the one control whose entire job is to open a file.
	// Finder is always running, is always allowed to come to the front, and
	// owning a file dialog is not a strange thing for it to be doing.
	//
	// There is no type filter. Restricting to a list of extensions greys out
	// databases named anything else, and SQLite files routinely carry an
	// unexpected extension or none at all. A picker that will not let you
	// select the file you are pointing at is worse than one that accepts
	// anything and reports a clear error when it is not a database.
	script := `tell application "Finder"
	activate
	set theFile to choose file with prompt "` + escapeAS(prompt) + `"
end tell
POSIX path of theFile`
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	return finish(string(out), err)
}

func unix(ctx context.Context, prompt string) (string, error) {
	filter := "*." + strings.Join(Databases, " *.")
	if _, err := exec.LookPath("zenity"); err == nil {
		// "All files" is offered first so a database with an unusual name is
		// never unselectable.
		out, err := exec.CommandContext(ctx, "zenity", "--file-selection",
			"--title="+prompt,
			"--file-filter=All files | *",
			"--file-filter=SQLite databases | "+filter).Output()
		return finish(string(out), err)
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		out, err := exec.CommandContext(ctx, "kdialog", "--getopenfilename",
			".", filter+"|SQLite databases").Output()
		return finish(string(out), err)
	}
	return "", ErrUnsupported
}

func windows(ctx context.Context, prompt string) (string, error) {
	filter := "SQLite databases|*." + strings.Join(Databases, ";*.") + "|All files|*.*"
	ps := `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d = New-Object System.Windows.Forms.OpenFileDialog;` +
		`$d.Title = '` + strings.ReplaceAll(prompt, "'", "''") + `';` +
		`$d.Filter = '` + filter + `';` +
		`if ($d.ShowDialog() -eq 'OK') { [Console]::Out.Write($d.FileName) }`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile",
		"-NonInteractive", "-Command", ps).Output()
	return finish(string(out), err)
}

// finish maps a dialog's output to a path. Every dialog reports cancellation as
// a non-zero exit or empty output, which is a normal outcome rather than a
// failure worth showing anyone.
func finish(out string, err error) (string, error) {
	path := strings.TrimSpace(out)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", ErrCancelled
		}
		return "", err
	}
	if path == "" {
		return "", ErrCancelled
	}
	return path, nil
}

// Available reports whether a dialog can be shown, so the viewer can hide the
// Open button rather than offer one that fails.
func Available() bool {
	if Native != nil {
		return true
	}
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	case "windows":
		_, err := exec.LookPath("powershell")
		return err == nil
	default:
		if _, err := exec.LookPath("zenity"); err == nil {
			return true
		}
		_, err := exec.LookPath("kdialog")
		return err == nil
	}
}

func escapeAS(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}
