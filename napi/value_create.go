package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// This file mirrors the value-creation half of js_native_api.h.

func GetUndefined(env Env) (Value, error) {
	var result Value
	st := C.napi_get_undefined(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetNull(env Env) (Value, error) {
	var result Value
	st := C.napi_get_null(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetGlobal(env Env) (Value, error) {
	var result Value
	st := C.napi_get_global(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetBoolean(env Env, value bool) (Value, error) {
	var result Value
	st := C.napi_get_boolean(C.napi_env(env), C.bool(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateObject(env Env) (Value, error) {
	var result Value
	st := C.napi_create_object(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateArray(env Env) (Value, error) {
	var result Value
	st := C.napi_create_array(C.napi_env(env), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateArrayWithLength(env Env, length int) (Value, error) {
	var result Value
	st := C.napi_create_array_with_length(C.napi_env(env), C.size_t(length), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateDouble(env Env, value float64) (Value, error) {
	var result Value
	st := C.napi_create_double(C.napi_env(env), C.double(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateInt32(env Env, value int32) (Value, error) {
	var result Value
	st := C.napi_create_int32(C.napi_env(env), C.int32_t(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateUint32(env Env, value uint32) (Value, error) {
	var result Value
	st := C.napi_create_uint32(C.napi_env(env), C.uint32_t(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func CreateInt64(env Env, value int64) (Value, error) {
	var result Value
	st := C.napi_create_int64(C.napi_env(env), C.int64_t(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateStringUtf8 creates a JS string from a UTF-8 Go string.
func CreateStringUtf8(env Env, str string) (Value, error) {
	cstr := C.CString(str)
	defer C.free(unsafe.Pointer(cstr))

	var result Value
	st := C.napi_create_string_utf8(C.napi_env(env), cstr, C.size_t(len(str)), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateStringLatin1 creates a JS string from ISO-8859-1 bytes. Each byte of
// str becomes one code unit.
func CreateStringLatin1(env Env, str string) (Value, error) {
	cstr := C.CString(str)
	defer C.free(unsafe.Pointer(cstr))

	var result Value
	st := C.napi_create_string_latin1(C.napi_env(env), cstr, C.size_t(len(str)), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateStringUtf16 creates a JS string from UTF-16 code units.
func CreateStringUtf16(env Env, units []uint16) (Value, error) {
	var p unsafe.Pointer
	if len(units) > 0 {
		p = unsafe.Pointer(&units[0])
	}
	var result Value
	st := C.napi_create_string_utf16(C.napi_env(env), (*C.char16_t)(p), C.size_t(len(units)), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateSymbol creates a symbol with the given description.
func CreateSymbol(env Env, description Value) (Value, error) {
	var result Value
	st := C.napi_create_symbol(C.napi_env(env), C.napi_value(description), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateDate creates a JS Date from a ms-since-epoch value.
func CreateDate(env Env, timeMs float64) (Value, error) {
	var result Value
	st := C.napi_create_date(C.napi_env(env), C.double(timeMs), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateBigIntInt64 wraps an int64 as a JS BigInt.
func CreateBigIntInt64(env Env, value int64) (Value, error) {
	var result Value
	st := C.napi_create_bigint_int64(C.napi_env(env), C.int64_t(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateBigIntUint64 wraps a uint64 as a JS BigInt.
func CreateBigIntUint64(env Env, value uint64) (Value, error) {
	var result Value
	st := C.napi_create_bigint_uint64(C.napi_env(env), C.uint64_t(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CreateBigIntWords creates a BigInt from sign-and-magnitude words
// (little-endian 64-bit limbs; sign_bit is negative for negative values).
func CreateBigIntWords(env Env, signBit int, words []uint64) (Value, error) {
	var p unsafe.Pointer
	if len(words) > 0 {
		p = unsafe.Pointer(&words[0])
	}
	var result Value
	st := C.napi_create_bigint_words(C.napi_env(env), C.int(signBit), C.size_t(len(words)), (*C.uint64_t)(p), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}
