package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

// externalFinalizeData travels as the finalize hint when JS collects an
// external ArrayBuffer or Buffer. The finalize_data slot cannot be used for
// it: Node-API forces finalize_data to be the raw external pointer, which
// our box convention must never be applied to.
type externalFinalizeData struct {
	data any
	fn   FinalizeFunc
}

// napiGoExternalFinalizeCallback is the napi_finalize for external
// ArrayBuffers and Buffers. Layout:
//
//	cData is the RAW external pointer (engine-owned, never a box);
//	cHint, when non-NULL, is a box holding *externalFinalizeData.
//
//export napiGoExternalFinalizeCallback
func napiGoExternalFinalizeCallback(cEnv C.napi_env, cData, cHint unsafe.Pointer) {
	env := Env(cEnv)
	defer func() {
		if r := recover(); r != nil {
			// Finalizers run during GC; throwing is not possible. Log it.
			fmt.Fprintf(os.Stderr, "node-api: panic in external finalizer: %v\n", r)
		}
	}()

	if cHint == nil {
		return // no Go payload was attached
	}
	// Release the hint box exactly once, whatever happens below. Freeing it
	// inline would skip the release whenever the user finalizer panics (the
	// panic unwinds past the rest of the function) and would double-free it
	// on any path that also recovers.
	defer freeBox(cHint)
	fd, ok := loadBox(cHint).(*externalFinalizeData)
	if !ok || fd == nil {
		return
	}
	if fd.fn != nil {
		fd.fn(env, fd.data)
	}
}
