package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "fmt"

// Status is a napi_status error code.
type Status uint32

// All napi_status values (js_native_api_types.h).
const (
	StatusOK                       Status = C.napi_ok
	StatusInvalidArg               Status = C.napi_invalid_arg
	StatusObjectExpected           Status = C.napi_object_expected
	StatusStringExpected           Status = C.napi_string_expected
	StatusNameExpected             Status = C.napi_name_expected
	StatusFunctionExpected         Status = C.napi_function_expected
	StatusNumberExpected           Status = C.napi_number_expected
	StatusBooleanExpected          Status = C.napi_boolean_expected
	StatusArrayExpected            Status = C.napi_array_expected
	StatusGenericFailure           Status = C.napi_generic_failure
	StatusPendingException         Status = C.napi_pending_exception
	StatusCancelled                Status = C.napi_cancelled
	StatusEscapeCalledTwice        Status = C.napi_escape_called_twice
	StatusHandleScopeMismatch      Status = C.napi_handle_scope_mismatch
	StatusCallbackScopeMismatch    Status = C.napi_callback_scope_mismatch
	StatusQueueFull                Status = C.napi_queue_full
	StatusClosing                  Status = C.napi_closing
	StatusBigIntExpected           Status = C.napi_bigint_expected
	StatusDateExpected             Status = C.napi_date_expected
	StatusArrayBufferExpected      Status = C.napi_arraybuffer_expected
	StatusDetachableArrayBuffer    Status = C.napi_detachable_arraybuffer_expected
	StatusWouldDeadlock            Status = C.napi_would_deadlock
	StatusNoExternalBuffersAllowed Status = C.napi_no_external_buffers_allowed
	StatusCannotRunJS              Status = C.napi_cannot_run_js
)

var statusNames = map[Status]string{
	StatusOK:                       "napi_ok",
	StatusInvalidArg:               "napi_invalid_arg",
	StatusObjectExpected:           "napi_object_expected",
	StatusStringExpected:           "napi_string_expected",
	StatusNameExpected:             "napi_name_expected",
	StatusFunctionExpected:         "napi_function_expected",
	StatusNumberExpected:           "napi_number_expected",
	StatusBooleanExpected:          "napi_boolean_expected",
	StatusArrayExpected:            "napi_array_expected",
	StatusGenericFailure:           "napi_generic_failure",
	StatusPendingException:         "napi_pending_exception",
	StatusCancelled:                "napi_cancelled",
	StatusEscapeCalledTwice:        "napi_escape_called_twice",
	StatusHandleScopeMismatch:      "napi_handle_scope_mismatch",
	StatusCallbackScopeMismatch:    "napi_callback_scope_mismatch",
	StatusQueueFull:                "napi_queue_full",
	StatusClosing:                  "napi_closing",
	StatusBigIntExpected:           "napi_bigint_expected",
	StatusDateExpected:             "napi_date_expected",
	StatusArrayBufferExpected:      "napi_arraybuffer_expected",
	StatusDetachableArrayBuffer:    "napi_detachable_arraybuffer_expected",
	StatusWouldDeadlock:            "napi_would_deadlock",
	StatusNoExternalBuffersAllowed: "napi_no_external_buffers_allowed",
	StatusCannotRunJS:              "napi_cannot_run_js",
}

func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	return fmt.Sprintf("napi_status(%d)", uint32(s))
}

// StatusError wraps a non-OK napi_status in an error. Because the engine may
// also have a pending JS exception set at the same time, callers that receive
// a StatusError with Code == StatusPendingException should usually just
// return immediately and let the exception propagate.
type StatusError struct {
	Code    Status
	Message string
}

func (e *StatusError) Error() string {
	if e.Message == "" {
		return "node-api: " + e.Code.String()
	}
	return fmt.Sprintf("node-api: %s: %s", e.Code, e.Message)
}

// AsStatus extracts the napi_status from err if it is a StatusError.
func AsStatus(err error) (Status, bool) {
	if se, ok := err.(*StatusError); ok {
		return se.Code, true
	}
	return StatusOK, false
}

// errOf converts a napi_status into a Go error (nil for napi_ok). The
// extended error info of the engine is consulted for the human-readable
// message when cheap to do so.
func errOf(env Env, st Status) error {
	if st == StatusOK {
		return nil
	}
	msg := ""
	if env != nil {
		msg = lastErrorMessage(env)
	}
	return &StatusError{Code: st, Message: msg}
}

// lastErrorMessage reads the engine's extended error info. It never calls
// into JS and is safe even with a pending exception.
func lastErrorMessage(env Env) string {
	var info *C.napi_extended_error_info
	C.napi_get_last_error_info(C.napi_env(env), &info)
	if info == nil || info.error_message == nil {
		return ""
	}
	return C.GoString(info.error_message)
}
