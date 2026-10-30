//go:build !windows && !darwin

package main

// Other systems (development on Linux): the core only opens the local
// proxy port; point a browser or curl at it by hand.

func setProxy(port int) error { return nil }

func restoreProxy() error { return nil }

// onPrimary is only used on macOS (restore reports early there).
var onPrimary func()
