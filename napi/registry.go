package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"fmt"
	"os"
	"runtime/cgo"
	"sync/atomic"
	"unsafe"
)

// Debug counters tracking the lifetime of callback boxes. Outstanding boxes
// should always return to zero once every JS object that carries a Go
// callback has been collected — the stress suite asserts exactly that.
var (
	boxesAllocated atomic.Uint64
	boxesFreed     atomic.Uint64
)

// DebugStats returns the cumulative number of callback boxes allocated and
// freed. Outstanding = allocated - freed; a permanently growing outstanding
// count indicates a leak in the binding or in the embedding.
func DebugStats() (allocated, freed uint64) {
	return boxesAllocated.Load(), boxesFreed.Load()
}

// This file contains the bridge between C callback pointers used by
// Node-API and Go functions. Every JS-facing Go function is registered in a
// cgo.Handle registry; a small C-allocated box holding the handle travels as
// the `void* data` of the corresponding napi object. A Node finalizer frees
// the box (and the handle) when the JS object is collected.

// boxHandle allocates a C box holding a cgo handle for v and returns it.
// It panics on OOM, which is recovered by the surrounding machinery.
func boxHandle(v any) unsafe.Pointer {
	h := cgo.NewHandle(v)
	p := C.go_box_new(C.uintptr_t(h))
	if p == nil {
		h.Delete()
		panic("node-api: out of memory allocating callback box")
	}
	boxesAllocated.Add(1)
	return p
}

// loadBox returns the Go value stored in a box. The box keeps owning the
// value; use freeBox to release it.
//
// A nil box panics rather than dereferencing NULL: the engine is allowed to
// hand a native callback a NULL data pointer (a C addon in the same process
// can create such a function), and a Go panic is recovered and reported to
// JavaScript whereas a NULL dereference takes the whole process down.
func loadBox(p unsafe.Pointer) any {
	if p == nil {
		panic("node-api: nil callback box")
	}
	return cgo.Handle(C.go_box_load(p)).Value()
}

// freeBox deletes the cgo handle stored in the box and frees the box
// memory.
func freeBox(p unsafe.Pointer) {
	if p == nil {
		return
	}
	h := cgo.Handle(C.go_box_load(p))
	C.go_box_free(p)
	h.Delete()
	boxesFreed.Add(1)
}

// unwrapPayload extracts the user-facing Go value from a box created by
// Wrap, CreateExternal, AddFinalizer or SetInstanceData. Those call sites
// deliberately park a finalizePayload (value + user finalizer) in a single
// box, so the payload has to be unwrapped before handing it back; every other
// box holds its value directly.
func unwrapPayload(p unsafe.Pointer) any {
	if v, ok := loadBox(p).(finalizePayload); ok {
		return v.data
	}
	return loadBox(p)
}

// panicToJS converts a recovered panic into a pending JS exception so that
// a Go panic never takes down the Node.js process.
func panicToJS(env Env, r any) {
	msg := fmt.Sprint(r)
	cmsg := C.CString(msg)
	defer C.free(unsafe.Pointer(cmsg))
	if st := C.napi_throw_error(C.napi_env(env), nil, cmsg); st != C.napi_ok {
		// Throwing failed (e.g. an exception is already pending, or we are
		// in a context where throwing is impossible). Log and move on.
		fmt.Fprintf(os.Stderr, "node-api: recovered panic (%s) and could not throw: %v\n", msg, st)
	}
}

// recoverToJS is used as `defer recoverToJS(env)` in trampolines that run on
// the JS thread.
func recoverToJS(env Env) {
	if r := recover(); r != nil {
		panicToJS(env, r)
	}
}

// recoverToLog is used in trampolines that run off the JS thread (async work
// execute, ...) where throwing is impossible.
func recoverToLog(what string) {
	if r := recover(); r != nil {
		fmt.Fprintf(os.Stderr, "node-api: panic in %s: %v\n", what, r)
	}
}

// napiGoExecuteCallback is the napi_callback passed for every function,
// method and constructor created from Go.
//
//export napiGoExecuteCallback
func napiGoExecuteCallback(cEnv C.napi_env, cInfo C.napi_callback_info) C.napi_value {
	env := Env(cEnv)
	defer recoverToJS(env)

	var data unsafe.Pointer
	C.napi_get_cb_info(cEnv, cInfo, nil, nil, nil, &data)
	if data == nil {
		panicToJS(env, "node-api: callback invoked without Go data")
		return nil
	}
	d, ok := loadBox(data).(cbData)
	if !ok || d.cb == nil {
		panicToJS(env, "node-api: callback data missing")
		return nil
	}
	ret := d.cb(env, CallbackInfo(cInfo))
	if ret == nil {
		return nil // returning NULL means `undefined` in Node-API
	}
	return C.napi_value(ret)
}

