package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// The Node-API C types are all opaque pointers. They are mirrored here as
// defined unsafe.Pointer types so they can be passed through cgo cheaply and
// stored in Go data structures.

// Env is an opaque handle to a JavaScript environment (a Node.js isolate /
// context pair). It is only valid on the JS thread unless stated otherwise.
type Env unsafe.Pointer

// Value is an opaque handle to a JavaScript value. A Value received from or
// created during a callback is only valid inside the callback's handle scope
// (i.e. for the duration of that callback) unless kept alive with a Ref.
//
// This rule is enforced by nothing, and breaking it does not fail loudly: the
// handle points at a slot in the engine's handle scope, the scope's storage is
// recycled as soon as the callback returns, and a stale Value then reads
// whatever now occupies that slot. Measured: saving a callback argument in the
// Go heap and reading it from the next callback returned the *new* callback's
// argument, with a status of napi_ok and no error anywhere. If a Value has to
// outlive its callback, wrap it with CreateReference and go through the Ref.
type Value unsafe.Pointer

// CallbackInfo is an opaque handle to the callback state of the current
// function invocation (arguments, this, new.target).
type CallbackInfo unsafe.Pointer

// Ref is a reference to a JavaScript value with a reference count, used to
// keep a value alive outside of a handle scope.
type Ref unsafe.Pointer

// HandleScope is a scope in which napi.Values may be created.
type HandleScope unsafe.Pointer

// EscapableHandleScope is a handle scope from which exactly one value may be
// escaped to the enclosing scope.
type EscapableHandleScope unsafe.Pointer

// Deferred is the deferred part of a promise, used to resolve or reject it.
type Deferred unsafe.Pointer

// AsyncContext is an opaque async context for napi_make_callback.
type AsyncContext unsafe.Pointer

// CallbackScope is an explicitly opened scope for calling into JS from
// places that are not ordinary callbacks.
type CallbackScope unsafe.Pointer

// AsyncCleanupHookHandle is a handle for removing an async cleanup hook.
type AsyncCleanupHookHandle unsafe.Pointer

// Callback is the Go signature of every JS-callable function:
// method, constructor, getter and setter all share it.
type Callback func(env Env, info CallbackInfo) Value

// FinalizeFunc runs when the JS object the callback was attached to is
// garbage collected. It runs on the JS thread during GC; only the "basic"
// Node-API functions (napi_unref_threadsafe_function, external memory
// adjustments, ...) may be called there. data is the Go value that was
// attached to the object.
type FinalizeFunc func(env Env, data any)

// CleanupHook is invoked when the Node.js environment shuts down.
type CleanupHook func()

// AsyncCleanupHookFunc is invoked on shutdown, possibly from another thread.
//
// It MUST call RemoveAsyncCleanupHook(handle), either directly or once any
// async work it started has finished. Node-API treats the hook as an
// asynchronous handshake: the environment cannot finish tearing down until the
// handle is removed, because only the handle's destructor releases the
// environment's pending-request counter. A hook that never removes its handle
// hangs process shutdown.
type AsyncCleanupHookFunc func(handle AsyncCleanupHookHandle)

// ThreadsafeFunctionCallMode controls whether
// ThreadsafeFunction.Call blocks when the queue is full.
type ThreadsafeFunctionCallMode uint32

const (
	// NonBlocking returns napi_queue_full as an error if the queue is full.
	NonBlocking ThreadsafeFunctionCallMode = iota // napi_tsfn_nonblocking
	// Blocking blocks the calling goroutine until there is room.
	Blocking // napi_tsfn_blocking
)

// ThreadsafeFunctionReleaseMode controls what happens when the last thread
// releases a threadsafe function.
type ThreadsafeFunctionReleaseMode uint32

const (
	// Release lets the threadsafe function close gracefully
	// (napi_tsfn_release).
	Release ThreadsafeFunctionReleaseMode = iota
	// Abort closes it immediately, emptying the queue with napi_closing
	// errors (napi_tsfn_abort).
	Abort
)

// PropertyAttributes mirror napi_property_attributes.
type PropertyAttributes uint32

const (
	Default           PropertyAttributes = C.napi_default
	Writable          PropertyAttributes = C.napi_writable
	Enumerable        PropertyAttributes = C.napi_enumerable
	Configurable      PropertyAttributes = C.napi_configurable
	Static            PropertyAttributes = C.napi_static
	DefaultMethod     PropertyAttributes = C.napi_default_method
	DefaultJSProperty PropertyAttributes = C.napi_default_jsproperty
)

