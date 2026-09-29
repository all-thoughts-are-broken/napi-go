package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"unsafe"
)

// This file covers handle scopes, references, instance data and external
// memory accounting.

// staleScopeErr reports a scope handle that this package will not act on.
func staleScopeErr(kind string) error {
	return &StatusError{
		Code: StatusInvalidArg,
		Message: kind + " is not live (already closed, or opened by the other " +
			"scope opener)",
	}
}

// ---- handle scopes --------------------------------------------------------

// handleScopeTokens and escapableScopeTokens hold the scopes opened through
// this package that have not been closed yet.
//
// The engine's only guard on a close is a per-environment open_handle_scopes
// counter that refuses once the count reaches zero. That counter does not catch
// the actual mistake: closing the *same* scope twice while another one is still
// open passes the check and deletes the engine object twice — measured as exit
// 0xC0000374 (heap corruption). Keeping the live set also catches closing a
// plain scope as an escapable one and vice versa, which would reinterpret the
// object and is not detectable in any other way.
//
// A separate set per kind, and tokens rather than the engine's own pointers: a
// scope token presented as a reference is then refused outright, and a closed
// scope stays distinguishable from a live one even when the allocator hands its
// address straight back to the next scope (measured: 8 of 64 rounds, every one
// of them accepted before the tokens existed).
var (
	handleScopeTokens    = newTokenSet("handle scope")
	escapableScopeTokens = newTokenSet("escapable handle scope")
)

// OpenHandleScope opens a scope in which new Values are registered. Values
// created inside the scope are freed when it closes, unless escaped with
// EscapeHandle from an EscapableHandleScope.
//
// Node-API already opens a scope around every native callback, so scopes are
// only needed when creating many values in a loop (to bound memory) or when
// calling into JS outside of a callback.
func OpenHandleScope(env Env) (HandleScope, error) {
	var raw C.napi_handle_scope
	st := C.napi_open_handle_scope(C.napi_env(env), &raw)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if raw == nil {
		return nil, nil
	}
	return HandleScope(handleScopeTokens.issue(unsafe.Pointer(raw))), nil
}

// CloseHandleScope closes a scope opened with OpenHandleScope. Closing the
// same scope twice, or a scope opened with OpenEscapableHandleScope, returns
// napi_invalid_arg rather than corrupting the heap.
func CloseHandleScope(env Env, scope HandleScope) error {
	raw, live := handleScopeTokens.retire(unsafe.Pointer(scope))
	if !live {
		return staleScopeErr("handle scope")
	}
	st := C.napi_close_handle_scope(C.napi_env(env), C.napi_handle_scope(raw))
	return errOf(env, Status(st))
}

func OpenEscapableHandleScope(env Env) (EscapableHandleScope, error) {
	var raw C.napi_escapable_handle_scope
	st := C.napi_open_escapable_handle_scope(C.napi_env(env), &raw)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if raw == nil {
		return nil, nil
	}
	return EscapableHandleScope(escapableScopeTokens.issue(unsafe.Pointer(raw))), nil
}

func CloseEscapableHandleScope(env Env, scope EscapableHandleScope) error {
	raw, live := escapableScopeTokens.retire(unsafe.Pointer(scope))
	if !live {
		return staleScopeErr("escapable handle scope")
	}
	st := C.napi_close_escapable_handle_scope(C.napi_env(env), C.napi_escapable_handle_scope(raw))
	return errOf(env, Status(st))
}