// napiGoGetterCallback is the napi_callback passed for accessors.
//
//export napiGoGetterCallback
func napiGoGetterCallback(cEnv C.napi_env, cInfo C.napi_callback_info) C.napi_value {
	env := Env(cEnv)
	defer recoverToJS(env)

	var data unsafe.Pointer
	C.napi_get_cb_info(cEnv, cInfo, nil, nil, nil, &data)
	if data == nil {
		panicToJS(env, "node-api: getter invoked without Go data")
		return nil
	}
	d, ok := loadBox(data).(cbData)
	if !ok || d.get == nil {
		panicToJS(env, "node-api: getter data missing")
		return nil
	}
	ret := d.get(env, CallbackInfo(cInfo))
	if ret == nil {
		return nil
	}
	return C.napi_value(ret)
}

// napiGoSetterCallback is the napi_callback passed for accessors.
//
//export napiGoSetterCallback
func napiGoSetterCallback(cEnv C.napi_env, cInfo C.napi_callback_info) C.napi_value {
	env := Env(cEnv)
	defer recoverToJS(env)

	var data unsafe.Pointer
	C.napi_get_cb_info(cEnv, cInfo, nil, nil, nil, &data)
	if data == nil {
		panicToJS(env, "node-api: setter invoked without Go data")
		return nil
	}
	d, ok := loadBox(data).(cbData)
	if !ok || d.set == nil {
		panicToJS(env, "node-api: setter data missing")
		return nil
	}
	d.set(env, CallbackInfo(cInfo))
	return nil
}

// finalizePayload is what a finalizer-owned box holds when a user finalizer
// is involved: the attached Go value plus the user's callback.
//
// Both travel in ONE box on purpose. napi_remove_wrap hands the attached
// value back and guarantees the finalizer will never run, so at that point
// the data box is the only handle anybody has on the pair — a separate hint
// box would be unreachable and leak on every remove_wrap. (External
// ArrayBuffers/Buffers cannot use this scheme: Node-API forces
// finalize_data to be the raw external pointer there, hence the hint box in
// external_finalize.go.)
type finalizePayload struct {
	data any
	fn   FinalizeFunc
}

// napiGoFinalizeCallback is the universal napi_finalize for boxes created by
// CreateFunction, CreateExternal, Wrap, AddFinalizer and SetInstanceData.
//
// Convention: cData is a box holding either a finalizePayload (value plus
// optional user finalizer) or a bare cbData (plain callback box). cHint is
// unused and always nil for these call sites.
//
//export napiGoFinalizeCallback
func napiGoFinalizeCallback(cEnv C.napi_env, cData, cHint unsafe.Pointer) {
	if cData == nil {
		return
	}
	env := Env(cEnv)
	// The box is released exactly once by this defer. Doing it inline would
	// leak the box (and its cgo handle) whenever the user finalizer panics:
	// the panic unwinds past the rest of the function straight into the
	// recover below.
	defer func() {
		if r := recover(); r != nil {
			// Finalizers run during GC; throwing is not possible. Log it.
			fmt.Fprintf(os.Stderr, "node-api: panic in finalizer: %v\n", r)
		}
	}()
	defer freeBox(cData)

	if cHint != nil {
		// Defensive: no caller passes a hint here any more, but if an old
		// caller did, the hint box must still be released.
		freeBox(cHint)
	}
	switch p := loadBox(cData).(type) {
	case finalizePayload:
		if p.fn != nil {
			p.fn(env, p.data)
		}
	case cbData:
		// Plain callback box: nothing user-visible to run.
	default:
		fmt.Fprintf(os.Stderr, "node-api: unknown finalizer payload %T\n", p)
	}
}

// napiGoCleanupHook is the trampoline for env cleanup hooks.
//
//export napiGoCleanupHook
func napiGoCleanupHook(cArg unsafe.Pointer) {
	if cArg == nil {
		return
	}
	// A cleanup hook runs once. Release the box by defer so a malformed
	// payload or a panicking hook cannot leak it (the old inline free after an
	// unchecked assertion leaked on both).
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "node-api: panic in env cleanup hook: %v\n", r)
		}
	}()
	defer freeBox(cArg)

	fn, ok := loadBox(cArg).(CleanupHook)
	if !ok || fn == nil {
		fmt.Fprintf(os.Stderr, "node-api: env cleanup hook has no Go function\n")
		return
	}
	fn()
}