// KeyCollectionMode mirrors napi_key_collection_mode.
type KeyCollectionMode uint32

const (
	IncludePrototypes KeyCollectionMode = C.napi_key_include_prototypes
	OwnOnly           KeyCollectionMode = C.napi_key_own_only
)

// KeyFilter mirrors napi_key_filter.
type KeyFilter uint32

const (
	KeyAllProperties KeyFilter = C.napi_key_all_properties
	KeyWritable      KeyFilter = C.napi_key_writable
	KeyEnumerable    KeyFilter = C.napi_key_enumerable
	KeyConfigurable  KeyFilter = C.napi_key_configurable
	KeySkipStrings   KeyFilter = C.napi_key_skip_strings
	KeySkipSymbols   KeyFilter = C.napi_key_skip_symbols
)

// KeyConversion mirrors napi_key_conversion.
type KeyConversion uint32

const (
	KeepNumbers      KeyConversion = C.napi_key_keep_numbers
	NumbersToStrings KeyConversion = C.napi_key_numbers_to_strings
)

// TypedArrayType mirrors napi_typedarray_type.
type TypedArrayType uint32

const (
	Int8Array         TypedArrayType = C.napi_int8_array
	Uint8Array        TypedArrayType = C.napi_uint8_array
	Uint8ClampedArray TypedArrayType = C.napi_uint8_clamped_array
	Int16Array        TypedArrayType = C.napi_int16_array
	Uint16Array       TypedArrayType = C.napi_uint16_array
	Int32Array        TypedArrayType = C.napi_int32_array
	Uint32Array       TypedArrayType = C.napi_uint32_array
	Float32Array      TypedArrayType = C.napi_float32_array
	Float64Array      TypedArrayType = C.napi_float64_array
	BigInt64Array     TypedArrayType = C.napi_bigint64_array
	BigUint64Array    TypedArrayType = C.napi_biguint64_array
)

// ValueType mirrors napi_valuetype (the result of the JS typeof operator,
// with null separated from object).
type ValueType uint32

const (
	TypeUndefined ValueType = C.napi_undefined
	TypeNull      ValueType = C.napi_null
	TypeBoolean   ValueType = C.napi_boolean
	TypeNumber    ValueType = C.napi_number
	TypeString    ValueType = C.napi_string
	TypeSymbol    ValueType = C.napi_symbol
	TypeObject    ValueType = C.napi_object
	TypeFunction  ValueType = C.napi_function
	TypeExternal  ValueType = C.napi_external
	TypeBigInt    ValueType = C.napi_bigint
)

// TypeTag is a 128-bit tag used to brand native objects
// (napi_type_tag_object / napi_check_object_type_tag).
type TypeTag struct {
	Lower uint64
	Upper uint64
}

// NodeVersion describes the running Node.js version (napi_get_node_version).
type NodeVersion struct {
	Major   uint32
	Minor   uint32
	Patch   uint32
	Release string
}

// cbData is what gets stored behind every JS-facing callback. Method,
// getter and setter share one box so that a single PropertyDescriptor only
// needs one registry entry.
type cbData struct {
	cb  Callback // method / constructor / plain function
	get Callback // getter
	set Callback // setter
}

// CallbackData exposes per-callback data to advanced users (napi.Data of a
// descriptor is delivered here in addition to being used internally).
type CallbackData struct {
	Data any
}

// PropertyDescriptor describes one property for DefineProperties or
// DefineClass. Exactly one of Name/NameValue identifies the property; the
// value forms (Value, Method, Getter/Setter) are mutually exclusive.
type PropertyDescriptor struct {
	// Name is the UTF-8 name of a string-keyed property.
	Name string
	// NameValue is a Value (string or symbol) keying the property.
	NameValue Value

	// Value makes the property a plain data property.
	Value Value

	// Method makes the property a function-valued property.
	Method Callback

	// Getter and/or Setter make the property an accessor property.
	Getter Callback
	Setter Callback

	// Attributes control writability etc. Static is only meaningful for
	// DefineClass.
	Attributes PropertyAttributes
}

// CbInfoResult carries the pieces of a callback invocation.
type CbInfoResult struct {
	Args []Value
	This Value
	// Data is the raw void* passed at function creation time. Users of the
	// high-level API do not need it; it is how CreateFunction plumbs the Go
	// callback through.
	Data unsafe.Pointer
}
