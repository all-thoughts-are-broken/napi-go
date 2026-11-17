package napi

import (
	"fmt"
	"os"
)

// The addon must never be unloaded: the Go runtime inside it cannot be torn
// down, so unmapping the library leaves its threads running in freed memory.
// Each platform's pinModule (pin_windows.go, pin_darwin.go, pin_other.go) makes
// that impossible; this runs it once, when the library is loaded.
func init() {
	if !pinModule() {
		fmt.Fprintln(os.Stderr, "node-api: could not pin the addon in memory; "+
			"it will crash if Node.js unloads it (for example when the only Worker that loaded it exits)")
	}
}
