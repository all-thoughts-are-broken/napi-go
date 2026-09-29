package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// This file covers ArrayBuffer, TypedArray, DataView (js_native_api.h) and
// node:Buffer (node_api.h).

// ArrayBufferInfo describes the raw storage of an ArrayBuffer.
type ArrayBufferInfo struct {
	Data []byte // valid while the ArrayBuffer is alive and not detached
}

// TypedArrayInfo describes a TypedArray view.
//
// The engine reports the pointer of the VIEW's first element, not of the
// backing store: Data is already offset by ByteOffset and covers exactly
// Length elements. Do not index it with ByteOffset.
type TypedArrayInfo struct {
	Type        TypedArrayType
	Length      uint64 // in elements, not bytes
	Data        []byte // the view itself: Length*elementSize bytes at the view's start
	ArrayBuffer Value
	ByteOffset  uint64 // byte offset of the view within the backing store
}

// DataViewInfo describes a DataView view. As with TypedArrayInfo, Data points
// at the view, not at the start of the backing store.
type DataViewInfo struct {
	ByteLength  uint64
	Data        []byte
	ArrayBuffer Value
	ByteOffset  uint64
}

// BufferInfo describes a node:Buffer.
type BufferInfo struct {
	Data []byte
}

// byteSlice wraps a raw C pointer and length as a Go []byte. The result is
// only valid while the backing JS object is alive; do not retain it.
//
// This alias is invisible to both collectors. Go does not know the storage
// belongs to V8, and V8 does not know a Go slice points into it, so a slice
// kept past the JS object's life reads freed memory — measured as an access
// violation (exit 0xC0000005) inside the Go frame that touched it. To hold a
// buffer across callbacks, keep the napi_ref (see CreateReference) and re-read
// the data from it each time.
func byteSlice(data unsafe.Pointer, length uint64) []byte {
	if data == nil || length == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(data), length)
}

// CreateArrayBuffer allocates a new ArrayBuffer of byteLength bytes. The
// returned []byte aliases its storage and is valid while the ArrayBuffer is
// alive — see byteSlice for what "while" means when two collectors are
// involved: V8 owns the storage and has no way to see the slice, so retaining
// it past the object's life is a use-after-free.
func CreateArrayBuffer(env Env, byteLength uint64) (value Value, data []byte, err error) {
	var p unsafe.Pointer
	st := C.napi_create_arraybuffer(C.napi_env(env), C.size_t(byteLength), &p, (*C.napi_value)(unsafe.Pointer(&value)))
	if st != C.napi_ok {
		return nil, nil, errOf(env, Status(st))
	}
	return value, byteSlice(p, byteLength), nil
}

