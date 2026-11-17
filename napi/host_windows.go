//go:build windows

package napi

/*
int napi_go_bind_host(const char** first_missing);
*/
import "C"

import "fmt"

// hostProblem describes the Node-API functions the host does not provide, or
// is empty when it provides them all. It is set once, by init.
var hostProblem string

// On Windows the Node-API functions are bound to the host process at run time
// (see host_windows.c). This runs when the library is loaded, before Node.js
// can call the module's registration function, so no call can get ahead of it.
func init() {
	var first *C.char
	missing := int(C.napi_go_bind_host(&first))
	if missing == 0 {
		return
	}
	hostProblem = fmt.Sprintf(
		"the process that loaded this addon does not provide %d of the Node-API functions it binds "+
			"(the first is %s); it needs a host with Node-API 8, and a missing function fails with napi_generic_failure",
		missing, C.GoString(first))
}

// hostFailure returns hostProblem when st is how a missing function fails, so
// that the error says why instead of just "napi_generic_failure".
func hostFailure(st Status) string {
	if st == StatusGenericFailure {
		return hostProblem
	}
	return ""
}