// EscapeHandle promotes escapee out of an escapable scope. May only be
// called once per scope (napi_escape_called_twice otherwise).
func EscapeHandle(env Env, scope EscapableHandleScope, escapee Value) (Value, error) {
	raw, live := escapableScopeTokens.resolve(unsafe.Pointer(scope))
	if !live {
		return nil, staleScopeErr("escapable handle scope")
	}
	var result Value
	st := C.napi_escape_handle(C.napi_env(env), C.napi_escapable_handle_scope(raw), C.napi_value(escapee), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// ---- references -------------------------------------------------------------

// refTokens holds every reference handed out by CreateReference that has not
// been deleted yet.
//
// napi_delete_reference is a plain `delete` of the engine's Reference object:
// deleting the same handle twice is a double free that corrupts the V8 heap
// (measured: exit 0xc0000374), and any other operation on a deleted handle
// dereferences freed memory. The engine cannot tell a stale handle from a live
// one, so the binding keeps the live set and rejects stale handles instead of
// forwarding them.
//
// The token matters more here than anywhere else, because napi_ref is by far
// the most churned of these handles: in 28 of 64 create/delete rounds the
// allocator handed the freed address straight back, and every one of those
// stale deletes was accepted — each one destroying a *live* reference.
var refTokens = newTokenSet("reference")

// staleRefErr builds the error returned for a handle that is nil or already
// deleted.
func staleRefErr() error {
	return &StatusError{
		Code:    StatusInvalidArg,
		Message: "reference was already deleted, or was not created by CreateReference",
	}
}

// refEngine resolves a live reference token to the engine's pointer.
func refEngine(ref Ref) (unsafe.Pointer, error) {
	raw, live := refTokens.resolve(unsafe.Pointer(ref))
	if !live {
		return nil, staleRefErr()
	}
	return raw, nil
}

// CreateReference pins value with an initial reference count. Use 0 for a
// weak reference (the value may be collected; GetReferenceValue then returns
// nil) and >=1 for a strong one.
func CreateReference(env Env, value Value, initialRefcount uint32) (Ref, error) {
	var raw C.napi_ref
	st := C.napi_create_reference(C.napi_env(env), C.napi_value(value), C.uint32_t(initialRefcount), &raw)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	return Ref(refTokens.issue(unsafe.Pointer(raw))), nil
}

// DeleteReference releases a reference. Must be called on the JS thread, and
// exactly once — the engine frees the handle whether or not the call makes
// sense.
func DeleteReference(env Env, ref Ref) error {
	raw, live := refTokens.retire(unsafe.Pointer(ref))
	if !live {
		return staleRefErr()
	}
	st := C.napi_delete_reference(C.napi_env(env), C.napi_ref(raw))
	return errOf(env, Status(st))
}

// ReferenceRef strengthens a reference, returning the new count.
func ReferenceRef(env Env, ref Ref) (uint32, error) {
	raw, err := refEngine(ref)
	if err != nil {
		return 0, err
	}
	var count C.uint32_t
	st := C.napi_reference_ref(C.napi_env(env), C.napi_ref(raw), &count)
	return uint32(count), errOf(env, Status(st))
}

// ReferenceUnref weakens a reference, returning the new count.
func ReferenceUnref(env Env, ref Ref) (uint32, error) {
	raw, err := refEngine(ref)
	if err != nil {
		return 0, err
	}
	var count C.uint32_t
	st := C.napi_reference_unref(C.napi_env(env), C.napi_ref(raw), &count)
	return uint32(count), errOf(env, Status(st))
}

// GetReferenceValue materializes the referenced value, or nil when the
// reference is weak and the value was collected.
func GetReferenceValue(env Env, ref Ref) (Value, error) {
	raw, err := refEngine(ref)
	if err != nil {
		return nil, err
	}
	var result Value
	st := C.napi_get_reference_value(C.napi_env(env), C.napi_ref(raw), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// ---- instance data -------------------------------------------------------------

// instanceBoxes tracks which box currently backs each environment's instance
// data slot.
//
// Node-API documents a single instance-data slot per env and says a second
// napi_set_instance_data "fails", but modern Node silently deletes the old
// record instead ("Our contract so far has been to not finalize any old data
// there may be. So we simply delete it." — js_native_api_v8.cc). Deleting the
// record means the old finalizer never runs, so a blind replacement strands
// the previous box and its Go value for good. Holding our own handle lets the
// replacement release it; the dedicated finalizer clears the entry at env
// shutdown so a recycled env pointer can never free a box twice.
var instanceBoxes sync.Map // Env -> unsafe.Pointer

// SetInstanceData attaches a Go value to the environment for its lifetime.
// Installing a second value replaces the first; because the engine will not
// call the replaced value's finalizer, the binding releases it here instead.
// When the environment shuts down, onFinalize runs with the value.
func SetInstanceData(env Env, data any, onFinalize FinalizeFunc) error {
	box := boxHandle(finalizePayload{data: data, fn: onFinalize})

	st := C.napi_set_instance_data(
		C.napi_env(env), box,
		C.napi_finalize(C.napiGoInstanceDataFinalizeCallback), nil,
	)
	if st != C.napi_ok {
		freeBox(box)
		return errOf(env, Status(st))
	}
	if old, loaded := instanceBoxes.Swap(env, box); loaded {
		// The engine has already dropped the old record, so its finalizer is
		// guaranteed to never run: nobody else can reach this box.
		freeBox(old.(unsafe.Pointer))
	}
	return nil
}

// napiGoInstanceDataFinalizeCallback is the napi_finalize for the instance
// data slot: it releases the box and forgets it so the bookkeeping cannot
// outlive the environment.
//
//export napiGoInstanceDataFinalizeCallback
func napiGoInstanceDataFinalizeCallback(cEnv C.napi_env, cData, cHint unsafe.Pointer) {
	env := Env(cEnv)
	defer func() {
		if r := recover(); r != nil {
			// Finalizers run during GC; throwing is not possible. Log it.
			fmt.Fprintf(os.Stderr, "node-api: panic in instance data finalizer: %v\n", r)
		}
	}()
	instanceBoxes.Delete(env)
	if cData == nil {
		return
	}
	if p, ok := loadBox(cData).(finalizePayload); ok && p.fn != nil {
		p.fn(env, p.data)
	}
	freeBox(cData)
}

// GetInstanceData returns the value installed with SetInstanceData.
func GetInstanceData(env Env) (any, error) {
	var data unsafe.Pointer
	st := C.napi_get_instance_data(C.napi_env(env), &data)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if data == nil {
		return nil, nil
	}
	return unwrapPayload(data), nil
}

// ---- external memory ----------------------------------------------------------

// AdjustExternalMemory tells V8 about externally allocated memory so its GC
// pressure heuristics account for it. Returns the adjusted total.
func AdjustExternalMemory(env Env, changeInBytes int64) (int64, error) {
	var adjusted C.int64_t
	st := C.napi_adjust_external_memory(C.napi_env(env), C.int64_t(changeInBytes), (*C.int64_t)(unsafe.Pointer(&adjusted)))
	return int64(adjusted), errOf(env, Status(st))
}
