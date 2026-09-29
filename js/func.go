package js

import "github.com/all-thoughts-are-broken/napi-go/napi"

// FuncOf creates a JS function backed by fn, mirroring syscall/js.FuncOf.
// When JavaScript calls the function, fn receives the `this` value and the
// call arguments as js.Values; its return value is converted back with
// ValueOf.
//
// Panics inside fn (including type panics from ValueOf) are recovered by the
// callback machinery and surface in JavaScript as an Error — the process
// does not crash.
func FuncOf(env napi.Env, fn func(this Value, args []Value) any) Value {
	raw, err := napi.CreateFunction(env, "", func(e napi.Env, info napi.CallbackInfo) napi.Value {
		cb, err := napi.GetCbInfo(e, info)
		if err != nil {
			panic(err)
		}
		args := make([]Value, len(cb.Args))
		for i, a := range cb.Args {
			args[i] = Value{env: e, v: a}
		}
		ret := fn(Value{env: e, v: cb.This}, args)
		if ret == nil {
			return nil // undefined
		}
		return ValueOf(e, ret).must()
	})
	if err != nil {
		panic(err)
	}
	return Value{env: env, v: raw}
}
