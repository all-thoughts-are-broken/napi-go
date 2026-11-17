//go:build !windows

package napi

// hostFailure has nothing to report here: outside Windows the Node-API
// functions are ordinary undefined symbols that the dynamic loader resolves
// against the host process, and a host that lacks one fails at load time.
func hostFailure(Status) string { return "" }
