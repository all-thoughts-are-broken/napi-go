package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// This file mirrors the remaining node_api.h surface: version info, async
// contexts, callback scopes, the libuv loop and cleanup hooks.

// GetVersion returns the highest Node-API version the runtime supports.
func GetVersion(env Env) (uint32, error) {
	var result C.uint32_t
	st := C.napi_get_version(C.napi_env(env), &result)
	return uint32(result), errOf(env, Status(st))
}

// GetNodeVersion returns the running Node.js version.
func GetNodeVersion(env Env) (NodeVersion, error) {
	var v *C.napi_node_version
	st := C.napi_get_node_version(C.napi_env(env), &v)
	if st != C.napi_ok {
		return NodeVersion{}, errOf(env, Status(st))
	}
	if v == nil {
		return NodeVersion{}, nil
	}
	nv := NodeVersion{
		Major: uint32(v.major),
		Minor: uint32(v.minor),
		Patch: uint32(v.patch),
	}
	if v.release != nil {
		nv.Release = C.GoString(v.release)
	}
	return nv, nil
}

// GetUVEventLoop returns the libuv loop of the environment. The pointer is
// opaque here; combine with a Go libuv binding if you need it.
func GetUVEventLoop(env Env) (unsafe.Pointer, error) {
	var loop unsafe.Pointer
	st := C.go_get_uv_loop(C.napi_env(env), &loop)
	return loop, errOf(env, Status(st))
}

// ---- async context / make_callback ------------------------------------------

// asyncContextTokens is the liveness set for async contexts created by
// AsyncInit. The engine's napi_async_destroy is a bare `delete node_async_context`
// with no validity check, so destroying the same context twice — or handing it a
// pointer this package never produced — corrupts the heap (measured:
// 0xC0000374 double destroy, 0xC0000028 foreign pointer).
//
// Tokens, not engine pointers: in 1 of 64 create/destroy rounds the allocator
// handed the freed address straight back to the next context, and the stale
// destroy was accepted — taking down a live context.
var asyncContextTokens = newTokenSet("async context")

// asyncContextRaw resolves a live context token for the engine calls that take
// one. MakeCallback and OpenCallbackScope go through here too, which is why
// they now reject a destroyed context instead of attributing work to whatever
// the address happens to mean by then.
func asyncContextRaw(ctx AsyncContext) (unsafe.Pointer, error) {
	raw, live := asyncContextTokens.resolve(unsafe.Pointer(ctx))
	if !live {
		return nil, &StatusError{
			Code:    StatusInvalidArg,
			Message: "async context is not live (already destroyed, or not created by AsyncInit)",
		}
	}
	return raw, nil
}

// AsyncInit creates an async context for MakeCallback. Pass the object the
// async operation is attributed to (or nil) and a resource name.
func AsyncInit(env Env, asyncResource Value, resourceName string) (AsyncContext, error) {
	name, err := CreateStringUtf8(env, resourceName)
	if err != nil {
		return nil, err
	}
	var raw C.napi_async_context
	st := C.napi_async_init(C.napi_env(env), C.napi_value(asyncResource), C.napi_value(name), &raw)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	return AsyncContext(asyncContextTokens.issue(unsafe.Pointer(raw))), nil
}

// AsyncDestroy releases an async context. The context must come from
// AsyncInit and must not have been destroyed already; a second call reports
// napi_invalid_arg instead of corrupting the heap.
func AsyncDestroy(env Env, ctx AsyncContext) error {
	raw, live := asyncContextTokens.retire(unsafe.Pointer(ctx))
	if !live {
		return &StatusError{
			Code:    StatusInvalidArg,
			Message: "async context is not live (already destroyed, or not created by AsyncInit)",
		}
	}
	st := C.napi_async_destroy(C.napi_env(env), C.napi_async_context(raw))
	return errOf(env, Status(st))
}

