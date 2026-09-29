// Package js provides a syscall/js-style convenience layer over package
// napi. It trades the explicit error handling of napi for panics: all
// failures (engine errors and type mismatches) panic. That is safe inside
// callbacks because the callback machinery recovers panics and reports them
// to JavaScript as exceptions.
//
// Values are light wrappers around (napi.Env, napi.Value). Like in
// syscall/js, js.Value is only a view: the underlying Node-API value is
// valid for the current callback invocation unless kept alive with
// MakeRef. To call a JS function from another goroutine, use
// napi.NewThreadsafeFunction.
package js

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// Value is a JS value handle. The zero Value is invalid; panics from using
// it carry that message.
type Value struct {
	env napi.Env
	v   napi.Value
}

func (val Value) must() napi.Value {
	if val.v == nil {
		panic("js: zero Value (invalid)")
	}
	return val.v
}

// Env exposes the environment this value belongs to.
func (val Value) Env() napi.Env { return val.env }

// Raw exposes the underlying napi.Value for use with the low-level API.
func (val Value) Raw() napi.Value { return val.v }

// Wrap wraps a raw napi.Value into the convenience layer.
func Wrap(env napi.Env, v napi.Value) Value { return Value{env: env, v: v} }

// Undefined returns JS undefined for env.
func Undefined(env napi.Env) Value {
	v, err := napi.GetUndefined(env)
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: v}
}

// Null returns JS null for env.
func Null(env napi.Env) Value {
	v, err := napi.GetNull(env)
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: v}
}

// Global returns the global object (globalThis) for env.
func Global(env napi.Env) Value {
	v, err := napi.GetGlobal(env)
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: v}
}

// ---- type inspection -------------------------------------------------------

