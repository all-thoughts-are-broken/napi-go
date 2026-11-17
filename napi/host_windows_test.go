//go:build windows

package napi

import (
	"strings"
	"testing"
)

// A test binary is not a Node.js host: no module of the process exports
// Node-API, so binding finds nothing. That is exactly the situation an addon is
// in when it is loaded by a host that lacks a function, and it must come out as
// an error that says so — not as a call through a null pointer.
func TestMissingHostFunctionsFailWithAnExplanation(t *testing.T) {
	if hostProblem == "" {
		t.Fatal("a process without Node-API was reported as a complete host")
	}

	_, err := GetVersion(nil)
	code, ok := AsStatus(err)
	if !ok || code != StatusGenericFailure {
		t.Fatalf("GetVersion against a missing function = %v, want napi_generic_failure", err)
	}
	if !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("the error does not explain itself: %v", err)
	}

	// A status that a missing function cannot produce is left alone.
	if got := hostFailure(StatusInvalidArg); got != "" {
		t.Fatalf("hostFailure(napi_invalid_arg) = %q, want nothing", got)
	}
}