// MakeCallback invokes funcValue like a JS callback would, with proper
// async-context tracking (async_hooks, uncaughtException handling).
func MakeCallback(env Env, ctx AsyncContext, recv, funcValue Value, args ...Value) (Value, error) {
	rawCtx, err := asyncContextRaw(ctx)
	if err != nil {
		return nil, err
	}
	argc := C.size_t(len(args))
	var argvPtr *C.napi_value
	if len(args) > 0 {
		cargs := make([]C.napi_value, len(args))
		for i, a := range args {
			cargs[i] = C.napi_value(a)
		}
		argvPtr = &cargs[0]
	}

	var result Value
	st := C.napi_make_callback(
		C.napi_env(env), C.napi_async_context(rawCtx),
		C.napi_value(recv), C.napi_value(funcValue),
		argc, argvPtr, (*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

// callbackScopeTokens is the liveness set for callback scopes.
//
// napi_close_callback_scope is a bare `delete` of the engine's CallbackScope,
// and its only guard is the environment's open_callback_scopes counter. That
// counter catches the plain double close (it answers napi_callback_scope_mismatch
// once the count is back to zero) but not the case the counter was never going
// to catch: with two scopes open, closing the *first* one twice passes the
// check both times and deletes the same object twice. It is also the one engine
// handle this package handed out with no bookkeeping at all.
var callbackScopeTokens = newTokenSet("callback scope")

// OpenCallbackScope opens a scope required around MakeCallback when called
// from a place that is not an ordinary callback (e.g. a TSFN dispatch).
func OpenCallbackScope(env Env, resourceObject Value, ctx AsyncContext) (CallbackScope, error) {
	rawCtx, err := asyncContextRaw(ctx)
	if err != nil {
		return nil, err
	}
	var raw C.napi_callback_scope
	st := C.napi_open_callback_scope(C.napi_env(env), C.napi_value(resourceObject), C.napi_async_context(rawCtx), &raw)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if raw == nil {
		return nil, nil
	}
	return CallbackScope(callbackScopeTokens.issue(unsafe.Pointer(raw))), nil
}

// CloseCallbackScope closes a scope opened with OpenCallbackScope. Closing it
// twice reports napi_callback_scope_mismatch — the engine's own answer for a
// scope it does not have open — rather than reaching the `delete`.
func CloseCallbackScope(env Env, scope CallbackScope) error {
	raw, live := callbackScopeTokens.retire(unsafe.Pointer(scope))
	if !live {
		return &StatusError{
			Code:    StatusCallbackScopeMismatch,
			Message: "callback scope is not open (already closed, or not created by OpenCallbackScope)",
		}
	}
	st := C.napi_close_callback_scope(C.napi_env(env), C.napi_callback_scope(raw))
	return errOf(env, Status(st))
}

// ---- cleanup hooks ------------------------------------------------------------

// CleanupHookToken identifies a removable cleanup hook.
type CleanupHookToken struct {
	env Env
	box unsafe.Pointer
}

// AddEnvCleanupHook registers fn to run when the environment shuts down.
// The hook cannot be removed afterwards.
func AddEnvCleanupHook(env Env, fn CleanupHook) error {
	box := boxHandle(fn)
	st := C.napi_add_env_cleanup_hook(C.napi_env(env), C.napi_cleanup_hook(C.napiGoCleanupHook), box)
	if st != C.napi_ok {
		freeBox(box)
		return errOf(env, Status(st))
	}
	return nil
}

// AddEnvCleanupHookRemovable registers fn and returns a token that allows
// removing the hook before it fires.
func AddEnvCleanupHookRemovable(env Env, fn CleanupHook) (*CleanupHookToken, error) {
	box := boxHandle(fn)
	st := C.napi_add_env_cleanup_hook(C.napi_env(env), C.napi_cleanup_hook(C.napiGoCleanupHook), box)
	if st != C.napi_ok {
		freeBox(box)
		return nil, errOf(env, Status(st))
	}
	return &CleanupHookToken{env: env, box: box}, nil
}

// RemoveEnvCleanupHook removes a hook registered with
// AddEnvCleanupHookRemovable before it fired.
func RemoveEnvCleanupHook(tok *CleanupHookToken) error {
	if tok == nil || tok.box == nil {
		return nil
	}
	st := C.napi_remove_env_cleanup_hook(C.napi_env(tok.env), C.napi_cleanup_hook(C.napiGoCleanupHook), tok.box)
	if st != C.napi_ok {
		return errOf(tok.env, Status(st))
	}
	freeBox(tok.box)
	tok.box = nil
	return nil
}

// asyncCleanupBoxes maps a live async cleanup hook handle to the box holding
// its Go closure. Node never invokes the callback of a hook that was removed,
// so RemoveAsyncCleanupHook must free the box itself — otherwise every removed
// hook would leak a box plus its cgo handle. Both the remover and the
// trampoline claim the mapping with LoadAndDelete, so whichever runs first
// frees exactly once.
var asyncCleanupBoxes sync.Map // AsyncCleanupHookHandle -> unsafe.Pointer

// asyncCleanupLive tracks handles whose engine-side object has not been
// deleted yet. It is deliberately a *different* set from asyncCleanupBoxes:
// the Go closure is released as soon as the hook runs, but the engine handle
// outlives that — napi_remove_async_cleanup_hook is a bare `delete handle`
// whose validity cannot be probed, so a second call is a double free
// (measured: exit 0xC0000028). The engine also requires the addon to call
// napi_remove_async_cleanup_hook after the hook fires: the handle's destructor
// is what runs AsyncCleanupHookInfo::done_cb_ and releases the environment's
// pending-request counter, so skipping it hangs environment teardown.
var asyncCleanupLive sync.Map // AsyncCleanupHookHandle -> unsafe.Pointer

func takeAsyncCleanupBox(h AsyncCleanupHookHandle) unsafe.Pointer {
	if box, ok := asyncCleanupBoxes.LoadAndDelete(h); ok {
		return box.(unsafe.Pointer)
	}
	return nil
}

// AddAsyncCleanupHook registers fn to run on environment shutdown; unlike
// env cleanup hooks it may run on another thread and supports asynchronous
// completion through the handle.
//
// fn *must* call RemoveAsyncCleanupHook(handle) once it is done. The engine
// treats the hook as an asynchronous handshake: teardown blocks until the
// handle is removed, and only the handle's destructor releases the wait.
//
// The box is registered *after* the engine call returns. That is sound, not a
// race: napi_add_async_cleanup_hook only inserts the hook into the
// environment's CleanupQueue (CleanupQueue::Add never invokes anything
// synchronously), the queue is drained during environment teardown, and both
// registration and that drain run on the environment's JS thread — so the
// store is ordered before any possible callback.
func AddAsyncCleanupHook(env Env, fn AsyncCleanupHookFunc) (AsyncCleanupHookHandle, error) {
	box := boxHandle(fn)
	var handle C.napi_async_cleanup_hook_handle
	st := C.napi_add_async_cleanup_hook(C.napi_env(env), C.napi_async_cleanup_hook(C.napiGoAsyncCleanupHook), box, &handle)
	if st != C.napi_ok {
		freeBox(box)
		return nil, errOf(env, Status(st))
	}
	h := AsyncCleanupHookHandle(handle)
	asyncCleanupBoxes.Store(h, box)
	asyncCleanupLive.Store(h, box)
	return h, nil
}

// RemoveAsyncCleanupHook completes (or cancels) a hook registered with
// AddAsyncCleanupHook, releasing its Go closure and the engine handle.
//
// It is a no-op error — not a crash — to call this twice, or to call it for a
// handle this package never issued. The engine implements the underlying call
// as `delete handle` with no validity check, so liveness is enforced here.
func RemoveAsyncCleanupHook(handle AsyncCleanupHookHandle) error {
	if handle == nil {
		return &StatusError{Code: StatusInvalidArg}
	}
	if _, live := asyncCleanupLive.LoadAndDelete(handle); !live {
		return &StatusError{
			Code:    StatusInvalidArg,
			Message: "async cleanup hook handle is not live (already removed, or not issued by AddAsyncCleanupHook)",
		}
	}
	if box := takeAsyncCleanupBox(handle); box != nil {
		freeBox(box)
	}
	st := C.napi_remove_async_cleanup_hook(C.napi_async_cleanup_hook_handle(handle))
	return errOf(nil, Status(st))
}
