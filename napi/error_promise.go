package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// This file covers error handling (throwing and inspecting pending
// exceptions) and promises.

// ---- throwing -------------------------------------------------------------

// Throw makes err the pending exception of env.
func Throw(env Env, err Value) error {
	st := C.napi_throw(C.napi_env(env), C.napi_value(err))
	return errOf(env, Status(st))
}

// ThrowError throws an Error with an optional code. Note that this only sets
// a pending exception; return from the callback right after.
func ThrowError(env Env, code, message string) error {
	var ccode, cmsg *C.char
	if code != "" {
		ccode = C.CString(code)
		defer C.free(unsafe.Pointer(ccode))
	}
	cmsg = C.CString(message)
	defer C.free(unsafe.Pointer(cmsg))

	st := C.napi_throw_error(C.napi_env(env), ccode, cmsg)
	return errOf(env, Status(st))
}

// ThrowTypeError throws a TypeError.
func ThrowTypeError(env Env, code, message string) error {
	var ccode, cmsg *C.char
	if code != "" {
		ccode = C.CString(code)
		defer C.free(unsafe.Pointer(ccode))
	}
	cmsg = C.CString(message)
	defer C.free(unsafe.Pointer(cmsg))

	st := C.napi_throw_type_error(C.napi_env(env), ccode, cmsg)
	return errOf(env, Status(st))
}

// ThrowRangeError throws a RangeError.
func ThrowRangeError(env Env, code, message string) error {
	var ccode, cmsg *C.char
	if code != "" {
		ccode = C.CString(code)
		defer C.free(unsafe.Pointer(ccode))
	}
	cmsg = C.CString(message)
	defer C.free(unsafe.Pointer(cmsg))

	st := C.napi_throw_range_error(C.napi_env(env), ccode, cmsg)
	return errOf(env, Status(st))
}

// ---- creating error values ---------------------------------------------------

func CreateError(env Env, code, message Value) (Value, error) {
	var result Value
	st := C.napi_create_error(C.napi_env(env), C.napi_value(code), C.napi_value(message), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateTypeError(env Env, code, message Value) (Value, error) {
	var result Value
	st := C.napi_create_type_error(C.napi_env(env), C.napi_value(code), C.napi_value(message), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateRangeError(env Env, code, message Value) (Value, error) {
	var result Value
	st := C.napi_create_range_error(C.napi_env(env), C.napi_value(code), C.napi_value(message), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// ---- pending exceptions --------------------------------------------------------

// IsExceptionPending reports whether an exception is pending. Any Node-API
// call may return napi_pending_exception; in that case the only sensible
// action is to return and let the engine handle it.
func IsExceptionPending(env Env) (bool, error) {
	var result bool
	st := C.napi_is_exception_pending(C.napi_env(env), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// GetAndClearLastException retrieves and clears the pending exception.
func GetAndClearLastException(env Env) (Value, error) {
	var result Value
	st := C.napi_get_and_clear_last_exception(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// FatalException triggers an 'uncaughtException' in Node.js. Use it when a
// Go error cannot reasonably be reported back to the call site.
func FatalException(env Env, err Value) error {
	st := C.napi_fatal_exception(C.napi_env(env), C.napi_value(err))
	return errOf(env, Status(st))
}

// FatalError aborts the process unconditionally (napi_fatal_error). There is
// no coming back from this call.
func FatalError(location, message string) {
	var cloc, cmsg *C.char
	if location != "" {
		cloc = C.CString(location)
		defer C.free(unsafe.Pointer(cloc))
	}
	cmsg = C.CString(message)
	defer C.free(unsafe.Pointer(cmsg))

	C.napi_fatal_error(cloc, C.size_t(len(location)), cmsg, C.size_t(len(message)))
}

// GetExtendedErrorInfo returns the engine's error info for the last failed
// Node-API call. Debugging aid; the info is invalidated by the next call.
func GetExtendedErrorInfo(env Env) (message string, code Status, err error) {
	var info *C.napi_extended_error_info
	st := C.napi_get_last_error_info(C.napi_env(env), &info)
	if st != C.napi_ok {
		return "", Status(st), errOf(env, Status(st))
	}
	if info == nil {
		return "", StatusOK, nil
	}
	if info.error_message != nil {
		message = C.GoString(info.error_message)
	}
	return message, Status(info.error_code), nil
}

// ---- promises -------------------------------------------------------------------

// deferredTokens holds every deferred handed out by CreatePromise that has not
// been settled yet.
//
// napi_resolve_deferred / napi_reject_deferred free the deferred record
// unconditionally — ConcludeDeferred does `delete deferred_ref` before it even
// looks at the resolution result — so settling the same deferred twice is a
// use-after-free that corrupts the heap. The engine cannot tell the second
// call apart from the first, so the binding keeps the live set and rejects it.
//
// It keys on a token rather than the engine pointer because napi_deferred is
// another recycled address: in 1 of 64 create/settle rounds the allocator
// handed the freed address back, and the stale settle was accepted — resolving
// the *live* promise through a handle to a dead one. That failure is quieter
// than a crash: the wrong promise settles and the right one stays pending.
var deferredTokens = newTokenSet("deferred")

// CreatePromise creates a promise and its deferred counterpart.
func CreatePromise(env Env) (promise Value, deferred Deferred, err error) {
	var d C.napi_deferred
	st := C.napi_create_promise(C.napi_env(env), &d, (*C.napi_value)(unsafe.Pointer(&promise)))
	if st != C.napi_ok {
		return nil, nil, errOf(env, Status(st))
	}
	return promise, Deferred(deferredTokens.issue(unsafe.Pointer(d))), nil
}

// ResolveDeferred resolves a promise created with CreatePromise. The
// deferred must not be used again afterwards (a second settle is a hard crash
// in the engine; here it comes back as an error).
func ResolveDeferred(env Env, deferred Deferred, resolution Value) error {
	return concludeDeferred(env, deferred, resolution, true)
}

// RejectDeferred rejects a promise created with CreatePromise.
func RejectDeferred(env Env, deferred Deferred, rejection Value) error {
	return concludeDeferred(env, deferred, rejection, false)
}

// concludeDeferred settles a deferred exactly once.
func concludeDeferred(env Env, deferred Deferred, value Value, resolve bool) error {
	what := "rejected"
	if resolve {
		what = "resolved"
	}
	if unsafe.Pointer(deferred) == nil {
		return &StatusError{Code: StatusInvalidArg, Message: "nil deferred"}
	}
	if value == nil {
		// The engine checks the result argument first and bails out before
		// freeing the deferred, so a nil value would leave the promise
		// pending forever. Reject it without touching the engine.
		return &StatusError{Code: StatusInvalidArg, Message: "deferred resolution value must not be nil"}
	}
	raw, live := deferredTokens.retire(unsafe.Pointer(deferred))
	if !live {
		return &StatusError{
			Code:    StatusInvalidArg,
			Message: "deferred already " + what + ", or not created by CreatePromise",
		}
	}
	// From here on the engine owns the record and will free it even on
	// failure, so the entry must already be gone.
	var st C.napi_status
	if resolve {
		st = C.napi_resolve_deferred(C.napi_env(env), C.napi_deferred(raw), C.napi_value(value))
	} else {
		st = C.napi_reject_deferred(C.napi_env(env), C.napi_deferred(raw), C.napi_value(value))
	}
	return errOf(env, Status(st))
}
