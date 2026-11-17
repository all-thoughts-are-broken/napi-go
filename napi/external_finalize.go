package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"
)

// externalFinalizeData travels as the finalize hint when JS collects an
// external ArrayBuffer or Buffer. The finalize_data slot cannot be used for
// it: Node-API forces finalize_data to be the raw external pointer, which
// our box convention must never be applied to.
type externalFinalizeData struct {
	data any
	fn   FinalizeFunc
	// pinner pins the Go memory the engine was given, when there is any (the
	// FromBytes constructors). It is released once the engine is done with the
	// memory, which is exactly when this finalizer runs.
	pinner *runtime.Pinner
}

// pinnedBytes builds the finalize data for an external buffer over b: it keeps
// b reachable and pins its storage for as long as the engine points at it.
//
// Reachability alone is what keeps the memory from being freed, and with the
// current collector, which never moves heap objects, it is also enough to keep
// the address valid. But that is a property of the implementation: the cgo
// rules only let C keep a Go pointer past the call that passed it while the
// memory is pinned, and pinning is what turns "the collector does not move
// this today" into a guarantee.
func pinnedBytes(b []byte, fn FinalizeFunc) *externalFinalizeData {
	fd := &externalFinalizeData{data: b, fn: fn, pinner: new(runtime.Pinner)}
	fd.pinner.Pin(&b[0])
	return fd
}

// unpin releases the pin, if there is one. It is safe to call more than once.
func (fd *externalFinalizeData) unpin() {
	if fd.pinner != nil {
		fd.pinner.Unpin()
		fd.pinner = nil
	}
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
	// The engine no longer points at the memory; a deferred unpin also covers
	// a finalizer that panics.
	defer fd.unpin()
	if fd.fn != nil {
		fd.fn(env, fd.data)
	}
}
