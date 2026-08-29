//go:build !darwin || !cgo

package main

// installNativePicker is a macOS concern: elsewhere the dialog internal/pick
// already runs (zenity, kdialog, the Windows common dialog) belongs to no other
// application and leaves nothing in front of the window.
func installNativePicker() {}
