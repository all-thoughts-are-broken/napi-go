package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// This file covers functions: creation, callback info, invocation and
// classes.

// CreateFunction creates a JS function backed by a Go callback. The Go
// callback and its registry entry are released when the JS function object
// is garbage collected.
func CreateFunction(env Env, name string, cb Callback) (Value, error) {
	cstr := C.CString(name)
	defer C.free(unsafe.Pointer(cstr))

	box := boxHandle(cbData{cb: cb})

	var result Value
	st := C.napi_create_function(
		C.napi_env(env), cstr, C.size_t(len(name)),
		C.napi_callback(C.napiGoExecuteCallback), box,
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	if st != C.napi_ok {
		freeBox(box)
		return nil, errOf(env, Status(st))
	}

	// Release the box when the function object dies.
	st = C.napi_add_finalizer(
		C.napi_env(env), C.napi_value(result), box,
		C.node_api_basic_finalize(C.napiGoFinalizeCallback), nil, nil,
	)
	if st != C.napi_ok {
		// Older engines without napi_add_finalizer would leak here; with
		// NAPI_VERSION 8 it is always available, so failure is unexpected.
		freeBox(box)
		return nil, errOf(env, Status(st))
	}
	return result, nil
}

// GetCbInfo extracts the arguments, this-value and callback data of the
// current invocation.
func GetCbInfo(env Env, info CallbackInfo) (CbInfoResult, error) {
	argc := C.size_t(0)
	st := C.napi_get_cb_info(
		C.napi_env(env), C.napi_callback_info(info),
		&argc, nil, nil, nil,
	)
	if st != C.napi_ok {
		return CbInfoResult{}, errOf(env, Status(st))
	}

	argv := make([]Value, argc)
	var argvPtr *C.napi_value
	if argc > 0 {
		argvPtr = (*C.napi_value)(unsafe.Pointer(&argv[0]))
	}

	var this Value
	var data unsafe.Pointer
	st = C.napi_get_cb_info(
		C.napi_env(env), C.napi_callback_info(info),
		&argc, argvPtr, (*C.napi_value)(unsafe.Pointer(&this)), &data,
	)
	if st != C.napi_ok {
		return CbInfoResult{}, errOf(env, Status(st))
	}
	return CbInfoResult{Args: argv, This: this, Data: data}, nil
}

// GetNewTarget returns the new.target of a constructor invocation, or nil
// when the function was called without `new`.
func GetNewTarget(env Env, info CallbackInfo) (Value, error) {
	var result Value
	st := C.napi_get_new_target(C.napi_env(env), C.napi_callback_info(info), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CallFunction invokes funcValue as `recv.funcValue(args...)`.
func CallFunction(env Env, recv, funcValue Value, args ...Value) (Value, error) {
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
	st := C.napi_call_function(
		C.napi_env(env), C.napi_value(recv), C.napi_value(funcValue),
		argc, argvPtr, (*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

// NewInstance constructs an object with `new constructor(args...)`.
func NewInstance(env Env, constructor Value, args ...Value) (Value, error) {
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
	st := C.napi_new_instance(
		C.napi_env(env), C.napi_value(constructor),
		argc, argvPtr, (*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

// ClassDescriptor describes a JS class backed by a Go constructor.
type ClassDescriptor struct {
	// Name of the class (its .name and constructor display name).
	Name string
	// Constructor runs for `new Name(...)`. Use GetNewTarget to tell
	// constructor calls from plain calls, and Wrap to attach Go state.
	Constructor Callback
	// Properties are installed on the prototype (instance methods, accessors)
	// and, when flagged Static, on the constructor itself.
	Properties []PropertyDescriptor
}

// DefineClass creates a JS class from a Go constructor and property list.
//
// The returned class, its prototype and every method on it are permanently
// reachable: V8 caches instantiations of cacheable FunctionTemplates in the
// context (TemplateInfo::CacheTemplateInstantiation) and never drops that
// cache, so the objects built by this call are NOT garbage collected while
// the Node environment lives. The practical consequences:
//
//   - the boxes created here stay allocated for the environment's lifetime,
//     and any finalizer attached to the class never runs;
//   - define classes at module initialisation, never per call or per loop
//     iteration. (Verified against raw Node-API with no binding involved:
//     a bare napi_define_class result survives every forced collection.)
func DefineClass(env Env, d ClassDescriptor) (Value, error) {
	if d.Constructor == nil {
		// napi_define_class requires a constructor; without one the class
		// would be a function whose callback data holds a nil Go func, i.e.
		// a panic on every `new`.
		return nil, &StatusError{
			Code:    StatusInvalidArg,
			Message: "class descriptor has no constructor",
		}
	}

	cstr := C.CString(d.Name)
	defer C.free(unsafe.Pointer(cstr))

	ctorBox := boxHandle(cbData{cb: d.Constructor})

	built, err := buildProps(d.Properties)
	if err != nil {
		freeBox(ctorBox)
		return nil, err
	}

	var result Value
	st := C.napi_define_class(
		C.napi_env(env), cstr, C.size_t(len(d.Name)),
		C.napi_callback(C.napiGoExecuteCallback), ctorBox,
		C.size_t(len(d.Properties)), built.cProps,
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	if st != C.napi_ok {
		built.release()
		freeBox(ctorBox)
		return nil, errOf(env, Status(st))
	}
	// The C array itself can go away; the boxes live with the class object.
	if built.cProps != nil {
		C.go_props_free(built.cProps)
		built.cProps = nil
	}

	// Tie every box (constructor + per-property) to the class function.
	all := append([]unsafe.Pointer{ctorBox}, built.boxes...)
	built.boxes = all
	built.attachFinalizer(env, result)
	return result, nil
}

// RunScript evaluates JS source in the current context. It is a sharp tool:
// the script can touch anything the embedding has exposed.
func RunScript(env Env, script string) (Value, error) {
	src, err := CreateStringUtf8(env, script)
	if err != nil {
		return nil, err
	}
	var result Value
	st := C.napi_run_script(C.napi_env(env), C.napi_value(src), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}