func (val Value) typeof() napi.ValueType {
	t, err := napi.Typeof(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return t
}

func (val Value) IsUndefined() bool { return val.typeof() == napi.TypeUndefined }
func (val Value) IsNull() bool      { return val.typeof() == napi.TypeNull }

// IsNullOrUndefined reports whether the value is null or undefined.
func (val Value) IsNullOrUndefined() bool {
	t := val.typeof()
	return t == napi.TypeNull || t == napi.TypeUndefined
}

func (val Value) IsBoolean() bool  { return val.typeof() == napi.TypeBoolean }
func (val Value) IsNumber() bool   { return val.typeof() == napi.TypeNumber }
func (val Value) IsString() bool   { return val.typeof() == napi.TypeString }
func (val Value) IsSymbol() bool   { return val.typeof() == napi.TypeSymbol }
func (val Value) IsBigInt() bool   { return val.typeof() == napi.TypeBigInt }
func (val Value) IsExternal() bool { return val.typeof() == napi.TypeExternal }
func (val Value) IsFunction() bool { return val.typeof() == napi.TypeFunction }
func (val Value) IsObject() bool   { return val.typeof() == napi.TypeObject }

// IsArray reports whether the value is a JS array (typeof "object" + Array).
func (val Value) IsArray() bool {
	yes, err := napi.IsArray(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return yes
}

// ---- scalar extraction ------------------------------------------------------

// Bool returns the value as a Go bool (type must be boolean).
func (val Value) Bool() bool {
	b, err := napi.GetValueBool(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return b
}

// Float returns the value as a float64 (type must be number).
func (val Value) Float() float64 {
	f, err := napi.GetValueDouble(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return f
}

// Int returns the value as an int (type must be number).
func (val Value) Int() int { return int(val.Int64()) }

// Int64 returns the value as an int64 (type must be number).
func (val Value) Int64() int64 {
	i, err := napi.GetValueInt64(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return i
}

// String returns the value as a Go string (type must be string).
func (val Value) String() string {
	s, err := napi.GetValueStringUtf8(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return s
}

// ---- properties and elements ---------------------------------------------------

// Get returns the value of property name.
func (val Value) Get(name string) Value {
	v, err := napi.GetNamedProperty(val.env, val.must(), name)
	if err != nil {
		panic(err)
	}
	return Value{env: val.env, v: v}
}

// Set sets property name to x (converted with ValueOf).
func (val Value) Set(name string, x any) {
	v := ValueOf(val.env, x)
	if err := napi.SetNamedProperty(val.env, val.must(), name, v.must()); err != nil {
		panic(err)
	}
}

// Delete removes property name, returning whether it existed.
func (val Value) Delete(name string) bool {
	key, err := napi.CreateStringUtf8(val.env, name)
	if err != nil {
		panic(err)
	}
	deleted, err := napi.DeleteProperty(val.env, val.must(), key)
	if err != nil {
		panic(err)
	}
	return deleted
}

// Index returns element i of an array.
func (val Value) Index(i int) Value {
	v, err := napi.GetElement(val.env, val.must(), uint32(i))
	if err != nil {
		panic(err)
	}
	return Value{env: val.env, v: v}
}

// Len returns the length of an array.
func (val Value) Len() int {
	n, err := napi.GetArrayLength(val.env, val.must())
	if err != nil {
		panic(err)
	}
	return int(n)
}

// Keys returns Object.keys(value).
func (val Value) Keys() []string {
	names, err := napi.GetPropertyNames(val.env, val.must())
	if err != nil {
		panic(err)
	}
	lenv := Value{env: val.env, v: names}
	out := make([]string, lenv.Len())
	for i := range out {
		out[i] = lenv.Index(i).String()
	}
	return out
}

// ---- invocation ----------------------------------------------------------------

// Call invokes the method name on the value (with the value as `this`)
// and returns the result.
func (val Value) Call(name string, args ...any) Value {
	fn := val.Get(name)
	if fn.v == nil {
		panic(fmt.Sprintf("js: no method %q", name))
	}
	argv := make([]napi.Value, len(args))
	for i, a := range args {
		argv[i] = ValueOf(val.env, a).must()
	}
	ret, err := napi.CallFunction(val.env, val.must(), fn.must(), argv...)
	if err != nil {
		panic(err)
	}
	return Value{env: val.env, v: ret}
}

// Invoke calls the value as a function with args.
func (val Value) Invoke(args ...any) Value {
	argv := make([]napi.Value, len(args))
	for i, a := range args {
		argv[i] = ValueOf(val.env, a).must()
	}
	ret, err := napi.CallFunction(val.env, Undefined(val.env).must(), val.must(), argv...)
	if err != nil {
		panic(err)
	}
	return Value{env: val.env, v: ret}
}

// New constructs a new instance with the value as constructor.
func (val Value) New(args ...any) Value {
	argv := make([]napi.Value, len(args))
	for i, a := range args {
		argv[i] = ValueOf(val.env, a).must()
	}
	ret, err := napi.NewInstance(val.env, val.must(), argv...)
	if err != nil {
		panic(err)
	}
	return Value{env: val.env, v: ret}
}

// ---- persistence -----------------------------------------------------------------

// Ref is a strong reference keeping a value alive across callback scopes.
type Ref struct {
	env napi.Env
	ref napi.Ref
}

// MakeRef pins the value so it survives past the current callback. Release
// it when done, or it pins the value for the lifetime of the environment.
func (val Value) MakeRef() *Ref {
	ref, err := napi.CreateReference(val.env, val.must(), 1)
	if err != nil {
		panic(err)
	}
	return &Ref{env: val.env, ref: ref}
}

// Value materializes the referenced value for use in the current callback.
func (r *Ref) Value() Value {
	v, err := napi.GetReferenceValue(r.env, r.ref)
	if err != nil {
		panic(err)
	}
	return Value{env: r.env, v: v}
}

// Release drops the reference.
func (r *Ref) Release() {
	if err := napi.DeleteReference(r.env, r.ref); err != nil {
		panic(err)
	}
	r.ref = nil
}

// ---- conversions -------------------------------------------------------------------

// ValueOf converts a Go value to a JS value. Supported: nil, bool, all int
// and uint kinds, float32/64, string, []any, map[string]any, func
// (this js.Value, args []js.Value) any, js.Value and anything wrapped with
// napi.CreateExternal by the caller (passed through as napi.Value or
// js.Value). Unknown types panic.
func ValueOf(env napi.Env, x any) Value {
	switch v := x.(type) {
	case nil:
		return Null(env)
	case Value:
		return v
	case napi.Value:
		return Value{env: env, v: v}
	case bool:
		b, err := napi.GetBoolean(env, v)
		if err != nil {
			panic(err)
		}
		return Value{env: env, v: b}
	case string:
		s, err := napi.CreateStringUtf8(env, v)
		if err != nil {
			panic(err)
		}
		return Value{env: env, v: s}
	case int:
		return mustInt(env, int64(v))
	case int8:
		return mustInt(env, int64(v))
	case int16:
		return mustInt(env, int64(v))
	case int32:
		return mustInt(env, int64(v))
	case int64:
		return mustInt(env, v)
	case uint:
		return mustUint(env, uint64(v))
	case uint8:
		return mustUint(env, uint64(v))
	case uint16:
		return mustUint(env, uint64(v))
	case uint32:
		return mustUint(env, uint64(v))
	case uint64:
		return mustUint(env, v)
	case float32:
		return mustFloat(env, float64(v))
	case float64:
		return mustFloat(env, v)
	case []string:
		arr, err := napi.CreateArrayWithLength(env, len(v))
		if err != nil {
			panic(err)
		}
		for i, e := range v {
			s, err := napi.CreateStringUtf8(env, e)
			if err != nil {
				panic(err)
			}
			if err := napi.SetElement(env, arr, uint32(i), s); err != nil {
				panic(err)
			}
		}
		return Value{env: env, v: arr}
	case []any:
		arr, err := napi.CreateArrayWithLength(env, len(v))
		if err != nil {
			panic(err)
		}
		for i, e := range v {
			if err := napi.SetElement(env, arr, uint32(i), ValueOf(env, e).must()); err != nil {
				panic(err)
			}
		}
		return Value{env: env, v: arr}
	case map[string]any:
		obj, err := napi.CreateObject(env)
		if err != nil {
			panic(err)
		}
		// Deterministic order for reproducibility.
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := napi.SetNamedProperty(env, obj, k, ValueOf(env, v[k]).must()); err != nil {
				panic(err)
			}
		}
		return Value{env: env, v: obj}
	case func(this Value, args []Value) any:
		return FuncOf(env, v)
	default:
		panic(fmt.Sprintf("js: cannot convert Go type %s to a JS value", reflect.TypeOf(x)))
	}
}

func mustInt(env napi.Env, v int64) Value {
	n, err := napi.CreateInt64(env, v)
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: n}
}

func mustUint(env napi.Env, v uint64) Value {
	// JS numbers are doubles; uint64 above 2^53 loses precision either way.
	// Use int64 for values that fit and a double otherwise.
	if v <= 1<<53 {
		return mustInt(env, int64(v))
	}
	return mustFloat(env, float64(v))
}

func mustFloat(env napi.Env, v float64) Value {
	n, err := napi.CreateDouble(env, v)
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: n}
}
