//go:build !darwin || !cgo

package main

// activate is only needed on macOS, where a bundled process does not become a
// foreground application on its own.
func activate() {}