// napiGoAsyncCleanupHook is the trampoline for async cleanup hooks.
//
// Two invariants, and they pull in opposite directions:
//
//   - The Go callback must run *unconditionally*. The engine is holding an
//     async handshake open; only the addon calling
//     napi_remove_async_cleanup_hook (normally from inside this callback)
//     deletes the handle, and only that destructor runs
//     AsyncCleanupHookInfo::done_cb_. Returning early here hangs environment
//     teardown forever — measured as a process that never exits.
//   - The cgo handle behind cArg is owned by the handle→box mapping in
//     node_api.go, not by this frame. RemoveAsyncCleanupHook may already have
//     freed it, in which case cArg must not be dereferenced.
//
// So: read the closure while the box is still known good, release the box only
// if this frame actually claims the mapping, then call the user function.
//
//export napiGoAsyncCleanupHook
func napiGoAsyncCleanupHook(cHandle C.napi_async_cleanup_hook_handle, cArg unsafe.Pointer) {
	if cArg == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "node-api: panic in async cleanup hook: %v\n", r)
		}
	}()

	fn, ok := loadBox(cArg).(AsyncCleanupHookFunc)
	if !ok || fn == nil {
		fmt.Fprintf(os.Stderr, "node-api: async cleanup hook has no Go function\n")
		return
	}
	// Release the cgo handle exactly once: whichever of the two callers (this
	// trampoline or RemoveAsyncCleanupHook) claims the mapping frees it.
	if box, claimed := asyncCleanupBoxes.LoadAndDelete(AsyncCleanupHookHandle(cHandle)); claimed {
		if p, ok := box.(unsafe.Pointer); ok && p == cArg {
			freeBox(cArg)
		}
	}
	fn(AsyncCleanupHookHandle(cHandle))
}

// asyncWorkData carries the user callbacks of an async work item. owner is
// back-pointer to the AsyncWork so the complete trampoline can move its
// state machine forward before the user callback runs.
type asyncWorkData struct {
	execute  func(Env)
	complete func(Env, error)
	owner    *AsyncWork
}

// napiGoAsyncExecute runs on a libuv worker thread. Only "basic" Node-API
// calls are legal here; we pass the env through but the user is expected to
// do pure Go work.
//
//export napiGoAsyncExecute
func napiGoAsyncExecute(cEnv C.napi_env, cData unsafe.Pointer) {
	defer recoverToLog("async work execute")
	d, ok := loadBox(cData).(*asyncWorkData)
	if !ok || d.execute == nil {
		return
	}
	d.execute(Env(cEnv))
}

// napiGoAsyncComplete runs on the JS thread when the work item finishes. The
// work is marked done *before* the user callback runs so that the canonical
// "delete the work from its own completion callback" pattern is legal.
//
//export napiGoAsyncComplete
func napiGoAsyncComplete(cEnv C.napi_env, cStatus C.napi_status, cData unsafe.Pointer) {
	env := Env(cEnv)
	defer recoverToJS(env)
	d, ok := loadBox(cData).(*asyncWorkData)
	if !ok {
		return
	}
	if d.owner != nil {
		d.owner.state.Store(asyncDone)
	}
	if d.complete != nil {
		d.complete(env, errOf(env, Status(cStatus)))
	}
}

// tsfnCall carries the payload of one threadsafe-function dispatch.
type tsfnCall struct {
	fn func(Env, Value)
}

// napiGoTSFNCallJs runs on the JS thread for every queued TSFN call. The
// data pointer is a box holding a tsfnCall.
//
// Node's TSFN dispatch (napi_threadsafe_function CallJs/DispatchOne) already
// runs the callback inside a v8 HandleScope, so payloads may create values
// without opening one themselves. When the function is released or aborted
// with undelivered items, Node calls this with a NULL env (and possibly a
// NULL js_callback) purely so the payload can be freed — calling into JS is
// illegal there, so the payload is dropped.
//
//export napiGoTSFNCallJs
func napiGoTSFNCallJs(cEnv C.napi_env, cJsCallback C.napi_value, cContext, cData unsafe.Pointer) {
	if cData == nil {
		return
	}
	// The payload box is released exactly once, whatever happens below: a
	// malformed payload, a NULL env (Node drains the backlog purely so the
	// payload can be freed), or a panic in the user closure. Freeing it by
	// defer is also what keeps a panicking callback from double-freeing the
	// box on the unwind path.
	defer freeBox(cData)
	// A malformed payload must not unwind into the engine: recover here and
	// log instead.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "node-api: panic dispatching threadsafe call: %v\n", r)
		}
	}()

	call, ok := loadBox(cData).(*tsfnCall)
	if !ok || cEnv == nil {
		// Environment is gone; nothing can be dispatched. The closure is
		// garbage collected along with the freed box.
		return
	}
	fn := call.fn
	if fn == nil {
		return
	}

	env := Env(cEnv)
	func() {
		// Registered in an inner frame so that a panic from the user closure
		// surfaces as a JS exception instead of only a log line.
		defer recoverToJS(env)
		fn(env, Value(cJsCallback))
	}()
}
