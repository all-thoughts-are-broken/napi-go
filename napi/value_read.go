package napi

/*
#include "napi_preamble.h"
*/
import "C"

import "unsafe"

// This file mirrors the value-inspection half of js_native_api.h:
// typeof, predicates, native value extraction and coercion.

// Typeof returns the JS typeof-style classification of a value.
func Typeof(env Env, value Value) (ValueType, error) {
	var result ValueType
	st := C.napi_typeof(C.napi_env(env), C.napi_value(value), (*C.napi_valuetype)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetValueDouble(env Env, value Value) (float64, error) {
	var result float64
	st := C.napi_get_value_double(C.napi_env(env), C.napi_value(value), (*C.double)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetValueInt32(env Env, value Value) (int32, error) {
	var result int32
	st := C.napi_get_value_int32(C.napi_env(env), C.napi_value(value), (*C.int32_t)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetValueUint32(env Env, value Value) (uint32, error) {
	var result uint32
	st := C.napi_get_value_uint32(C.napi_env(env), C.napi_value(value), (*C.uint32_t)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetValueInt64(env Env, value Value) (int64, error) {
	var result int64
	st := C.napi_get_value_int64(C.napi_env(env), C.napi_value(value), (*C.int64_t)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetValueBool(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_get_value_bool(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// GetValueStringUtf8 converts a JS string to a Go string (UTF-8). The
// two-call pattern of napi_get_value_string_utf8 is used: first without a
// buffer to learn the size, then to fill a C buffer.
func GetValueStringUtf8(env Env, value Value) (string, error) {
	var size C.size_t
	st := C.napi_get_value_string_utf8(C.napi_env(env), C.napi_value(value), nil, 0, &size)
	if st != C.napi_ok {
		return "", errOf(env, Status(st))
	}

	cstr := (*C.char)(C.malloc(C.size_t(size) + 1))
	if cstr == nil {
		return "", &StatusError{Code: StatusGenericFailure, Message: "out of memory"}
	}
	defer C.free(unsafe.Pointer(cstr))

	st = C.napi_get_value_string_utf8(C.napi_env(env), C.napi_value(value), cstr, C.size_t(size)+1, &size)
	if st != C.napi_ok {
		return "", errOf(env, Status(st))
	}
	return C.GoStringN(cstr, C.int(size)), nil
}

// GetValueStringLatin1 converts a JS string to ISO-8859-1 bytes.
func GetValueStringLatin1(env Env, value Value) (string, error) {
	var size C.size_t
	st := C.napi_get_value_string_latin1(C.napi_env(env), C.napi_value(value), nil, 0, &size)
	if st != C.napi_ok {
		return "", errOf(env, Status(st))
	}

	cstr := (*C.char)(C.malloc(C.size_t(size) + 1))
	if cstr == nil {
		return "", &StatusError{Code: StatusGenericFailure, Message: "out of memory"}
	}
	defer C.free(unsafe.Pointer(cstr))

	st = C.napi_get_value_string_latin1(C.napi_env(env), C.napi_value(value), cstr, C.size_t(size)+1, &size)
	if st != C.napi_ok {
		return "", errOf(env, Status(st))
	}
	return C.GoStringN(cstr, C.int(size)), nil
}

// GetValueStringUtf16 converts a JS string to UTF-16 code units.
func GetValueStringUtf16(env Env, value Value) ([]uint16, error) {
	var size C.size_t
	st := C.napi_get_value_string_utf16(C.napi_env(env), C.napi_value(value), nil, 0, &size)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}

	buf := make([]uint16, size+1) // +1 for the null terminator
	st = C.napi_get_value_string_utf16(
		C.napi_env(env), C.napi_value(value),
		(*C.char16_t)(unsafe.Pointer(&buf[0])), C.size_t(size+1), &size,
	)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	return buf[:size], nil
}

// GetValueBigIntInt64 extracts an int64 BigInt. lossless reports whether the
// conversion was exact.
func GetValueBigIntInt64(env Env, value Value) (result int64, lossless bool, err error) {
	var loss C.bool
	st := C.napi_get_value_bigint_int64(C.napi_env(env), C.napi_value(value), (*C.int64_t)(unsafe.Pointer(&result)), &loss)
	return result, bool(loss), errOf(env, Status(st))
}

// GetValueBigIntUint64 extracts a uint64 BigInt. lossless reports whether
// the conversion was exact.
func GetValueBigIntUint64(env Env, value Value) (result uint64, lossless bool, err error) {
	var loss C.bool
	st := C.napi_get_value_bigint_uint64(C.napi_env(env), C.napi_value(value), (*C.uint64_t)(unsafe.Pointer(&result)), &loss)
	return result, bool(loss), errOf(env, Status(st))
}

// GetValueBigIntWords extracts a BigInt as sign-and-magnitude words.
//
// napi_get_value_bigint_words requires sign_bit and words to be either both
// NULL (size query) or both valid — a half-NULL call is napi_invalid_arg — so
// the second call always passes a real buffer. A value that needs no words at
// all (0n) gets a one-word scratch: the engine then writes wordCount 0 and
// nothing into it.
func GetValueBigIntWords(env Env, value Value) (signBit int, words []uint64, err error) {
	var cSign C.int
	var wordCount C.size_t

	// First call learns the number of words.
	st := C.napi_get_value_bigint_words(C.napi_env(env), C.napi_value(value), nil, &wordCount, nil)
	if st != C.napi_ok {
		return 0, nil, errOf(env, Status(st))
	}

	n := int(wordCount)
	var scratch [1]C.uint64_t
	words = make([]uint64, n)
	wordsPtr := (*C.uint64_t)(unsafe.Pointer(&scratch[0]))
	if n > 0 {
		wordsPtr = (*C.uint64_t)(unsafe.Pointer(&words[0]))
	}
	st = C.napi_get_value_bigint_words(C.napi_env(env), C.napi_value(value), &cSign, &wordCount, wordsPtr)
	if st != C.napi_ok {
		return 0, nil, errOf(env, Status(st))
	}
	if c := int(wordCount); c < n {
		// Defensive: the two calls describe the same value, so this cannot
		// normally shrink; never grow past the buffer we filled.
		words = words[:c]
	}
	return int(cSign), words, nil
}

// CoerceToBool applies JS truthiness.
func CoerceToBool(env Env, value Value) (Value, error) {
	var result Value
	st := C.napi_coerce_to_bool(C.napi_env(env), C.napi_value(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CoerceToNumber applies JS ToNumber (may run user code, may throw NaN).
func CoerceToNumber(env Env, value Value) (Value, error) {
	var result Value
	st := C.napi_coerce_to_number(C.napi_env(env), C.napi_value(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CoerceToObject applies JS ToObject.
func CoerceToObject(env Env, value Value) (Value, error) {
	var result Value
	st := C.napi_coerce_to_object(C.napi_env(env), C.napi_value(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// CoerceToString applies JS ToString.
func CoerceToString(env Env, value Value) (Value, error) {
	var result Value
	st := C.napi_coerce_to_string(C.napi_env(env), C.napi_value(value), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// ---- predicates ---------------------------------------------------------

func IsArray(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_array(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsArrayBuffer(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_arraybuffer(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsTypedArray(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_typedarray(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsDataView(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_dataview(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsDate(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_date(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsError(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_error(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsPromise(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_promise(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsBuffer(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_buffer(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func IsDetachedArrayBuffer(env Env, value Value) (bool, error) {
	var result bool
	st := C.napi_is_detached_arraybuffer(C.napi_env(env), C.napi_value(value), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetDateValue(env Env, value Value) (float64, error) {
	var result float64
	st := C.napi_get_date_value(C.napi_env(env), C.napi_value(value), (*C.double)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetArrayLength(env Env, value Value) (uint32, error) {
	var result C.uint32_t
	st := C.napi_get_array_length(C.napi_env(env), C.napi_value(value), &result)
	return uint32(result), errOf(env, Status(st))
}

func StrictEquals(env Env, lhs, rhs Value) (bool, error) {
	var result bool
	st := C.napi_strict_equals(C.napi_env(env), C.napi_value(lhs), C.napi_value(rhs), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func InstanceOf(env Env, object, constructor Value) (bool, error) {
	var result bool
	st := C.napi_instanceof(C.napi_env(env), C.napi_value(object), C.napi_value(constructor), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}