// CreateExternalArrayBuffer creates an ArrayBuffer that wraps caller-owned
// memory without copying. finalData (if non-nil) is kept alive until the
// ArrayBuffer is collected and is then passed to onFinalize.
//
// If data points into the Go heap, finalData MUST keep that memory reachable —
// normally by passing the slice itself. Nothing else pins it: an
// unsafe.Pointer carries no Go reference, so the collector is free to recycle
// the storage while V8 still points at it, and Go may hand the pages back to
// the OS outright. Measured: wrapping a Go slice with a nil finalData and then
// running Go's collector makes the next read from JS an access violation (exit
// 0xC0000005). CreateExternalArrayBufferFromBytes cannot be misused this way;
// prefer it for Go-owned memory.
//
// finalize_data is forced by Node-API to be the raw external pointer, so the
// Go payload travels through the hint slot and a dedicated finalizer
// (napiGoExternalFinalizeCallback); it must never be treated as a box.
func CreateExternalArrayBuffer(env Env, data unsafe.Pointer, byteLength uint64, onFinalize FinalizeFunc, finalData any) (Value, error) {
	var hint unsafe.Pointer
	if finalData != nil || onFinalize != nil {
		hint = boxHandle(&externalFinalizeData{data: finalData, fn: onFinalize})
	}

	var result Value
	st := C.napi_create_external_arraybuffer(
		C.napi_env(env), data, C.size_t(byteLength),
		C.node_api_basic_finalize(C.napiGoExternalFinalizeCallback), hint,
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	if st != C.napi_ok {
		if hint != nil {
			freeBox(hint)
		}
		return nil, errOf(env, Status(st))
	}
	return result, nil
}

// CreateExternalArrayBufferFromBytes wraps b as an external ArrayBuffer
// without copying and pins b for as long as the ArrayBuffer is alive, so the
// two collectors cannot disagree about who owns it. onFinalize, when non-nil,
// receives b.
//
// This is the safe form of CreateExternalArrayBuffer for Go-owned memory: the
// slice travels in finalData by construction, which is exactly the step callers
// forget when they pass a bare pointer.
func CreateExternalArrayBufferFromBytes(env Env, b []byte, onFinalize FinalizeFunc) (Value, error) {
	if len(b) == 0 {
		return CreateExternalArrayBuffer(env, nil, 0, onFinalize, b)
	}
	return CreateExternalArrayBuffer(env, unsafe.Pointer(&b[0]), uint64(len(b)), onFinalize, b)
}

func GetArrayBufferInfo(env Env, arrayBuffer Value) (ArrayBufferInfo, error) {
	var p unsafe.Pointer
	var length C.size_t
	st := C.napi_get_arraybuffer_info(C.napi_env(env), C.napi_value(arrayBuffer), &p, &length)
	if st != C.napi_ok {
		return ArrayBufferInfo{}, errOf(env, Status(st))
	}
	return ArrayBufferInfo{Data: byteSlice(p, uint64(length))}, nil
}

func DetachArrayBuffer(env Env, arrayBuffer Value) error {
	st := C.napi_detach_arraybuffer(C.napi_env(env), C.napi_value(arrayBuffer))
	return errOf(env, Status(st))
}

func CreateTypedArray(env Env, typ TypedArrayType, length uint64, arrayBuffer Value, byteOffset uint64) (Value, error) {
	var result Value
	st := C.napi_create_typedarray(
		C.napi_env(env), C.napi_typedarray_type(typ), C.size_t(length),
		C.napi_value(arrayBuffer), C.size_t(byteOffset),
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

func GetTypedArrayInfo(env Env, typedArray Value) (TypedArrayInfo, error) {
	var (
		typ         C.napi_typedarray_type
		length      C.size_t
		p           unsafe.Pointer
		arrayBuffer Value
		byteOffset  C.size_t
	)
	st := C.napi_get_typedarray_info(
		C.napi_env(env), C.napi_value(typedArray),
		&typ, &length, &p, (*C.napi_value)(unsafe.Pointer(&arrayBuffer)), &byteOffset,
	)
	if st != C.napi_ok {
		return TypedArrayInfo{}, errOf(env, Status(st))
	}
	return TypedArrayInfo{
		Type:        TypedArrayType(typ),
		Length:      uint64(length),
		Data:        byteSlice(p, uint64(length)*elementSize(TypedArrayType(typ))),
		ArrayBuffer: arrayBuffer,
		ByteOffset:  uint64(byteOffset),
	}, nil
}

// elementSize returns the byte size of one element of a typed array kind.
func elementSize(t TypedArrayType) uint64 {
	switch t {
	case Int8Array, Uint8Array, Uint8ClampedArray:
		return 1
	case Int16Array, Uint16Array:
		return 2
	case Int32Array, Uint32Array, Float32Array:
		return 4
	case Float64Array, BigInt64Array, BigUint64Array:
		return 8
	default:
		return 1
	}
}

func CreateDataView(env Env, byteLength uint64, arrayBuffer Value, byteOffset uint64) (Value, error) {
	var result Value
	st := C.napi_create_dataview(
		C.napi_env(env), C.size_t(byteLength),
		C.napi_value(arrayBuffer), C.size_t(byteOffset),
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

func GetDataViewInfo(env Env, dataView Value) (DataViewInfo, error) {
	var (
		bytelength  C.size_t
		p           unsafe.Pointer
		arrayBuffer Value
		byteOffset  C.size_t
	)
	st := C.napi_get_dataview_info(
		C.napi_env(env), C.napi_value(dataView),
		&bytelength, &p, (*C.napi_value)(unsafe.Pointer(&arrayBuffer)), &byteOffset,
	)
	if st != C.napi_ok {
		return DataViewInfo{}, errOf(env, Status(st))
	}
	return DataViewInfo{
		ByteLength:  uint64(bytelength),
		Data:        byteSlice(p, uint64(bytelength)),
		ArrayBuffer: arrayBuffer,
		ByteOffset:  uint64(byteOffset),
	}, nil
}

// ---- node:Buffer ---------------------------------------------------------

// CreateBuffer allocates a new node:Buffer of length bytes. The returned
// []byte aliases its storage.
func CreateBuffer(env Env, length uint64) (value Value, data []byte, err error) {
	var p unsafe.Pointer
	st := C.napi_create_buffer(C.napi_env(env), C.size_t(length), &p, (*C.napi_value)(unsafe.Pointer(&value)))
	if st != C.napi_ok {
		return nil, nil, errOf(env, Status(st))
	}
	return value, byteSlice(p, length), nil
}

// CreateBufferCopy creates a node:Buffer that copies b.
func CreateBufferCopy(env Env, b []byte) (value Value, err error) {
	var p unsafe.Pointer
	var src unsafe.Pointer
	if len(b) > 0 {
		src = unsafe.Pointer(&b[0])
	}
	st := C.napi_create_buffer_copy(C.napi_env(env), C.size_t(len(b)), src, &p, (*C.napi_value)(unsafe.Pointer(&value)))
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	return value, nil
}

// CreateExternalBuffer creates a node:Buffer that wraps caller-owned memory
// without copying. finalData (if non-nil) is kept alive until the Buffer is
// collected and is then passed to onFinalize.
//
// The pinning rule is the same as for CreateExternalArrayBuffer, and so is the
// consequence of ignoring it (exit 0xC0000005): for Go-owned memory use
// CreateExternalBufferFromBytes. Some Node builds disallow external buffers
// entirely (napi_no_external_buffers_allowed); CreateBufferCopy is the portable
// alternative.
//
// Same hint-based finalizer scheme as CreateExternalArrayBuffer.
func CreateExternalBuffer(env Env, data unsafe.Pointer, byteLength uint64, onFinalize FinalizeFunc, finalData any) (Value, error) {
	var hint unsafe.Pointer
	if finalData != nil || onFinalize != nil {
		hint = boxHandle(&externalFinalizeData{data: finalData, fn: onFinalize})
	}

	var result Value
	st := C.napi_create_external_buffer(
		C.napi_env(env), C.size_t(byteLength), data,
		C.node_api_basic_finalize(C.napiGoExternalFinalizeCallback), hint,
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	if st != C.napi_ok {
		if hint != nil {
			freeBox(hint)
		}
		return nil, errOf(env, Status(st))
	}
	return result, nil
}

// CreateExternalBufferFromBytes wraps b as a node:Buffer without copying and
// pins b for as long as the Buffer is alive. It is to CreateExternalBuffer what
// CreateExternalArrayBufferFromBytes is to CreateExternalArrayBuffer: the safe
// form whenever the memory comes from the Go heap.
func CreateExternalBufferFromBytes(env Env, b []byte, onFinalize FinalizeFunc) (Value, error) {
	if len(b) == 0 {
		return CreateExternalBuffer(env, nil, 0, onFinalize, b)
	}
	return CreateExternalBuffer(env, unsafe.Pointer(&b[0]), uint64(len(b)), onFinalize, b)
}

func GetBufferInfo(env Env, buffer Value) (BufferInfo, error) {
	var p unsafe.Pointer
	var length C.size_t
	st := C.napi_get_buffer_info(C.napi_env(env), C.napi_value(buffer), &p, &length)
	if st != C.napi_ok {
		return BufferInfo{}, errOf(env, Status(st))
	}
	return BufferInfo{Data: byteSlice(p, uint64(length))}, nil
}
