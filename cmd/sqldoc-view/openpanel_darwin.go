//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "openpanel_darwin.h"
*/
import "C"

import (
	"context"
	"runtime/cgo"
	"unsafe"

	"github.com/darianmavgo/sqldoc/internal/pick"
)

// installNativePicker gives internal/pick a dialog belonging to this
// application, so it stops asking Finder for one. Only a front end with a
// window and a run loop can offer this, which is why it is registered here
// rather than chosen inside the package.
func installNativePicker() { pick.Native = openPanel }

// openPanel shows the panel and waits for an answer.
//
// The deadline internal/pick puts on the context is not honoured, and the
// context is ignored entirely. It exists to stop a forgotten dialog in another
// process from pinning this one forever; this panel is in this process, on this
// window, in front of the person who asked for it. Giving up on it after five
// minutes would mean throwing away the file they then chose.
func openPanel(_ context.Context, prompt string) (string, error) {
	// Buffered, and the handle is released by the callback: the answer has to
	// have somewhere to go even if nothing is waiting for it any more.
	ch := make(chan string, 1)
	h := cgo.NewHandle(ch)

	cprompt := C.CString(prompt)
	defer C.free(unsafe.Pointer(cprompt))
	C.sqldocOpenPanel(cprompt, C.uintptr_t(h))

	path := <-ch
	if path == "" {
		return "", pick.ErrCancelled
	}
	return path, nil
}

//export sqldocPanelDone
func sqldocPanelDone(handle C.uintptr_t, path *C.char) {
	h := cgo.Handle(handle)
	ch, _ := h.Value().(chan string)
	h.Delete()
	if ch == nil {
		return
	}
	if path == nil {
		ch <- ""
		return
	}
	ch <- C.GoString(path)
}
