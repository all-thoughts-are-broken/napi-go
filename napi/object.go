package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"fmt"
	"os"
	"unsafe"
)

// This file covers objects: properties, wrapping of native data, externals,
// type tags and freezing/sealing.

// ---- property access ----------------------------------------------------

func SetProperty(env Env, object, key, value Value) error {
	st := C.napi_set_property(C.napi_env(env), C.napi_value(object), C.napi_value(key), C.napi_value(value))
	return errOf(env, Status(st))
}

func HasProperty(env Env, object, key Value) (bool, error) {
	var result bool
	st := C.napi_has_property(C.napi_env(env), C.napi_value(object), C.napi_value(key), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetProperty(env Env, object, key Value) (Value, error) {
	var result Value
	st := C.napi_get_property(C.napi_env(env), C.napi_value(object), C.napi_value(key), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func DeleteProperty(env Env, object, key Value) (bool, error) {
	var result bool
	st := C.napi_delete_property(C.napi_env(env), C.napi_value(object), C.napi_value(key), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func HasOwnProperty(env Env, object, key Value) (bool, error) {
	var result bool
	st := C.napi_has_own_property(C.napi_env(env), C.napi_value(object), C.napi_value(key), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func SetNamedProperty(env Env, object Value, name string, value Value) error {
	cstr := C.CString(name)
	defer C.free(unsafe.Pointer(cstr))
	st := C.napi_set_named_property(C.napi_env(env), C.napi_value(object), cstr, C.napi_value(value))
	return errOf(env, Status(st))
}

func HasNamedProperty(env Env, object Value, name string) (bool, error) {
	cstr := C.CString(name)
	defer C.free(unsafe.Pointer(cstr))
	var result bool
	st := C.napi_has_named_property(C.napi_env(env), C.napi_value(object), cstr, (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetNamedProperty(env Env, object Value, name string) (Value, error) {
	cstr := C.CString(name)
	defer C.free(unsafe.Pointer(cstr))
	var result Value
	st := C.napi_get_named_property(C.napi_env(env), C.napi_value(object), cstr, (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func SetElement(env Env, object Value, index uint32, value Value) error {
	st := C.napi_set_element(C.napi_env(env), C.napi_value(object), C.uint32_t(index), C.napi_value(value))
	return errOf(env, Status(st))
}

func HasElement(env Env, object Value, index uint32) (bool, error) {
	var result bool
	st := C.napi_has_element(C.napi_env(env), C.napi_value(object), C.uint32_t(index), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetElement(env Env, object Value, index uint32) (Value, error) {
	var result Value
	st := C.napi_get_element(C.napi_env(env), C.napi_value(object), C.uint32_t(index), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func DeleteElement(env Env, object Value, index uint32) (bool, error) {
	var result bool
	st := C.napi_delete_element(C.napi_env(env), C.napi_value(object), C.uint32_t(index), (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

func GetPrototype(env Env, object Value) (Value, error) {
	var result Value
	st := C.napi_get_prototype(C.napi_env(env), C.napi_value(object), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// GetPropertyNames returns the enumerable string-keyed property names (the
// result of Object.keys).
func GetPropertyNames(env Env, object Value) (Value, error) {
	var result Value
	st := C.napi_get_property_names(C.napi_env(env), C.napi_value(object), (*C.napi_value)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// GetAllPropertyNames is the configurable variant (key mode, filter and
// number-to-string conversion), mirroring napi_get_all_property_names.
func GetAllPropertyNames(env Env, object Value, keyMode KeyCollectionMode, keyFilter KeyFilter, keyConversion KeyConversion) (Value, error) {
	var result Value
	st := C.napi_get_all_property_names(
		C.napi_env(env), C.napi_value(object),
		C.napi_key_collection_mode(keyMode), C.napi_key_filter(keyFilter), C.napi_key_conversion(keyConversion),
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	return result, errOf(env, Status(st))
}

// propsResult holds the C descriptor array produced by buildProps plus the
// callback boxes whose lifetime must now outlive the define call.
//
// It serves DefineClass only. A class has to hand its callbacks to the engine
// as C function pointers — prototype members are built from templates, not
// from function objects the binding could own — so the functions the engine
// makes from them are out of reach and their boxes are tied to the class
// constructor instead. That is sound for exactly one reason: the engine never
// collects a class (see DefineClass), so the constructor outlives every
// function that carries one of these boxes. DefineProperties has no such
// guarantee and gives every function its own box instead.
type propsResult struct {
	cProps *C.napi_property_descriptor
	boxes  []unsafe.Pointer
	cstrs  []*C.char
}

// builder finalizer: registered on the target object so that when it is
// collected, every callback box created for its properties is released.
//
//export napiGoPropsFinalizeCallback
func napiGoPropsFinalizeCallback(cEnv C.napi_env, cData, cHint unsafe.Pointer) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "node-api: panic in property finalizer: %v\n", r)
		}
	}()
	if cData == nil {
		return
	}
	boxes := loadBox(cData).([]unsafe.Pointer)
	freeBox(cData)
	for _, b := range boxes {
		freeBox(b)
	}
}

// buildProps converts the Go descriptors of a class into a C array of
// napi_property_descriptor. The caller MUST either
// result.attachFinalizer(env, object) or result.release() after the define
// call: release frees everything (for failed defines), attachFinalizer
// transfers the boxes to a Node finalizer on the object.
func buildProps(props []PropertyDescriptor) (*propsResult, error) {
	r := &propsResult{}
	if len(props) == 0 {
		return r, nil
	}

	r.cProps = C.go_props_alloc(C.size_t(len(props)))
	if r.cProps == nil {
		return nil, &StatusError{Code: StatusGenericFailure, Message: "out of memory"}
	}

	for i, p := range props {
		if p.Name == "" && p.NameValue == nil {
			r.release()
			return nil, errNamelessProperty()
		}

		var utf8name *C.char
		var nameValue Value

		if p.Name != "" {
			// The C string only needs to live until the define call
			// completes; napi copies the name into the property.
			cs := C.CString(p.Name)
			r.cstrs = append(r.cstrs, cs)
			utf8name = cs
		} else if p.NameValue != nil {
			nameValue = p.NameValue
		}

		var method, getter, setter C.napi_callback
		var data unsafe.Pointer

		// All callbacks of one descriptor share a single box: napi passes
		// descriptor data identically to method, getter and setter.
		if p.Method != nil || p.Getter != nil || p.Setter != nil {
			data = boxHandle(cbData{cb: p.Method, get: p.Getter, set: p.Setter})
			r.boxes = append(r.boxes, data)
		}
		if p.Method != nil {
			method = C.napi_callback(C.napiGoExecuteCallback)
		}
		if p.Getter != nil {
			getter = C.napi_callback(C.napiGoGetterCallback)
		}
		if p.Setter != nil {
			setter = C.napi_callback(C.napiGoSetterCallback)
		}

		C.go_props_set(
			r.cProps, C.size_t(i),
			utf8name, C.napi_value(nameValue),
			method, getter, setter,
			C.napi_value(p.Value),
			C.uint(p.Attributes),
			data,
		)
	}

	return r, nil
}

// attachFinalizer ties the callback boxes to the lifetime of object: when
// the object is garbage collected, all boxes (and their cgo handles) are
// released.
func (r *propsResult) attachFinalizer(env Env, object Value) {
	if len(r.boxes) == 0 {
		r.release()
		return
	}
	boxes := r.boxes // escape to finalizer
	h := boxHandle(boxes)
	st := C.napi_add_finalizer(
		C.napi_env(env), C.napi_value(object), h,
		C.node_api_basic_finalize(C.napiGoPropsFinalizeCallback),
		nil, nil,
	)
	if st != C.napi_ok {
		// Could not attach (non-extensible object etc.). Release the outer
		// box; the inner boxes leak for the module's lifetime rather than
		// dangling — the safe direction.
		freeBox(h)
	}
	// The temporary name strings are no longer needed either way.
	r.freeCStrs()
}

// release frees everything immediately (used when the define call failed).
func (r *propsResult) release() {
	r.freeCStrs()
	for _, b := range r.boxes {
		freeBox(b)
	}
	if r.cProps != nil {
		C.go_props_free(r.cProps)
	}
}

func (r *propsResult) freeCStrs() {
	for _, cs := range r.cstrs {
		C.free(unsafe.Pointer(cs))
	}
	r.cstrs = nil
}

// DefineProperties defines a batch of properties on an object, in order.
//
// Every Method, Getter and Setter becomes a JS function that owns its Go
// callback: the callback is released when that function is collected, not when
// object is. The two lifetimes are different. A method read off the object
// (const m = obj.m) and an accessor taken from its property descriptor are
// ordinary functions that JavaScript can keep and call long after object is
// gone; tying their callback data to object freed it underneath them, and the
// next call ran whichever Go callback had been given the recycled storage —
// measured as a method returning another function's result, with no error.
//
// To own the functions, the binding has to create them, which changes how a
// descriptor reaches the engine but not what it defines:
//
//   - a Method is created as a function and defined as a data property holding
//     it, which is what napi_define_properties does with a method itself;
//   - a Getter/Setter pair is created as functions and defined with
//     Reflect.defineProperty, because napi_define_properties can only build an
//     accessor from C callbacks whose functions it never hands back.
//
// Properties are defined one after another, as the engine does, so the object
// ends up with the same keys in the same order, and a failure leaves the
// earlier ones in place.
func DefineProperties(env Env, object Value, props []PropertyDescriptor) error {
	for _, p := range props {
		if p.Name == "" && p.NameValue == nil {
			return errNamelessProperty()
		}
	}

	d := propertyDefiner{env: env, object: object}
	for _, p := range props {
		switch {
		case p.Getter != nil || p.Setter != nil:
			// An accessor cannot join the run of data properties, so that run
			// is defined first to keep the keys in the order they were given.
			if err := d.flush(); err != nil {
				return err
			}
			if err := d.defineAccessor(p); err != nil {
				return err
			}
		case p.Method != nil:
			if err := d.flush(); err != nil {
				return err
			}
			if err := d.defineMethod(p); err != nil {
				return err
			}
		default:
			d.pending = append(d.pending, p)
		}
	}
	return d.flush()
}

// errNamelessProperty rejects a descriptor with neither Name nor NameValue.
// The engine dereferences the NULL name slot (v8impl::V8NameFromProperty
// Descriptor calls IsName() on a null Local) and takes the whole process down.
func errNamelessProperty() error {
	return &StatusError{
		Code:    StatusInvalidArg,
		Message: "property descriptor has no name: set Name or NameValue",
	}
}

// propertyDefiner carries the state of one DefineProperties call.
type propertyDefiner struct {
	env    Env
	object Value

	// pending is the run of data properties not yet handed to the engine.
	pending []PropertyDescriptor
	// checked records that the engine has accepted object as a target.
	checked bool

	// Looked up once per call, on the first accessor.
	target         Value // object as the engine sees it (after ToObject)
	reflect        Value
	defineProperty Value
}

// flush hands the pending run of data properties to napi_define_properties in
// one call. A call with no properties still reaches the engine once: that is
// its own verdict on the target (napi_object_expected for undefined, and so
// on), which has to come before an accessor is defined and has to be reported
// even when there is nothing to define.
func (d *propertyDefiner) flush() error {
	if len(d.pending) == 0 && d.checked {
		return nil
	}
	err := defineDataProperties(d.env, d.object, d.pending)
	d.pending = d.pending[:0]
	if err != nil {
		return err
	}
	d.checked = true
	return nil
}

// defineDataProperties defines value-only descriptors. None of them carries a
// callback, so nothing here has to outlive the call.
func defineDataProperties(env Env, object Value, props []PropertyDescriptor) error {
	var cProps *C.napi_property_descriptor
	if len(props) > 0 {
		cProps = C.go_props_alloc(C.size_t(len(props)))
		if cProps == nil {
			return &StatusError{Code: StatusGenericFailure, Message: "out of memory"}
		}
		defer C.go_props_free(cProps)

		for i, p := range props {
			var utf8name *C.char
			var nameValue Value
			if p.Name != "" {
				// napi copies the name into the property; the C string only
				// has to live until the define call returns.
				utf8name = C.CString(p.Name)
				defer C.free(unsafe.Pointer(utf8name))
			} else {
				nameValue = p.NameValue
			}
			C.go_props_set(
				cProps, C.size_t(i),
				utf8name, C.napi_value(nameValue),
				nil, nil, nil,
				C.napi_value(p.Value),
				C.uint(p.Attributes),
				nil,
			)
		}
	}

	st := C.napi_define_properties(C.napi_env(env), C.napi_value(object), C.size_t(len(props)), cProps)
	return errOf(env, Status(st))
}

// defineMethod defines one method: a function the binding created, held by a
// data property.
//
// It is defined on its own rather than with the run of data properties so that
// a refusal can be reported as napi_define_properties reports it. The engine
// answers napi_generic_failure when the object refuses a method and
// napi_invalid_arg when it refuses a value; the method travels as a value here,
// so the second is mapped back to the first. With a single value descriptor,
// napi_invalid_arg has no other source.
func (d *propertyDefiner) defineMethod(p PropertyDescriptor) error {
	fn, err := newCallbackFunction(d.env, "", cbData{cb: p.Method}, C.napi_callback(C.napiGoExecuteCallback))
	if err != nil {
		return err
	}
	err = defineDataProperties(d.env, d.object, []PropertyDescriptor{{
		Name: p.Name, NameValue: p.NameValue, Value: fn, Attributes: p.Attributes,
	}})
	if se, ok := err.(*StatusError); ok && se.Code == StatusInvalidArg {
		return &StatusError{Code: StatusGenericFailure, Message: "Unknown failure"}
	}
	return err
}

// defineAccessor defines one accessor property from functions the binding
// created, through Reflect.defineProperty.
//
// It reports failure the way napi_define_properties does: a definition the
// object refuses (frozen, or an existing non-configurable property) is
// napi_invalid_arg, and one that throws leaves the exception pending.
func (d *propertyDefiner) defineAccessor(p PropertyDescriptor) error {
	env := d.env
	if d.defineProperty == nil {
		// napi_define_properties applies ToObject to its target; do the same,
		// so that a primitive target behaves as it does for data properties.
		target, err := CoerceToObject(env, d.object)
		if err != nil {
			return err
		}
		global, err := GetGlobal(env)
		if err != nil {
			return err
		}
		reflect, err := GetNamedProperty(env, global, "Reflect")
		if err != nil {
			return err
		}
		defineProperty, err := GetNamedProperty(env, reflect, "defineProperty")
		if err != nil {
			return err
		}
		d.target, d.reflect, d.defineProperty = target, reflect, defineProperty
	}

	key := p.NameValue
	if p.Name != "" {
		var err error
		if key, err = CreateStringUtf8(env, p.Name); err != nil {
			return err
		}
	}

	desc, err := CreateObject(env)
	if err != nil {
		return err
	}
	if p.Getter != nil {
		fn, err := newCallbackFunction(env, "", cbData{get: p.Getter}, C.napi_callback(C.napiGoGetterCallback))
		if err != nil {
			return err
		}
		if err := SetNamedProperty(env, desc, "get", fn); err != nil {
			return err
		}
	}
	if p.Setter != nil {
		fn, err := newCallbackFunction(env, "", cbData{set: p.Setter}, C.napi_callback(C.napiGoSetterCallback))
		if err != nil {
			return err
		}
		if err := SetNamedProperty(env, desc, "set", fn); err != nil {
			return err
		}
	}
	for _, flag := range []struct {
		name string
		on   bool
	}{
		{"enumerable", p.Attributes&Enumerable != 0},
		{"configurable", p.Attributes&Configurable != 0},
	} {
		v, err := GetBoolean(env, flag.on)
		if err != nil {
			return err
		}
		if err := SetNamedProperty(env, desc, flag.name, v); err != nil {
			return err
		}
	}

	defined, err := CallFunction(env, d.reflect, d.defineProperty, d.target, key, desc)
	if err != nil {
		return err
	}
	ok, err := GetValueBool(env, defined)
	if err != nil {
		return err
	}
	if !ok {
		return &StatusError{Code: StatusInvalidArg, Message: "Invalid argument"}
	}
	return nil
}

// ---- freezing and sealing ------------------------------------------------

func ObjectFreeze(env Env, object Value) error {
	st := C.napi_object_freeze(C.napi_env(env), C.napi_value(object))
	return errOf(env, Status(st))
}

func ObjectSeal(env Env, object Value) error {
	st := C.napi_object_seal(C.napi_env(env), C.napi_value(object))
	return errOf(env, Status(st))
}

// ---- type tags ------------------------------------------------------------

// TypeTagObject brands an object with a 128-bit tag. Use a package-private
// TypeTag constant for each Go type you expose.
func TypeTagObject(env Env, object Value, tag TypeTag) error {
	cTag := C.napi_type_tag{lower: C.uint64_t(tag.Lower), upper: C.uint64_t(tag.Upper)}
	st := C.napi_type_tag_object(C.napi_env(env), C.napi_value(object), &cTag)
	return errOf(env, Status(st))
}

// CheckObjectTypeTag reports whether object carries exactly tag.
func CheckObjectTypeTag(env Env, object Value, tag TypeTag) (bool, error) {
	cTag := C.napi_type_tag{lower: C.uint64_t(tag.Lower), upper: C.uint64_t(tag.Upper)}
	var result bool
	st := C.napi_check_object_type_tag(C.napi_env(env), C.napi_value(object), &cTag, (*C.bool)(unsafe.Pointer(&result)))
	return result, errOf(env, Status(st))
}

// ---- external values -------------------------------------------------------

// CreateExternal wraps an arbitrary Go value in an opaque JS object. When
// the JS object is collected, onFinalize (if given) runs with the value.
func CreateExternal(env Env, data any, onFinalize FinalizeFunc) (Value, error) {
	box := boxHandle(finalizePayload{data: data, fn: onFinalize})

	var result Value
	st := C.napi_create_external(
		C.napi_env(env), box,
		C.node_api_basic_finalize(C.napiGoFinalizeCallback), nil,
		(*C.napi_value)(unsafe.Pointer(&result)),
	)
	if st != C.napi_ok {
		freeBox(box)
		return nil, errOf(env, Status(st))
	}
	return result, nil
}

// GetValueExternal returns the Go value wrapped by CreateExternal.
func GetValueExternal(env Env, value Value) (any, error) {
	var data unsafe.Pointer
	st := C.napi_get_value_external(C.napi_env(env), C.napi_value(value), &data)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if data == nil {
		return nil, nil
	}
	return unwrapPayload(data), nil
}

// ---- object wrapping --------------------------------------------------------

// Wrap attaches a Go value to a JS object, retrievable with Unwrap. When the
// JS object is collected, onFinalize (if given) runs with the wrapped value.
//
// Value and finalizer share ONE box on purpose: napi_remove_wrap guarantees
// the finalizer will never run, and at that point the data box is the only
// handle anybody still has on the pair — a separate hint box would be
// unreachable and leak on every RemoveWrap.
func Wrap(env Env, object Value, native any, onFinalize FinalizeFunc) error {
	box := boxHandle(finalizePayload{data: native, fn: onFinalize})

	st := C.napi_wrap(
		C.napi_env(env), C.napi_value(object), box,
		C.node_api_basic_finalize(C.napiGoFinalizeCallback), nil,
		nil,
	)
	if st != C.napi_ok {
		freeBox(box)
		return errOf(env, Status(st))
	}
	return nil
}

// Unwrap returns the Go value attached with Wrap.
func Unwrap(env Env, object Value) (any, error) {
	var data unsafe.Pointer
	st := C.napi_unwrap(C.napi_env(env), C.napi_value(object), &data)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if data == nil {
		return nil, nil
	}
	return unwrapPayload(data), nil
}

// RemoveWrap detaches the wrapped Go value; Node-API promises the finalizer
// will not run afterwards, so the caller becomes responsible for the value and
// the box is released here.
func RemoveWrap(env Env, object Value) (any, error) {
	var data unsafe.Pointer
	st := C.napi_remove_wrap(C.napi_env(env), C.napi_value(object), &data)
	if st != C.napi_ok {
		return nil, errOf(env, Status(st))
	}
	if data == nil {
		return nil, nil
	}
	v := unwrapPayload(data)
	freeBox(data)
	return v, nil
}

// AddFinalizer runs onFinalize(data) when jsObject is garbage collected.
// It does not make the value retrievable (unlike Wrap).
//
// Beware the one object kind that is never collected: a class returned by
// DefineClass (see the note there) — attaching to it commits the box until
// the environment dies.
func AddFinalizer(env Env, jsObject Value, data any, onFinalize FinalizeFunc) error {
	box := boxHandle(finalizePayload{data: data, fn: onFinalize})

	st := C.napi_add_finalizer(
		C.napi_env(env), C.napi_value(jsObject), box,
		C.node_api_basic_finalize(C.napiGoFinalizeCallback), nil, nil,
	)
	if st != C.napi_ok {
		freeBox(box)
		return errOf(env, Status(st))
	}
	return nil
}
