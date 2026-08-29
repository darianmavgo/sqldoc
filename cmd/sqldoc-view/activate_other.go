//go:build !darwin || !cgo

package main

// activate is only needed on macOS, where a bundled process does not become a
// foreground application on its own.
func activate() {}

// setupMenu installs standard native menus on macOS.
func setupMenu() {}

// fillScreen is a macOS implementation for now: the window managers behind the
// other WebView backends want to be asked in their own terms, and a window that
// merely opens large is not worth guessing at them for.
func fillScreen() {}
