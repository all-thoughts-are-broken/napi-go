// Command probe is a scratch addon used to hunt down binding bugs. It is
// deliberately hostile: it feeds the binding malformed inputs, misuse
// patterns and leak-prone paths that example/ and stress/ do not cover.
//
// Build:
//
//	go build -buildmode=c-shared -o probe/probe.node ./probe
//
// Run: node probe/run.mjs
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/js"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

func main() {}

// ---- JS value helpers -----------------------------------------------------

func newObj(env napi.Env) napi.Value {
	o, _ := napi.CreateObject(env)
	return o
}

func setStr(env napi.Env, o napi.Value, k, v string) {
	s, _ := napi.CreateStringUtf8(env, v)
	napi.SetNamedProperty(env, o, k, s)
}

func setI64(env napi.Env, o napi.Value, k string, v int64) {
	n, _ := napi.CreateInt64(env, v)
	napi.SetNamedProperty(env, o, k, n)
}

func setBool(env napi.Env, o napi.Value, k string, v bool) {
	b, _ := napi.GetBoolean(env, v)
	napi.SetNamedProperty(env, o, k, b)
}

func setVal(env napi.Env, o napi.Value, k string, v napi.Value) {
	napi.SetNamedProperty(env, o, k, v)
}

// statusOf renders an error as a short, comparable string.
func statusOf(err error) string {
	if err == nil {
		return "ok"
	}
	if st, ok := napi.AsStatus(err); ok {
		return st.String()
	}
	return "err:" + err.Error()
}

// errText keeps the message, not just the status name. The binding's guards and
// the engine answer the same status for a call on a dead threadsafe function
// (napi_closing); which of the two answered is the whole question, and only the
// message tells them apart.
func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// args helpers: probes must never panic on a missing argument.

func arg(cb napi.CbInfoResult, i int) napi.Value {
	if i < len(cb.Args) {
		return cb.Args[i]
	}
	return nil
}

func num(env napi.Env, v napi.Value, def int64) int64 {
	if v == nil {
		return def
	}
	n, err := napi.GetValueInt64(env, v)
	if err != nil {
		return def
	}
	return n
}

func boolArg(env napi.Env, v napi.Value, def bool) bool {
	if v == nil {
		return def
	}
	b, err := napi.GetValueBool(env, v)
	if err != nil {
		return def
	}
	return b
}

func strArg(env napi.Env, v napi.Value) string {
	if v == nil {
		return ""
	}
	s, err := napi.GetValueStringUtf8(env, v)
	if err != nil {
		return ""
	}
	return s
}

func need(env napi.Env, cb napi.CbInfoResult, i int, chk func(napi.Env, napi.Value) error) napi.Value {
	v := arg(cb, i)
	if v == nil {
		return nil
	}
	if err := chk(env, v); err != nil {
		return nil
	}
	return v
}

func isObj(env napi.Env, v napi.Value) error {
	t, err := napi.Typeof(env, v)
	if err != nil {
		return err
	}
	if t != napi.TypeObject && t != napi.TypeFunction {
		return fmt.Errorf("not an object: %v", t)
	}
	return nil
}

func boxCounters(env napi.Env, o napi.Value) {
	a, f := napi.DebugStats()
	setI64(env, o, "boxAlloc", int64(a))
	setI64(env, o, "boxFreed", int64(f))
	setI64(env, o, "boxOutstanding", int64(a-f))
}

func delta(env napi.Env, o napi.Value, preA, preF uint64) {
	a, f := napi.DebugStats()
	setI64(env, o, "allocDelta", int64(a-preA))
	setI64(env, o, "freeDelta", int64(f-preF))
	setI64(env, o, "outstanding", int64(a-f))
}

// ---- H1: panic path leaks a C string --------------------------------------

// panicSized panics with a message of n bytes. The trampoline converts it to
// a JS exception; probeStats + RSS tell us whether the C string leaked.
func panicSized(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 16)
	panic(errors.New(strings.Repeat("P", int(n))))
}

// ---- H2: Wrap/RemoveWrap box accounting ------------------------------------

func wrapRemove(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1000)
	withFin := boolArg(env, arg(cb, 1), false)

	preA, preF := napi.DebugStats()
	fails := int64(0)
	for i := int64(0); i < n; i++ {
		o := newObj(env)
		var fin napi.FinalizeFunc
		if withFin {
			fin = func(napi.Env, any) {}
		}
		if err := napi.Wrap(env, o, i, fin); err != nil {
			fails++
		}
		if _, err := napi.RemoveWrap(env, o); err != nil {
			fails++
		}
	}
	out := newObj(env)
	setI64(env, out, "n", n)
	setI64(env, out, "fails", fails)
	delta(env, out, preA, preF)
	return out
}

// wrapFinRemove wraps with an onFinalize callback and then removes the wrap:
// the finalizer will never run, so whoever allocated the finalize hint owns it.
func wrapFinRemove(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1000)
	preA, preF := napi.DebugStats()
	for i := int64(0); i < n; i++ {
		o := newObj(env)
		napi.Wrap(env, o, i, func(napi.Env, any) {})
		napi.RemoveWrap(env, o)
	}
	out := newObj(env)
	setI64(env, out, "n", n)
	delta(env, out, preA, preF)
	return out
}

func wrapTwice(env napi.Env, info napi.CallbackInfo) napi.Value {
	o := newObj(env)
	preA, preF := napi.DebugStats()
	e1 := napi.Wrap(env, o, "first", nil)
	e2 := napi.Wrap(env, o, "second", nil)
	got, e3 := napi.Unwrap(env, o)
	out := newObj(env)
	setStr(env, out, "wrap1", statusOf(e1))
	setStr(env, out, "wrap2", statusOf(e2))
	setStr(env, out, "unwrap", statusOf(e3))
	setVal(env, out, "value", nil)
	if s, ok := got.(string); ok {
		setStr(env, out, "value", s)
	} else {
		setStr(env, out, "value", fmt.Sprintf("%v", got))
	}
	delta(env, out, preA, preF)
	return out
}

func removeWrapTwice(env napi.Env, info napi.CallbackInfo) napi.Value {
	o := newObj(env)
	preA, preF := napi.DebugStats()
	e1 := napi.Wrap(env, o, "v", nil)
	v1, e2 := napi.RemoveWrap(env, o)
	v2, e3 := napi.RemoveWrap(env, o)
	out := newObj(env)
	setStr(env, out, "wrap", statusOf(e1))
	setStr(env, out, "remove1", statusOf(e2))
	setStr(env, out, "remove2", statusOf(e3))
	setStr(env, out, "v1", fmt.Sprintf("%v", v1))
	setStr(env, out, "v2", fmt.Sprintf("%v", v2))
	delta(env, out, preA, preF)
	return out
}

// ---- H3: callback with NULL data ------------------------------------------

// ndFunction returns a JS function created by raw Node-API with data = NULL.
// Calling it exercises napiGoExecuteCallback with a nil box.
func ndFunction(env napi.Env, info napi.CallbackInfo) napi.Value {
	v, err := rawNullDataFunction(env)
	if err != nil {
		setStr(env, newObj(env), "err", err.Error())
		return nil
	}
	return v
}

// ---- H10/descriptor edge cases --------------------------------------------

func mkPropFn(env napi.Env, _ napi.CallbackInfo) napi.Value {
	v, _ := napi.CreateInt32(env, 111)
	return v
}

func mkGetter(env napi.Env, _ napi.CallbackInfo) napi.Value {
	v, _ := napi.CreateInt32(env, 7)
	return v
}

func mkSetter(env napi.Env, _ napi.CallbackInfo) napi.Value {
	return nil
}

func defineBad(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	target := arg(cb, 1)

	preA, preF := napi.DebugStats()
	out := newObj(env)
	var err error
	v1, _ := napi.CreateInt32(env, 1)
	nameVal, _ := napi.CreateStringUtf8(env, "x")

	obj := target
	if obj == nil {
		obj = newObj(env)
	}

	switch kind {
	case 0: // no name at all
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Value: v1}})
	case 1: // value + method on the same descriptor
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "x", Value: v1, Method: mkPropFn}})
	case 2: // Name and NameValue both set
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "x", NameValue: nameVal, Value: v1}})
	case 3: // empty property list
		err = napi.DefineProperties(env, obj, nil)
	case 4: // duplicate names
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{
			{Name: "dup", Value: v1}, {Name: "dup", Method: mkPropFn},
		})
	case 5: // frozen target
		frozen := newObj(env)
		napi.ObjectFreeze(env, frozen)
		err = napi.DefineProperties(env, frozen, []napi.PropertyDescriptor{{Name: "x", Value: v1}})
	case 6: // non-object target
		err = napi.DefineProperties(env, v1, []napi.PropertyDescriptor{{Name: "x", Value: v1}})
	case 7: // getter only
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "g", Getter: mkGetter}})
	case 8: // setter only
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "s", Setter: mkSetter}})
	case 9: // class, no properties
		_, err = napi.DefineClass(env, napi.ClassDescriptor{Name: "Empty", Constructor: mkPropFn})
	case 10: // class, duplicate property names
		_, err = napi.DefineClass(env, napi.ClassDescriptor{
			Name: "Dup", Constructor: mkPropFn,
			Properties: []napi.PropertyDescriptor{
				{Name: "m", Method: mkPropFn}, {Name: "m", Method: mkPropFn},
			},
		})
	case 11: // class, static accessor
		_, err = napi.DefineClass(env, napi.ClassDescriptor{
			Name: "Stat", Constructor: mkPropFn,
			Properties: []napi.PropertyDescriptor{
				{Name: "sg", Getter: mkGetter, Attributes: napi.Static | napi.Enumerable},
				{Name: "m", Method: mkPropFn},
				{Name: "v", Value: v1},
			},
		})
	case 12: // class, property with neither name form and no value
		_, err = napi.DefineClass(env, napi.ClassDescriptor{
			Name: "NoName", Constructor: mkPropFn,
			Properties: []napi.PropertyDescriptor{{}},
		})
	case 13: // class, non-object target for DefineProperties (undefined)
		u, _ := napi.GetUndefined(env)
		err = napi.DefineProperties(env, u, []napi.PropertyDescriptor{{Name: "x", Value: v1}})
	case 14: // name but no value / method / accessor at all
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "onlyname"}})
	case 15: // method + getter on the same descriptor
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{Name: "x", Method: mkPropFn, Getter: mkGetter}})
	case 16: // symbol-keyed property
		sym, serr := napi.CreateSymbol(env, mustStr(env, "sym"))
		setStr(env, out, "symErr", statusOf(serr))
		err = napi.DefineProperties(env, obj, []napi.PropertyDescriptor{{NameValue: sym, Value: v1}})
	case 17: // DefineClass with a nil constructor
		_, err = napi.DefineClass(env, napi.ClassDescriptor{
			Name:       "NoCtor",
			Properties: []napi.PropertyDescriptor{{Name: "m", Method: mkPropFn}},
		})
	}

	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "target", obj)
	delta(env, out, preA, preF)
	return out
}

// ---- handle scope misuse ---------------------------------------------------

func escapeTwice(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	scope, err := napi.OpenEscapableHandleScope(env)
	if err != nil {
		setStr(env, out, "open", statusOf(err))
		return out
	}
	v, _ := napi.CreateInt32(env, 5)
	_, e1 := napi.EscapeHandle(env, scope, v)
	_, e2 := napi.EscapeHandle(env, scope, v)
	setStr(env, out, "escape1", statusOf(e1))
	setStr(env, out, "escape2", statusOf(e2))
	closeErr := napi.CloseEscapableHandleScope(env, scope)
	setStr(env, out, "close", statusOf(closeErr))
	return out
}

func scopeMisuse(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)

	switch kind {
	case 0: // close a handle scope twice
		s, _ := napi.OpenHandleScope(env)
		e1 := napi.CloseHandleScope(env, s)
		e2 := napi.CloseHandleScope(env, s)
		setStr(env, out, "close1", statusOf(e1))
		setStr(env, out, "close2", statusOf(e2))
	case 1: // close out of order (LIFO violation)
		s1, _ := napi.OpenHandleScope(env)
		s2, _ := napi.OpenHandleScope(env)
		e1 := napi.CloseHandleScope(env, s1) // wrong: s2 is innermost
		setStr(env, out, "closeOuter", statusOf(e1))
		// The engine only checks open_handle_scopes != 0, so it accepts both
		// the inverted order and the second close of s1. Record what it does
		// rather than assuming.
		setStr(env, out, "closeInner", statusOf(napi.CloseHandleScope(env, s2)))
		setStr(env, out, "closeOuterAgain", statusOf(napi.CloseHandleScope(env, s1)))
	case 2: // make_callback success path, then close a callback scope twice
		fn, _ := napi.CreateFunction(env, "noop", func(napi.Env, napi.CallbackInfo) napi.Value { return nil })
		ctx, _ := napi.AsyncInit(env, nil, "probe")
		cs, err := napi.OpenCallbackScope(env, nil, ctx)
		if err != nil {
			setStr(env, out, "open", statusOf(err))
			break
		}
		// A NULL recv is rejected by the engine, so pass a real receiver:
		// otherwise the only thing this probe ever measures is the rejection.
		recv := newObj(env)
		_, callErr := napi.MakeCallback(env, ctx, recv, fn)
		e1 := napi.CloseCallbackScope(env, cs)
		e2 := napi.CloseCallbackScope(env, cs)
		setStr(env, out, "call", statusOf(callErr))
		setStr(env, out, "close1", statusOf(e1))
		setStr(env, out, "close2", statusOf(e2))
		napi.AsyncDestroy(env, ctx)
	case 3: // destroy an async context twice
		ctx, err := napi.AsyncInit(env, nil, "probe")
		setStr(env, out, "init", statusOf(err))
		if err == nil {
			setStr(env, out, "destroy1", statusOf(napi.AsyncDestroy(env, ctx)))
			setStr(env, out, "destroy2", statusOf(napi.AsyncDestroy(env, ctx)))
		}
	case 4: // destroy nothing
		setStr(env, out, "destroyNil", statusOf(napi.AsyncDestroy(env, nil)))
	case 5: // destroy a context that was never created by AsyncInit
		var bogus napi.AsyncContext
		raw, err := rawMalloc(8)
		setStr(env, out, "mallocErr", statusOf(err))
		if err == nil {
			bogus = napi.AsyncContext(raw)
			setStr(env, out, "destroyBogus", statusOf(napi.AsyncDestroy(env, bogus)))
			rawFree(raw)
		}
	case 6: // double-close a scope that is not the innermost one.
		// napi_close_handle_scope only rejects a close when
		// open_handle_scopes has already hit zero, so with a spare scope on
		// the stack the engine would delete the same V8 handle scope twice.
		// The trailing close of s2 matters: the engine's counter must end up
		// balanced or the callback wrapper aborts on scope mismatch.
		s1, _ := napi.OpenHandleScope(env)
		s2, _ := napi.OpenHandleScope(env)
		setStr(env, out, "close1", statusOf(napi.CloseHandleScope(env, s1)))
		setStr(env, out, "close2", statusOf(napi.CloseHandleScope(env, s1)))
		setStr(env, out, "balance", statusOf(napi.CloseHandleScope(env, s2)))
	case 7: // close an escapable scope through the plain-scope entry point
		es, err := napi.OpenEscapableHandleScope(env)
		setStr(env, out, "open", statusOf(err))
		if err == nil {
			setStr(env, out, "wrongClose", statusOf(napi.CloseHandleScope(env, napi.HandleScope(es))))
			setStr(env, out, "rightClose", statusOf(napi.CloseEscapableHandleScope(env, es)))
		}
	}
	return out
}

// ---- exception semantics ----------------------------------------------------

func clearNoExc(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	v, err := napi.GetAndClearLastException(env)
	setStr(env, out, "err", statusOf(err))
	if v == nil {
		setStr(env, out, "type", "<nil value>")
	} else {
		t, terr := napi.Typeof(env, v)
		setStr(env, out, "type", fmt.Sprintf("%v", t))
		setStr(env, out, "typeErr", statusOf(terr))
	}
	pending, _ := napi.IsExceptionPending(env)
	setBool(env, out, "stillPending", pending)
	return out
}

func throwRoundtrip(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)

	var err error
	var ctorErr, getErr string
	var clearedType string
	switch kind {
	case 0:
		n, _ := napi.CreateInt32(env, 42)
		err = napi.Throw(env, n)
	case 1:
		msg, _ := napi.CreateStringUtf8(env, "boom")
		ev, cerr := napi.CreateError(env, nil, msg)
		ctorErr = statusOf(cerr)
		err = napi.Throw(env, ev)
	case 2:
		err = napi.ThrowError(env, "E_CODE", "thrown from Go")
	case 3:
		err = napi.ThrowTypeError(env, "", "bad type")
	case 4:
		err = napi.ThrowRangeError(env, "", "bad range")
	}
	throwErr := statusOf(err)

	// Every Node-API call that touches JS fails while an exception is
	// pending, so the state must be captured before anything else runs.
	pending, perr := napi.IsExceptionPending(env)
	pendingErr := statusOf(perr)

	got, gerr := napi.GetAndClearLastException(env)
	getErr = statusOf(gerr)
	if got != nil {
		t, _ := napi.Typeof(env, got)
		clearedType = fmt.Sprintf("%v", t)
	} else {
		clearedType = "<nil>"
	}
	still, _ := napi.IsExceptionPending(env)

	out := newObj(env)
	setStr(env, out, "throw", throwErr)
	setStr(env, out, "ctorErr", ctorErr)
	setBool(env, out, "pending", pending)
	setStr(env, out, "pendingErr", pendingErr)
	setStr(env, out, "getErr", getErr)
	setStr(env, out, "clearedType", clearedType)
	setBool(env, out, "stillPending", still)
	return out
}

func coerceProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	mode := num(env, arg(cb, 0), 0)
	target := arg(cb, 1)

	var (
		v       napi.Value
		err     error
		errStr  string
		typeStr string
		pending bool
	)
	switch mode {
	case 0:
		v, err = napi.CoerceToObject(env, target)
	case 1:
		v, err = napi.CoerceToNumber(env, target)
	case 2:
		v, err = napi.CoerceToString(env, target)
	case 3:
		v, err = napi.CoerceToBool(env, target)
	}
	errStr = statusOf(err)
	if v != nil {
		t, _ := napi.Typeof(env, v)
		typeStr = fmt.Sprintf("%v", t)
	} else {
		typeStr = "<nil>"
	}
	pending, _ = napi.IsExceptionPending(env)
	if pending {
		napi.GetAndClearLastException(env)
	}

	out := newObj(env)
	setStr(env, out, "err", errStr)
	setStr(env, out, "type", typeStr)
	setBool(env, out, "pending", pending)
	return out
}

// ---- typed array / dataview / arraybuffer ----------------------------------

func taMake(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	abl := num(env, arg(cb, 0), 16)
	off := num(env, arg(cb, 1), 0)
	n := num(env, arg(cb, 2), 4)

	ab, data, err := napi.CreateArrayBuffer(env, uint64(abl))
	out := newObj(env)
	setStr(env, out, "abErr", statusOf(err))
	if err != nil {
		return out
	}
	for i := range data {
		data[i] = byte(i + 1)
	}
	ta, err := napi.CreateTypedArray(env, napi.Uint8Array, uint64(n), ab, uint64(off))
	setStr(env, out, "taErr", statusOf(err))
	if err != nil {
		return out
	}
	setVal(env, out, "ta", ta)
	return out
}

func taInfo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ta := need(env, cb, 0, isObj)
	out := newObj(env)
	if ta == nil {
		setStr(env, out, "err", "missing arg")
		return out
	}
	inf, err := napi.GetTypedArrayInfo(env, ta)
	setStr(env, out, "err", statusOf(err))
	if err != nil {
		return out
	}
	setI64(env, out, "type", int64(inf.Type))
	setI64(env, out, "length", int64(inf.Length))
	setI64(env, out, "byteOffset", int64(inf.ByteOffset))
	setI64(env, out, "dataLen", int64(len(inf.Data)))
	setI64(env, out, "firstByte", int64(inf.Data[0]))
	setI64(env, out, "lastByte", int64(inf.Data[len(inf.Data)-1]))
	setVal(env, out, "arrayBuffer", inf.ArrayBuffer)
	return out
}

func dvMake(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	abl := num(env, arg(cb, 0), 16)
	off := num(env, arg(cb, 1), 0)
	n := num(env, arg(cb, 2), 4)

	ab, data, err := napi.CreateArrayBuffer(env, uint64(abl))
	out := newObj(env)
	setStr(env, out, "abErr", statusOf(err))
	if err != nil {
		return out
	}
	for i := range data {
		data[i] = byte(i + 1)
	}
	dv, err := napi.CreateDataView(env, uint64(n), ab, uint64(off))
	setStr(env, out, "dvErr", statusOf(err))
	if err != nil {
		return out
	}
	setVal(env, out, "dv", dv)
	return out
}

func dvInfo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	dv := arg(cb, 0)
	out := newObj(env)
	if dv == nil {
		setStr(env, out, "err", "missing arg")
		return out
	}
	inf, err := napi.GetDataViewInfo(env, dv)
	setStr(env, out, "err", statusOf(err))
	if err != nil {
		return out
	}
	setI64(env, out, "byteLength", int64(inf.ByteLength))
	setI64(env, out, "byteOffset", int64(inf.ByteOffset))
	setI64(env, out, "dataLen", int64(len(inf.Data)))
	if len(inf.Data) > 0 {
		setI64(env, out, "firstByte", int64(inf.Data[0]))
	}
	return out
}

func abMake(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 16)
	ab, data, err := napi.CreateArrayBuffer(env, uint64(n))
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	if err != nil {
		return out
	}
	for i := range data {
		data[i] = byte(0xA0 + i)
	}
	setVal(env, out, "ab", ab)
	return out
}

func abInfo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	out := newObj(env)
	inf, err := napi.GetArrayBufferInfo(env, arg(cb, 0))
	setStr(env, out, "err", statusOf(err))
	setI64(env, out, "dataLen", int64(len(inf.Data)))
	det, derr := napi.IsDetachedArrayBuffer(env, arg(cb, 0))
	setBool(env, out, "detached", det)
	setStr(env, out, "detachErr", statusOf(derr))
	ta, terr := napi.CreateTypedArray(env, napi.Uint8Array, 1, arg(cb, 0), 0)
	setStr(env, out, "taErr", statusOf(terr))
	_ = ta
	return out
}

func abDetach(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	out := newObj(env)
	err := napi.DetachArrayBuffer(env, arg(cb, 0))
	setStr(env, out, "err", statusOf(err))
	return out
}

// abLifecycle drives create -> info -> detach -> info/write entirely inside
// one callback, so a use-after-free on the detached storage shows up as a
// process crash rather than a cross-process protocol problem.
func abLifecycle(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	ab, data, err := napi.CreateArrayBuffer(env, 16)
	setStr(env, out, "create", statusOf(err))
	if err != nil {
		return out
	}
	for i := range data {
		data[i] = byte(i + 1)
	}
	inf1, e1 := napi.GetArrayBufferInfo(env, ab)
	setStr(env, out, "infoBefore", statusOf(e1))
	setI64(env, out, "lenBefore", int64(len(inf1.Data)))
	d0, _ := napi.IsDetachedArrayBuffer(env, ab)
	setBool(env, out, "detachedBefore", d0)

	setStr(env, out, "detach", statusOf(napi.DetachArrayBuffer(env, ab)))

	d1, e2 := napi.IsDetachedArrayBuffer(env, ab)
	setBool(env, out, "detachedAfter", d1)
	setStr(env, out, "isDetachedErr", statusOf(e2))

	inf2, e3 := napi.GetArrayBufferInfo(env, ab)
	setStr(env, out, "infoAfter", statusOf(e3))
	setI64(env, out, "lenAfter", int64(len(inf2.Data)))
	// If the engine hands back a non-empty view of detached storage, writing
	// through it is a use-after-free. This is the probe.
	if len(inf2.Data) > 0 {
		inf2.Data[0] = 0xEE
		setBool(env, out, "wroteDetachedStorage", true)
	} else {
		setBool(env, out, "wroteDetachedStorage", false)
	}
	_, e4 := napi.CreateTypedArray(env, napi.Uint8Array, 1, ab, 0)
	setStr(env, out, "taAfterDetach", statusOf(e4))
	_, e5 := napi.GetTypedArrayInfo(env, ab)
	setStr(env, out, "tabInfoErr", statusOf(e5))
	setStr(env, out, "detachAgain", statusOf(napi.DetachArrayBuffer(env, ab)))
	return out
}

// ---- string encodings -------------------------------------------------------

func utf16Probe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	src := arg(cb, 0)
	out := newObj(env)
	if src == nil {
		setStr(env, out, "err", "missing arg")
		return out
	}
	units, err := napi.GetValueStringUtf16(env, src)
	setStr(env, out, "getErr", statusOf(err))
	setI64(env, out, "n", int64(len(units)))
	setVal(env, out, "units", unitsToValue(env, units))

	// round-trip: rebuild a JS string from those units and compare.
	back, cerr := napi.CreateStringUtf16(env, units)
	setStr(env, out, "createErr", statusOf(cerr))
	if back != nil {
		eq, _ := napi.StrictEquals(env, back, src)
		setBool(env, out, "equal", eq)
		setVal(env, out, "back", back)
	}
	return out
}

func unitsToValue(env napi.Env, units []uint16) napi.Value {
	arr, _ := napi.CreateArrayWithLength(env, len(units))
	for i, u := range units {
		v, _ := napi.CreateUint32(env, uint32(u))
		napi.SetElement(env, arr, uint32(i), v)
	}
	return arr
}

func latin1Probe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	out := newObj(env)
	s := strArg(env, arg(cb, 0))
	v, err := napi.CreateStringLatin1(env, s)
	setStr(env, out, "createErr", statusOf(err))
	if err != nil {
		return out
	}
	setVal(env, out, "value", v)
	back, berr := napi.GetValueStringLatin1(env, v)
	setStr(env, out, "backErr", statusOf(berr))
	setStr(env, out, "back", back)
	setI64(env, out, "backLen", int64(len(back)))

	u8, u8err := napi.GetValueStringUtf8(env, v)
	setStr(env, out, "utf8Err", statusOf(u8err))
	setStr(env, out, "utf8", u8)
	return out
}

func utf8Edge(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	mode := num(env, arg(cb, 0), 0)
	out := newObj(env)

	var goStr string
	switch mode {
	case 0:
		goStr = "a\x00b" // embedded NUL
	case 1:
		goStr = "\xff\xfe" // invalid UTF-8
	case 2:
		goStr = "" // empty
	case 3:
		goStr = strings.Repeat("x", 4096)
	}
	v, err := napi.CreateStringUtf8(env, goStr)
	setStr(env, out, "createErr", statusOf(err))
	if err != nil {
		return out
	}
	back, berr := napi.GetValueStringUtf8(env, v)
	setStr(env, out, "backErr", statusOf(berr))
	setI64(env, out, "inLen", int64(len(goStr)))
	setI64(env, out, "outLen", int64(len(back)))
	setStr(env, out, "back", back)
	setVal(env, out, "value", v)
	return out
}

// ---- bigint -----------------------------------------------------------------

func bigintWords(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	v := arg(cb, 0)
	out := newObj(env)
	if v == nil {
		setStr(env, out, "err", "missing arg")
		return out
	}
	sign, words, err := napi.GetValueBigIntWords(env, v)
	setStr(env, out, "err", statusOf(err))
	setI64(env, out, "sign", int64(sign))
	setI64(env, out, "nwords", int64(len(words)))
	arr, _ := napi.CreateArrayWithLength(env, len(words))
	for i, w := range words {
		x, _ := napi.CreateBigIntUint64(env, w)
		napi.SetElement(env, arr, uint32(i), x)
	}
	setVal(env, out, "words", arr)

	i64v, loss64, err64 := napi.GetValueBigIntInt64(env, v)
	setStr(env, out, "i64Err", statusOf(err64))
	setI64(env, out, "i64", i64v)
	setBool(env, out, "losslessI64", loss64)

	u64v, lossU, errU := napi.GetValueBigIntUint64(env, v)
	setStr(env, out, "u64Err", statusOf(errU))
	setStr(env, out, "u64", fmt.Sprintf("%d", u64v))
	setBool(env, out, "losslessU64", lossU)
	return out
}

func bigintMake(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	sign := num(env, arg(cb, 0), 0)
	spec := strArg(env, arg(cb, 1))
	out := newObj(env)

	var words []uint64
	if spec != "" {
		for _, p := range strings.Split(spec, ",") {
			var w uint64
			fmt.Sscanf(strings.TrimSpace(p), "%d", &w)
			words = append(words, w)
		}
	}
	v, err := napi.CreateBigIntWords(env, int(sign), words)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "value", v)
	return out
}

func bigintCreate(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 0)
	u := boolArg(env, arg(cb, 1), false)
	out := newObj(env)
	if u {
		v, err := napi.CreateBigIntUint64(env, uint64(n))
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "value", v)
		return out
	}
	v, err := napi.CreateBigIntInt64(env, n)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "value", v)
	return out
}

// ---- callbacks / args / this ----------------------------------------------

func argProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, err := napi.GetCbInfo(env, info)
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setI64(env, out, "n", int64(len(cb.Args)))
	setBool(env, out, "thisIsGlobal", false)
	t, _ := napi.Typeof(env, cb.This)
	setStr(env, out, "thisType", fmt.Sprintf("%v", t))
	setI64(env, out, "dataNil", boolToI(cb.Data == nil))
	if len(cb.Args) > 0 {
		s, _ := napi.GetValueStringUtf8(env, cb.Args[0])
		setStr(env, out, "first", s)
	}
	return out
}

func boolToI(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func newTargetProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	nt, err := napi.GetNewTarget(env, info)
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setBool(env, out, "hasNewTarget", nt != nil)
	return out
}

func callThrow(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	recv, _ := napi.GetUndefined(env)
	v, err := napi.CallFunction(env, recv, arg(cb, 0))
	errStr := statusOf(err)

	pending, _ := napi.IsExceptionPending(env)
	excMessage := ""
	if pending {
		got, _ := napi.GetAndClearLastException(env)
		if got != nil {
			msg, _ := napi.GetNamedProperty(env, got, "message")
			if msg != nil {
				excMessage, _ = napi.GetValueStringUtf8(env, msg)
			}
		}
	}
	// The engine must still be usable afterwards.
	after, aerr := napi.CreateInt32(env, 5)

	out := newObj(env)
	setStr(env, out, "err", errStr)
	setBool(env, out, "pending", pending)
	setStr(env, out, "excMessage", excMessage)
	setStr(env, out, "afterErr", statusOf(aerr))
	setVal(env, out, "after", after)
	_ = v
	return out
}

func pendingMsg(env napi.Env, out napi.Value, exc napi.Value) {
	msg, _ := napi.GetNamedProperty(env, exc, "message")
	if msg != nil {
		s, _ := napi.GetValueStringUtf8(env, msg)
		setStr(env, out, "excMessage", s)
	}
}

func runScriptThrow(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	src := strArg(env, arg(cb, 0))
	v, err := napi.RunScript(env, src)

	// Everything that touches the engine has to happen AFTER the pending
	// exception is captured and cleared: while one is pending, every
	// Node-API property call fails with napi_pending_exception, so building
	// the report object first would silently produce an empty object.
	pending, _ := napi.IsExceptionPending(env)
	var thrown napi.Value
	if pending {
		thrown, _ = napi.GetAndClearLastException(env)
	}

	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setBool(env, out, "pending", pending)
	if thrown != nil {
		pendingMsg(env, out, thrown)
	}
	setVal(env, out, "result", v)
	return out
}

func callFn(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	out := newObj(env)
	recv := arg(cb, 0)
	fn := arg(cb, 1)
	rest := cb.Args
	if len(rest) > 2 {
		rest = rest[2:]
	} else {
		rest = nil
	}
	v, err := napi.CallFunction(env, recv, fn, rest...)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "value", v)
	return out
}

func newInstance(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	out := newObj(env)
	v, err := napi.NewInstance(env, arg(cb, 0), arg(cb, 1))
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "value", v)
	if v != nil {
		np, _ := napi.HasNamedProperty(env, v, "n")
		setBool(env, out, "hasN", np)
	}
	return out
}

// ---- instance data / finalizers / references --------------------------------

func instanceDataTwice(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	preA, preF := napi.DebugStats()
	e1 := napi.SetInstanceData(env, "first", nil)
	e2 := napi.SetInstanceData(env, "second", nil)
	got, e3 := napi.GetInstanceData(env)
	setStr(env, out, "set1", statusOf(e1))
	setStr(env, out, "set2", statusOf(e2))
	setStr(env, out, "get", statusOf(e3))
	setStr(env, out, "value", fmt.Sprintf("%v", got))
	delta(env, out, preA, preF)
	return out
}

var (
	finMu         sync.Mutex
	finHits       = map[string]int{}
	wrapFins      atomic.Int64
	wrapFinPanics atomic.Int64
)

func finalizerTwice(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj := arg(cb, 0)
	if obj == nil {
		obj = newObj(env)
	}
	e1 := napi.AddFinalizer(env, obj, "a", func(napi.Env, any) {
		finMu.Lock()
		finHits["a"]++
		finMu.Unlock()
	})
	e2 := napi.AddFinalizer(env, obj, "b", func(napi.Env, any) {
		finMu.Lock()
		finHits["b"]++
		finMu.Unlock()
	})
	out := newObj(env)
	setStr(env, out, "add1", statusOf(e1))
	setStr(env, out, "add2", statusOf(e2))
	setVal(env, out, "object", obj)
	return out
}

func probeCounters(env napi.Env, info napi.CallbackInfo) napi.Value {
	finMu.Lock()
	defer finMu.Unlock()
	out := newObj(env)
	setI64(env, out, "finA", int64(finHits["a"]))
	setI64(env, out, "finB", int64(finHits["b"]))
	setI64(env, out, "wrapFins", wrapFins.Load())
	return out
}

func wrapWithFinalize(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj := arg(cb, 0)
	if obj == nil {
		obj = newObj(env)
	}
	preA, preF := napi.DebugStats()
	err := napi.Wrap(env, obj, "payload", func(napi.Env, any) {
		wrapFins.Add(1)
	})
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "object", obj)
	delta(env, out, preA, preF)
	return out
}

// wrapFinPanic wraps n objects whose finalizer panics. Dropping the objects and
// forcing GC must still return the box ledger to baseline: the release has to
// survive the unwind out of the user finalizer. (The first fix freed the box
// inline after the user callback, so a panicking finalizer leaked it forever.)
func wrapFinPanic(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1000)
	preA, preF := napi.DebugStats()
	fails := int64(0)
	for i := int64(0); i < n; i++ {
		o := newObj(env)
		if err := napi.Wrap(env, o, i, func(napi.Env, any) {
			wrapFinPanics.Add(1)
			panic("finalizer boom")
		}); err != nil {
			fails++
		}
	}
	out := newObj(env)
	setI64(env, out, "n", n)
	setI64(env, out, "fails", fails)
	delta(env, out, preA, preF)
	return out
}

func wrapFinPanicReport(env napi.Env, _ napi.CallbackInfo) napi.Value {
	out := newObj(env)
	setI64(env, out, "panics", wrapFinPanics.Load())
	return out
}

func refProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)
	v := arg(cb, 1)
	if v == nil {
		v = newObj(env) // napi_create_reference accepts objects/functions only
	}

	switch kind {
	case 0: // strong reference lifecycle
		ref, err := napi.CreateReference(env, v, 1)
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			c1, _ := napi.ReferenceRef(env, ref)
			c2, _ := napi.ReferenceUnref(env, ref)
			c3, _ := napi.ReferenceUnref(env, ref)
			c4, e4 := napi.ReferenceUnref(env, ref) // already 0 -> napi_generic_failure
			setI64(env, out, "ref1", int64(c1))
			setI64(env, out, "unref1", int64(c2))
			setI64(env, out, "unref2", int64(c3))
			setStr(env, out, "unref3", statusOf(e4))
			setI64(env, out, "unref3Count", int64(c4))
			got, _ := napi.GetReferenceValue(env, ref)
			setBool(env, out, "valueAlive", got != nil)
			setStr(env, out, "delete", statusOf(napi.DeleteReference(env, ref)))
		}
	case 1: // weak reference to an unreferenced object
		tmp := newObj(env)
		ref, err := napi.CreateReference(env, tmp, 0)
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			refs = append(refs, ref)
			setI64(env, out, "slot", int64(len(refs)-1))
		}
	case 2: // read a stored weak ref after GC
		idx := num(env, arg(cb, 2), 0)
		if idx >= 0 && int(idx) < len(refs) {
			got, err := napi.GetReferenceValue(env, refs[idx])
			setStr(env, out, "err", statusOf(err))
			setBool(env, out, "alive", got != nil)
		} else {
			setStr(env, out, "err", "bad slot")
		}
	case 3: // delete the same reference twice
		ref, err := napi.CreateReference(env, v, 1)
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "delete1", statusOf(napi.DeleteReference(env, ref)))
			setStr(env, out, "delete2", statusOf(napi.DeleteReference(env, ref)))
		}
	case 4: // use a reference after deleting it
		ref, err := napi.CreateReference(env, v, 1)
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			napi.DeleteReference(env, ref)
			_, uerr := napi.GetReferenceValue(env, ref)
			setStr(env, out, "useAfterDelete", statusOf(uerr))
		}
	}
	return out
}

var refs []napi.Ref

// ---- async work -------------------------------------------------------------

var (
	asyncState     atomic.Int64
	asyncExec      atomic.Int64
	asyncDone      atomic.Int64
	asyncDeleteErr atomic.Int64
	asyncPreA      uint64
	asyncPreF      uint64
)

func asyncWork(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)
	preA, preF := napi.DebugStats()

	switch kind {
	case 0: // create + delete without queueing
		w, err := napi.NewAsyncWork(env, "probe", func(napi.Env) {}, func(napi.Env, error) {})
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "delete", statusOf(w.Delete(env)))
		}
	case 1: // queue, then delete from complete
		var w *napi.AsyncWork
		var err error
		w, err = napi.NewAsyncWork(env, "probe", func(napi.Env) { asyncState.Add(1) },
			func(e napi.Env, _ error) {
				asyncState.Add(100)
				if w != nil {
					w.Delete(e)
				}
			})
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "queue", statusOf(w.Queue(env)))
		}
	case 2: // queue then delete once, before completion (Node's docs: illegal)
		w, err := napi.NewAsyncWork(env, "probe",
			func(napi.Env) { asyncState.Add(1) },
			func(napi.Env, error) { asyncState.Add(100) })
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "queue", statusOf(w.Queue(env)))
			setStr(env, out, "delete", statusOf(w.Delete(env)))
		}
	case 3: // delete twice without queueing
		w, err := napi.NewAsyncWork(env, "probe", func(napi.Env) {}, func(napi.Env, error) {})
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "delete1", statusOf(w.Delete(env)))
			setStr(env, out, "delete2", statusOf(w.Delete(env)))
		}
	case 4: // queue + cancel, delete in complete
		var w *napi.AsyncWork
		var err error
		w, err = napi.NewAsyncWork(env, "probe",
			func(napi.Env) { asyncState.Add(1) },
			func(e napi.Env, cerr error) {
				asyncState.Add(100)
				if cerr != nil {
					asyncState.Add(1000)
				}
				if w != nil {
					w.Delete(e)
				}
			})
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "queue", statusOf(w.Queue(env)))
			setStr(env, out, "cancel", statusOf(w.Cancel(env)))
		}
	case 5: // delete before ever queueing, then queue the dead handle
		w, err := napi.NewAsyncWork(env, "probe", func(napi.Env) {}, func(napi.Env, error) {})
		setStr(env, out, "create", statusOf(err))
		if err == nil {
			setStr(env, out, "delete", statusOf(w.Delete(env)))
			setStr(env, out, "queueAfterDelete", statusOf(w.Queue(env)))
		}
	}
	setI64(env, out, "asyncState", asyncState.Load())
	delta(env, out, preA, preF)
	return out
}

// asyncFull runs the canonical lifecycle: N works queued, each deleting
// itself from its completion callback, and reports afterwards.
func asyncFull(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 5)
	asyncPreA, asyncPreF = napi.DebugStats()
	asyncExec.Store(0)
	asyncDone.Store(0)
	out := newObj(env)
	fails := 0
	for i := int64(0); i < n; i++ {
		var w *napi.AsyncWork
		var err error
		w, err = napi.NewAsyncWork(env, "probe",
			func(napi.Env) { asyncExec.Add(1) },
			func(e napi.Env, _ error) {
				asyncDone.Add(1)
				if w != nil {
					if derr := w.Delete(e); derr != nil {
						asyncDeleteErr.Add(1)
					}
				}
			})
		if err != nil {
			fails++
			continue
		}
		if err := w.Queue(env); err != nil {
			fails++
		}
	}
	setI64(env, out, "queued", n)
	setI64(env, out, "fails", int64(fails))
	return out
}

func asyncReport(env napi.Env, info napi.CallbackInfo) napi.Value {
	a, f := napi.DebugStats()
	out := newObj(env)
	setI64(env, out, "exec", asyncExec.Load())
	setI64(env, out, "done", asyncDone.Load())
	setI64(env, out, "deleteErr", asyncDeleteErr.Load())
	setI64(env, out, "allocDelta", int64(a-asyncPreA))
	setI64(env, out, "freeDelta", int64(f-asyncPreF))
	return out
}

// ---- cleanup hooks ----------------------------------------------------------

var hookState atomic.Int64

func removableHook(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	remove := boolArg(env, arg(cb, 0), false)
	out := newObj(env)
	preA, preF := napi.DebugStats()
	tok, err := napi.AddEnvCleanupHookRemovable(env, func() { hookState.Add(1) })
	setStr(env, out, "add", statusOf(err))
	if err == nil && remove {
		setStr(env, out, "remove", statusOf(napi.RemoveEnvCleanupHook(tok)))
		setStr(env, out, "removeAgain", statusOf(napi.RemoveEnvCleanupHook(tok)))
	}
	delta(env, out, preA, preF)
	return out
}

// asyncHookProbe exercises the async cleanup hook lifecycle. The engine
// implements napi_remove_async_cleanup_hook as a bare `delete handle`, so the
// interesting cases are the ones where Go hands it the same handle twice.
func asyncHookProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	k := num(env, arg(cb, 0), 0)
	out := newObj(env)
	preA, preF := napi.DebugStats()
	noop := func(napi.AsyncCleanupHookHandle) { hookState.Add(1) }

	switch k {
	case 0: // add then remove once
		h, err := napi.AddAsyncCleanupHook(env, noop)
		setStr(env, out, "add", statusOf(err))
		if err == nil {
			setStr(env, out, "remove1", statusOf(napi.RemoveAsyncCleanupHook(h)))
		}
	case 1: // add, remove, then remove the same handle again
		h, err := napi.AddAsyncCleanupHook(env, noop)
		setStr(env, out, "add", statusOf(err))
		if err == nil {
			setStr(env, out, "remove1", statusOf(napi.RemoveAsyncCleanupHook(h)))
			setStr(env, out, "remove2", statusOf(napi.RemoveAsyncCleanupHook(h)))
		}
	case 2: // remove with no handle at all
		setStr(env, out, "removeNil", statusOf(napi.RemoveAsyncCleanupHook(nil)))
	case 3: // add/remove repeatedly: does the allocator hand back the same handle?
		var seen []napi.AsyncCleanupHookHandle
		var firstErr error
		for i := 0; i < 6; i++ {
			h, err := napi.AddAsyncCleanupHook(env, noop)
			if err != nil {
				firstErr = err
				break
			}
			seen = append(seen, h)
			if rerr := napi.RemoveAsyncCleanupHook(h); rerr != nil {
				firstErr = rerr
				break
			}
		}
		setStr(env, out, "err", statusOf(firstErr))
		setI64(env, out, "cycles", int64(len(seen)))
		reused := 0
		for i := 1; i < len(seen); i++ {
			if seen[i] == seen[i-1] {
				reused++
			}
		}
		setI64(env, out, "reused", int64(reused))
	case 4: // add and never remove: the hook must fire during env teardown
		h, err := napi.AddAsyncCleanupHook(env, func(handle napi.AsyncCleanupHookHandle) {
			hookState.Add(1)
			// The hook runs while the process is exiting, so a marker on
			// stderr is the only way the driver can observe that it fired.
			fmt.Fprintln(os.Stderr, "PROBE_ASYNC_HOOK_FIRED")
			// Contract: the handler must close the handshake itself.
			if rerr := napi.RemoveAsyncCleanupHook(handle); rerr != nil {
				fmt.Fprintf(os.Stderr, "PROBE_ASYNC_HOOK_REMOVE_ERR %v\n", rerr)
			}
		})
		setStr(env, out, "add", statusOf(err))
		setStr(env, out, "handle", fmt.Sprintf("%v", h != nil))
	case 5: // control: fire the hook but never close the handshake
		// The engine only releases the environment's pending-request counter
		// from the handle's destructor, so this must hang process exit. The
		// driver asserts the timeout; if it ever exits cleanly, the contract
		// documented on AsyncCleanupHookFunc is wrong.
		h, err := napi.AddAsyncCleanupHook(env, func(napi.AsyncCleanupHookHandle) {
			fmt.Fprintln(os.Stderr, "PROBE_ASYNC_HOOK_FIRED_NO_REMOVE")
		})
		setStr(env, out, "add", statusOf(err))
		setStr(env, out, "handle", fmt.Sprintf("%v", h != nil))
	}
	delta(env, out, preA, preF)
	return out
}

// ---- external values --------------------------------------------------------

func externalProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)
	preA, preF := napi.DebugStats()
	switch kind {
	case 0: // external with payload + finalizer
		v, err := napi.CreateExternal(env, map[string]int{"k": 1}, func(napi.Env, any) {})
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "value", v)
		if v != nil {
			got, gerr := napi.GetValueExternal(env, v)
			setStr(env, out, "getErr", statusOf(gerr))
			setStr(env, out, "payload", fmt.Sprintf("%v", got))
			// GetValueExternal on a non-external value must fail.
			_, nerr := napi.GetValueExternal(env, newObj(env))
			setStr(env, out, "nonExternalErr", statusOf(nerr))
		}
	case 1: // zero-length external array buffer
		ab, err := napi.CreateExternalArrayBuffer(env, nil, 0, nil, nil)
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "value", ab)
		if ab != nil {
			inf, ierr := napi.GetArrayBufferInfo(env, ab)
			setStr(env, out, "infoErr", statusOf(ierr))
			setI64(env, out, "dataLen", int64(len(inf.Data)))
		}
	case 2: // zero-length Buffer + empty copy
		b, _, err := napi.CreateBuffer(env, 0)
		setStr(env, out, "bufErr", statusOf(err))
		setVal(env, out, "buffer", b)
		cp, cerr := napi.CreateBufferCopy(env, nil)
		setStr(env, out, "copyErr", statusOf(cerr))
		setVal(env, out, "copy", cp)
	case 3: // external Buffer rejection path (may be unsupported)
		raw, err := rawMalloc(4)
		setStr(env, out, "mallocErr", statusOf(err))
		if err == nil {
			b, berr := napi.CreateExternalBuffer(env, raw, 4, nil, nil)
			setStr(env, out, "err", statusOf(berr))
			setVal(env, out, "value", b)
			if berr != nil {
				rawFree(raw)
			}
		}
	}
	delta(env, out, preA, preF)
	return out
}

// ---- js convenience layer ---------------------------------------------------

func jsProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)

	safe := func(fn func() napi.Value) {
		defer func() {
			if r := recover(); r != nil {
				setStr(env, out, "panic", fmt.Sprintf("%v", r))
			}
		}()
		v := fn()
		if v != nil {
			setVal(env, out, "value", v)
		}
	}

	switch kind {
	case 0: // Keys() on a number
		safe(func() napi.Value {
			v := js.ValueOf(env, 42)
			keys := v.Keys()
			a, _ := napi.CreateArrayWithLength(env, len(keys))
			for i, k := range keys {
				s, _ := napi.CreateStringUtf8(env, k)
				napi.SetElement(env, a, uint32(i), s)
			}
			return a
		})
	case 1: // Index/Len on a string
		safe(func() napi.Value {
			s := js.ValueOf(env, "hello")
			setI64(env, out, "strLen", int64(s.Len()))
			return s.Raw()
		})
	case 2: // ValueOf on an unsupported type
		safe(func() napi.Value { return js.ValueOf(env, make(chan int)).Raw() })
	case 3: // ValueOf on a non-[]any slice
		safe(func() napi.Value { return js.ValueOf(env, []int{1, 2, 3}).Raw() })
	case 4: // ValueOf uint64 beyond 2^53
		safe(func() napi.Value { return js.ValueOf(env, uint64(1)<<60+1).Raw() })
	case 5: // Ref double release
		safe(func() napi.Value {
			obj := js.ValueOf(env, map[string]any{"x": 1})
			r := obj.MakeRef()
			r.Release()
			r.Release()
			return nil
		})
	case 6: // Ref.Value() after Release
		safe(func() napi.Value {
			obj := js.ValueOf(env, map[string]any{"x": 1})
			r := obj.MakeRef()
			r.Release()
			return r.Value().Raw()
		})
	case 7: // Index(-1) and out of range
		safe(func() napi.Value {
			arr := js.ValueOf(env, []any{1, 2, 3})
			a := arr.Index(-1)
			setBool(env, out, "negIsUndefined", a.IsUndefined())
			b := arr.Index(99)
			setBool(env, out, "oobIsUndefined", b.IsUndefined())
			return arr.Raw()
		})
	case 8: // Call a missing method
		safe(func() napi.Value {
			o := js.ValueOf(env, map[string]any{})
			return o.Call("nope").Raw()
		})
	case 9: // Invoke a non-function
		safe(func() napi.Value {
			n := js.ValueOf(env, 3)
			return n.Invoke().Raw()
		})
	case 10: // zero js.Value
		safe(func() napi.Value {
			var z js.Value
			return z.Raw()
		})
	case 11: // native wrapper: Undefined().Get returns undefined
		safe(func() napi.Value {
			u := js.Undefined(env)
			g := u.Get("anything")
			setBool(env, out, "undefinedProp", g.IsUndefined())
			return u.Raw()
		})
	case 12: // js.Wrap + String() on a number -> must panic (string expected)
		safe(func() napi.Value {
			n := js.ValueOf(env, 3.5)
			_ = n.String()
			return nil
		})
	case 13: // FuncOf round-trip through JS-land
		safe(func() napi.Value {
			return js.FuncOf(env, func(this js.Value, args []js.Value) any {
				if len(args) < 1 {
					return "no args"
				}
				return "got:" + args[0].String()
			}).Raw()
		})
	case 14: // Int() truncation of a float
		safe(func() napi.Value {
			f := js.ValueOf(env, 3.9)
			i := f.Int()
			setI64(env, out, "int", int64(i))
			return f.Raw()
		})
	case 15: // map with unsupported value type
		safe(func() napi.Value {
			return js.ValueOf(env, map[string]any{"bad": make(chan int)}).Raw()
		})
	}
	setStr(env, out, "kind", fmt.Sprintf("%d", kind))
	return out
}

// ---- misc -------------------------------------------------------------------

func bufferCopyEmpty(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	v, err := napi.CreateBufferCopy(env, []byte{})
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "value", v)
	cp, cerr := napi.CreateBufferCopy(env, []byte{1, 2, 3})
	setStr(env, out, "copyErr", statusOf(cerr))
	setVal(env, out, "copy", cp)
	return out
}

func promiseProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	mode := num(env, arg(cb, 0), 0)
	out := newObj(env)
	p, d, err := napi.CreatePromise(env)
	setStr(env, out, "create", statusOf(err))
	if err != nil {
		return out
	}
	switch mode {
	case 0:
		v, _ := napi.CreateInt32(env, 9)
		setStr(env, out, "resolve", statusOf(napi.ResolveDeferred(env, d, v)))
		setStr(env, out, "resolveAgain", statusOf(napi.ResolveDeferred(env, d, v)))
	case 1:
		e, _ := napi.CreateError(env, nil, mustStr(env, "nope"))
		setStr(env, out, "reject", statusOf(napi.RejectDeferred(env, d, e)))
	}
	isP, _ := napi.IsPromise(env, p)
	setBool(env, out, "isPromise", isP)
	setVal(env, out, "promise", p)
	return out
}

func mustStr(env napi.Env, s string) napi.Value {
	v, _ := napi.CreateStringUtf8(env, s)
	return v
}

func extErrInfo(env napi.Env, info napi.CallbackInfo) napi.Value {
	// Force a failure, then read the extended error info IMMEDIATELY: the
	// engine resets it to napi_ok on the next successful call, and creating a
	// report object is a successful call. Reading it late is how this probe
	// used to "pass" while reporting nothing.
	_, err := napi.GetValueStringUtf8(env, mustInt32(env, 1))
	fail := statusOf(err)
	errText := ""
	if err != nil {
		errText = err.Error() // errOf captured the engine message at failure time
	}
	msg, code, eerr := napi.GetExtendedErrorInfo(env)

	out := newObj(env)
	setStr(env, out, "fail", fail)
	setStr(env, out, "errText", errText)
	setStr(env, out, "err", statusOf(eerr))
	setStr(env, out, "msg", msg)
	setStr(env, out, "code", code.String())
	return out
}

func mustInt32(env napi.Env, v int32) napi.Value {
	x, _ := napi.CreateInt32(env, v)
	return x
}

func versionProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	v, err := napi.GetVersion(env)
	setStr(env, out, "err", statusOf(err))
	setI64(env, out, "version", int64(v))
	nv, nerr := napi.GetNodeVersion(env)
	setStr(env, out, "nodeErr", statusOf(nerr))
	setStr(env, out, "node", fmt.Sprintf("%d.%d.%d %s", nv.Major, nv.Minor, nv.Patch, nv.Release))
	loop, lerr := napi.GetUVEventLoop(env)
	setStr(env, out, "loopErr", statusOf(lerr))
	setBool(env, out, "loopNonNil", loop != nil)
	return out
}

// rawProbe calls Node-API directly (bypassing the wrappers) to settle
// questions about the engine's own contract.
func rawProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	mode := num(env, arg(cb, 1), 0)
	out := newObj(env)
	switch kind {
	case 0: // napi_get_value_bigint_words in three call patterns
		var v napi.Value
		switch mode {
		case 0:
			v, _ = napi.CreateBigIntInt64(env, 0)
		case 1:
			v, _ = napi.CreateBigIntInt64(env, 1)
		case 2:
			v, _ = napi.CreateBigIntInt64(env, -1)
		case 3:
			v, _ = napi.CreateBigIntUint64(env, 18446744073709551615)
		case 4:
			v, _ = napi.CreateBigIntWords(env, 0, []uint64{0, 1}) // 2^64
		}
		q, qs, rd, words, sign := rawBigintWords(env, v)
		setStr(env, out, "query(nil words)", q)
		setStr(env, out, "query(sign, nil words)", qs)
		setStr(env, out, "read(sign, words)", rd)
		setI64(env, out, "sign", int64(sign))
		setI64(env, out, "nwords", int64(len(words)))
	case 1: // napi_create_reference on this kind of value
		var v napi.Value
		switch mode {
		case 0:
			v, _ = napi.CreateInt32(env, 5)
		case 1:
			v, _ = napi.CreateStringUtf8(env, "s")
		case 2:
			v, _ = napi.GetNull(env)
		case 3:
			v = newObj(env)
		case 4:
			v, _ = napi.CreateFunction(env, "f", func(napi.Env, napi.CallbackInfo) napi.Value { return nil })
		}
		setStr(env, out, "createReference", rawCreateReference(env, v, 1))
	case 2: // napi_call_function with a NULL recv
		fn, _ := napi.CreateFunction(env, "f", func(napi.Env, napi.CallbackInfo) napi.Value { return nil })
		setStr(env, out, "callNullRecv", rawCallNullRecv(env, fn))
	case 3: // napi_set_instance_data twice
		setStr(env, out, "set1", rawSetInstanceData(env, nil))
		setStr(env, out, "set2", rawSetInstanceData(env, nil))
	}
	return out
}

// weakTarget builds an object by one of the four lifecycle mechanisms and
// hands it to JS so the driver can wrap it in a WeakRef. That separates "the
// engine never collected the object" from "the binding's finalizer never ran".
func weakTarget(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind := num(env, arg(cb, 0), 0)
	out := newObj(env)
	preA, preF := napi.DebugStats()
	switch kind {
	case 0: // DefineProperties on a plain object
		o := newObj(env)
		napi.DefineProperties(env, o, []napi.PropertyDescriptor{{Name: "m", Method: mkPropFn}})
		setVal(env, out, "target", o)
	case 1: // DefineClass
		c, err := napi.DefineClass(env, napi.ClassDescriptor{
			Name: "C", Constructor: mkPropFn,
			Properties: []napi.PropertyDescriptor{{Name: "m", Method: mkPropFn}},
		})
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "target", c)
	case 2: // CreateFunction
		f, err := napi.CreateFunction(env, "f", mkPropFn)
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "target", f)
	case 3: // Wrap on a plain object
		o := newObj(env)
		setStr(env, out, "err", statusOf(napi.Wrap(env, o, 1, func(napi.Env, any) { wrapFins.Add(1) })))
		setVal(env, out, "target", o)
	case 4: // AddFinalizer on a plain object
		o := newObj(env)
		setStr(env, out, "err", statusOf(napi.AddFinalizer(env, o, "x", func(napi.Env, any) { wrapFins.Add(1) })))
		setVal(env, out, "target", o)
	case 5: // CreateExternal
		v, err := napi.CreateExternal(env, "payload", func(napi.Env, any) { wrapFins.Add(1) })
		setStr(env, out, "err", statusOf(err))
		setVal(env, out, "target", v)
	}
	delta(env, out, preA, preF)
	return out
}

// classChurn creates n classes that are never referenced again.
func classChurn(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 100)
	preA, preF := napi.DebugStats()
	for i := int64(0); i < n; i++ {
		napi.DefineClass(env, napi.ClassDescriptor{
			Name: "C", Constructor: mkPropFn,
			Properties: []napi.PropertyDescriptor{{Name: "m", Method: mkPropFn}},
		})
	}
	out := newObj(env)
	setI64(env, out, "n", n)
	delta(env, out, preA, preF)
	return out
}

// propChurn is the DefineProperties control for classChurn.
func propChurn(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 100)
	preA, preF := napi.DebugStats()
	for i := int64(0); i < n; i++ {
		o := newObj(env)
		napi.DefineProperties(env, o, []napi.PropertyDescriptor{{Name: "m", Method: mkPropFn}})
	}
	out := newObj(env)
	setI64(env, out, "n", n)
	delta(env, out, preA, preF)
	return out
}

// rawCls hands JS a class built by raw napi_define_class with no properties
// and no finalizer, i.e. with none of the binding's bookkeeping. The driver
// wraps it in a WeakRef and forces a collection.
func rawCls(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	preA, preF := napi.DebugStats()
	c, err := rawDefineClass(env)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "target", c)
	delta(env, out, preA, preF)
	return out
}

func statsProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	boxCounters(env, out)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	setI64(env, out, "goHeapBytes", int64(ms.HeapAlloc))
	setI64(env, out, "goSysBytes", int64(ms.Sys))
	setI64(env, out, "goroutines", int64(runtime.NumGoroutine()))
	return out
}

func rawMallocProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	p, err := rawMalloc(16)
	setStr(env, out, "err", statusOf(err))
	if err == nil {
		setStr(env, out, "nonNil", fmt.Sprintf("%v", p != nil))
		rawFree(p)
	}
	return out
}

// freeGo returns Go heap memory to the OS. Leak checks call it between
// measurements so that RSS reflects retained memory instead of Go's garbage
// high-water mark (20k panics with 1KB messages would otherwise look like
// ~10MB of leakage even when nothing leaks).
func freeGo(env napi.Env, info napi.CallbackInfo) napi.Value {
	runtime.GC()
	debug.FreeOSMemory()
	out, _ := napi.CreateObject(env)
	return out
}

// ===================================================== threadsafe functions ==
//
// The engine destroys a ThreadSafeFunction with a plain `delete this` the
// moment the last thread releases it. node_api.cc Release():
//
//	if (!(state == kClosed && thread_count == 0)) { return napi_ok; }
//	break out of the lock;
//	delete this;                     // <- the object is gone
//
// Push() has the same escape hatch. What comes back to Go is a raw pointer
// with no lifetime attached, and — unlike AsyncWork, references, deferreds,
// handle scopes and async contexts, all of which were given a liveness set
// precisely because the engine validates none of them — the TSFN handle is
// tracked nowhere. Each case below is ordinary-looking Go that hands the
// engine a handle it may already have freed.

var tsfnCalls atomic.Int64

// tsfnHeld carries a live handle between two tsfnProbe calls, so the driver can
// let the event loop turn in between — which is when the engine's own state
// advances past anything the binding can observe.
var tsfnHeld napi.ThreadsafeFunction

func tsfnTarget(_ napi.Env, _ napi.CallbackInfo) napi.Value {
	tsfnCalls.Add(1)
	return nil
}

func tsfnProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	k := num(env, arg(cb, 0), 0)
	out := newObj(env)

	mk := func() (napi.ThreadsafeFunction, error) {
		fn, err := napi.CreateFunction(env, "probeTsfnTarget", tsfnTarget)
		if err != nil {
			return napi.ThreadsafeFunction{}, err
		}
		return napi.NewThreadsafeFunction(env, fn, 0, 1)
	}
	noop := func(napi.Env, napi.Value) {}

	// report runs a step and records its status without ever letting a Go
	// error abort the remaining steps: the point is to reach the bad call.
	report := func(name string, err error) {
		setStr(env, out, name, statusOf(err))
	}

	switch k {
	case 0: // baseline: one clean release
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
	case 1: // release twice — the second call talks to deleted memory
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		report("release2", f.Release(napi.Release))
	case 2: // abort, then abort again
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Abort))
		report("release2", f.Release(napi.Abort))
	case 3: // call after release
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		report("call", f.Call(noop, napi.NonBlocking))
	case 4: // call after abort
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Abort))
		report("call", f.Call(noop, napi.NonBlocking))
	case 5: // acquire after release
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		report("acquire", f.Acquire())
	case 6: // unref / ref after release
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		report("unref", f.Unref(env))
		report("ref", f.Ref(env))
	case 7: // GetContext after release
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		_, cerr := f.GetContext()
		report("context", cerr)
	case 8: // release more times than it was acquired
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		for i := 0; i < 4; i++ {
			report(fmt.Sprintf("release%d", i+1), f.Release(napi.Release))
		}
	case 9: // allocator hands A's freed storage to B, then A is used again
		a, err := mk()
		if err != nil {
			report("createA", err)
			break
		}
		report("releaseA1", a.Release(napi.Release)) // engine deletes A's object
		b, err := mk()                               // operator new very likely returns that very block
		if err != nil {
			report("createB", err)
			break
		}
		report("releaseA2", a.Release(napi.Release)) // <- aimed at B
		report("releaseB1", b.Release(napi.Release))
		report("releaseB2", b.Release(napi.Release))
	case 10: // churn the allocator with same-sized live objects, then poke the dead one
		a, err := mk()
		if err != nil {
			report("createA", err)
			break
		}
		report("releaseA1", a.Release(napi.Release))
		kept := 0
		for i := 0; i < 64; i++ {
			f, ferr := mk()
			if ferr != nil {
				break
			}
			// Release each one so the churn does not leave live thread counts
			// behind: an unreleased TSFN pins the environment, which would
			// show up as a teardown hang and mask what this case is asking.
			_ = f.Release(napi.Release)
			kept++
		}
		report("releaseA2", a.Release(napi.Release))
		setI64(env, out, "kept", int64(kept))
	case 11: // create and never release: thread_count stays 1
		// Node-API requires every Acquire to be matched by a Release. The
		// question this case answers is what the engine does to the *process*
		// when that contract is broken — the binding cannot repair it, but it
		// has to be able to describe it.
		f, err := mk()
		report("create", err)
		setStr(env, out, "created", fmt.Sprintf("%v", f != (napi.ThreadsafeFunction{})))
	case 12: // created with 2 threads, only one released
		fn, ferr := napi.CreateFunction(env, "probeTsfnTarget", tsfnTarget)
		if ferr != nil {
			report("createFn", ferr)
			break
		}
		f, err := napi.NewThreadsafeFunction(env, fn, 0, 2)
		report("create", err)
		if err == nil {
			report("release1", f.Release(napi.Release))
		}
	case 13: // release -> acquire -> release: back to zero, must be able to exit
		f, err := mk()
		if err != nil {
			report("create", err)
			break
		}
		report("release1", f.Release(napi.Release))
		report("acquire", f.Acquire())
		report("release2", f.Release(napi.Release))
	case 14: // abort while a thread is still acquired: 2 threads -> 1, state closing
		//
		// The engine does not free the object here. Aborting only moves it to
		// kClosing and leaves thread_count at 1, so the handle Go still holds
		// points at live memory. What happens to that memory on the next call
		// is the subject of case 15.
		fn, ferr := napi.CreateFunction(env, "probeTsfnTarget", tsfnTarget)
		if ferr != nil {
			report("createFn", ferr)
			break
		}
		f, err := napi.NewThreadsafeFunction(env, fn, 0, 2)
		report("create", err)
		if err != nil {
			break
		}
		tsfnHeld = f
		report("abort", f.Release(napi.Abort))
	case 15: // one stray call on the closing function, then the matching release
		//
		// node_api.cc ThreadSafeFunction::Push, when state != kOpen, does not
		// merely reject the call: it decrements thread_count and, if that makes
		// the object (kClosed, 0), runs `delete this` on the calling thread.
		// One call therefore frees the object the binding still reports live,
		// and the release that follows — the correct way to let a closing
		// function finish — talks to freed memory.
		f := tsfnHeld
		cerr := f.Call(noop, napi.NonBlocking)
		report("call", cerr)
		setStr(env, out, "callText", errText(cerr))
		rerr := f.Release(napi.Release)
		report("release", rerr)
		setStr(env, out, "releaseText", errText(rerr))
	case 16: // the follow-up call on its own
		report("call", tsfnHeld.Call(noop, napi.NonBlocking))
	case 17: // the follow-up release on its own
		rerr := tsfnHeld.Release(napi.Release)
		report("release", rerr)
		setStr(env, out, "releaseText", errText(rerr))
	case 18: // abort, then release the remaining thread with no loop turn between
		fn, ferr := napi.CreateFunction(env, "probeTsfnTarget", tsfnTarget)
		if ferr != nil {
			report("createFn", ferr)
			break
		}
		f, err := napi.NewThreadsafeFunction(env, fn, 0, 2)
		report("create", err)
		if err != nil {
			break
		}
		report("abort", f.Release(napi.Abort))
		report("release", f.Release(napi.Release))
	}
	setI64(env, out, "tsfnCalls", tsfnCalls.Load())
	return out
}

// ================================================================ cross-GC ==
//
// Two collectors, two heaps, no shared root set. Go's GC traces Go references
// only; V8's traces V8 references only. A raw pointer crossing the boundary is
// invisible to whichever collector actually owns the memory, so either side
// can recycle storage the other still believes it holds. Each case isolates one
// direction of that blindness.

var (
	staleValue napi.Value // a V8 handle kept past the scope that created it
	staleSlice []byte     // V8-owned memory aliased into the Go heap
)

// saveStale stores a callback argument in the Go heap. The argument is a V8
// handle: it is only valid until the engine closes the handle scope around this
// callback, which happens the moment we return.
func saveStale(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	staleValue = arg(cb, 0)
	out := newObj(env)
	setBool(env, out, "saved", staleValue != nil)
	return out
}

// useStale dereferences the handle saved by saveStale, long after its scope died.
func useStale(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	if staleValue == nil {
		setStr(env, out, "err", "nothing saved")
		return out
	}
	s, serr := napi.GetValueStringUtf8(env, staleValue)
	setStr(env, out, "str", s)
	setStr(env, out, "strErr", statusOf(serr))
	t, terr := napi.Typeof(env, staleValue)
	setI64(env, out, "type", int64(t))
	setStr(env, out, "typeErr", statusOf(terr))
	return out
}

// goMemNoPin wraps a Go slice as an external ArrayBuffer and passes no
// finalData. Nothing carries the slice across the boundary, so Go's collector
// is free to recycle memory V8 still points at.
func goMemNoPin(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1<<20)
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xAA
	}
	ab, err := napi.CreateExternalArrayBuffer(env, unsafe.Pointer(&b[0]), uint64(len(b)), nil, nil)
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "buffer", ab)
	return out // b is unreachable from here on
}

// goMemPinned is the same call made through the safe wrapper: the slice rides
// in finalData by construction, so nothing has to remember to pin it. If this
// case ever regresses, CreateExternalArrayBufferFromBytes has stopped pinning.
func goMemPinned(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1<<20)
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xAA
	}
	ab, err := napi.CreateExternalArrayBufferFromBytes(env, b, nil)
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "buffer", ab)
	return out
}

// grabV8Mem allocates an ArrayBuffer, paints it, and stashes the alias in the
// Go heap. V8 has no notion that Go holds a pointer, so collecting the
// ArrayBuffer frees memory the Go side will keep reading.
func grabV8Mem(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1<<20)
	ab, data, err := napi.CreateArrayBuffer(env, uint64(n))
	out := newObj(env)
	setStr(env, out, "err", statusOf(err))
	if err != nil {
		return out
	}
	for i := range data {
		data[i] = 0xBB
	}
	staleSlice = data
	setVal(env, out, "buffer", ab)
	setI64(env, out, "len", int64(len(data)))
	return out
}

// readV8Mem reads the alias kept by grabV8Mem.
func readV8Mem(env napi.Env, info napi.CallbackInfo) napi.Value {
	out := newObj(env)
	if len(staleSlice) == 0 {
		setStr(env, out, "err", "nothing grabbed")
		return out
	}
	setI64(env, out, "first", int64(staleSlice[0]))
	setI64(env, out, "mid", int64(staleSlice[len(staleSlice)/2]))
	setI64(env, out, "last", int64(staleSlice[len(staleSlice)-1]))
	return out
}

// dropCrossGc forgets both aliases so a later phase can start clean.
func dropCrossGc(env napi.Env, info napi.CallbackInfo) napi.Value {
	staleSlice = nil
	staleValue = nil
	return newObj(env)
}

// externalMemoryReport asks V8 how much external memory it is tracking, makes
// an external ArrayBuffer, and asks again. Nothing in the binding calls
// napi_adjust_external_memory on that path, so the delta tells us whether V8
// knows the memory it is nominally holding exists at all.
func externalMemoryReport(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n := num(env, arg(cb, 0), 1<<20)
	out := newObj(env)
	before, _ := napi.AdjustExternalMemory(env, 0)
	b := make([]byte, n)
	ab, err := napi.CreateExternalArrayBuffer(env, unsafe.Pointer(&b[0]), uint64(len(b)), nil, b)
	setStr(env, out, "err", statusOf(err))
	setVal(env, out, "buffer", ab)
	after, _ := napi.AdjustExternalMemory(env, 0)
	setI64(env, out, "before", before)
	setI64(env, out, "after", after)
	setI64(env, out, "allocated", n)
	setI64(env, out, "delta", after-before)
	return out
}

// refAliasProbe checks the invariant that makes the liveness sets worth having:
// an operation on a released handle must never be accepted, and must never harm
// a live one. Each round releases a reference, allocates another, then tries to
// delete through the dead handle.
//
// `recycled` counts the rounds where the allocator handed the released
// *identity* back — the mechanism that used to defeat the guard (28 of 64 rounds
// with raw engine pointers, every one of them accepted). With tokens it is
// structurally zero, and the invariant is checked every round regardless.
func refAliasProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	const rounds = 64
	out := newObj(env)
	objA, _ := napi.CreateObject(env)
	objB, _ := napi.CreateObject(env)

	var recycled, staleAccepted, liveBroken int
	for i := 0; i < rounds; i++ {
		refA, errA := napi.CreateReference(env, objA, 1)
		if errA != nil {
			break
		}
		addrA := fmt.Sprintf("%p", refA)
		if err := napi.DeleteReference(env, refA); err != nil {
			break
		}

		refB, errB := napi.CreateReference(env, objB, 1)
		if errB != nil {
			break
		}
		if fmt.Sprintf("%p", refB) == addrA {
			recycled++
		}

		// The interesting call: delete through the stale handle. It must be
		// refused; if it is accepted, B has just been destroyed.
		if napi.DeleteReference(env, refA) == nil {
			staleAccepted++
		}
		if _, verr := napi.GetReferenceValue(env, refB); verr != nil {
			liveBroken++
		}
		_ = napi.DeleteReference(env, refB)
	}
	setI64(env, out, "rounds", rounds)
	setI64(env, out, "recycled", int64(recycled))
	setI64(env, out, "staleAccepted", int64(staleAccepted))
	setI64(env, out, "liveRefDestroyed", int64(liveBroken))
	return out
}

// scopeAliasProbe is the same question for handle scopes: open one, close it,
// open another, then close through the stale handle.
func scopeAliasProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	const rounds = 64
	out := newObj(env)
	var recycled, staleAccepted, liveClosed int
	for i := 0; i < rounds; i++ {
		s1, err1 := napi.OpenHandleScope(env)
		if err1 != nil || s1 == nil {
			break
		}
		addr1 := fmt.Sprintf("%p", s1)
		if err := napi.CloseHandleScope(env, s1); err != nil {
			break
		}

		s2, err2 := napi.OpenHandleScope(env)
		if err2 != nil || s2 == nil {
			break
		}
		if fmt.Sprintf("%p", s2) == addr1 {
			recycled++
		}

		if napi.CloseHandleScope(env, s1) == nil {
			staleAccepted++
		}
		// If the stale close was accepted it closed the live scope, so this
		// close has nothing left to close.
		if napi.CloseHandleScope(env, s2) != nil {
			liveClosed++
		}
	}
	setI64(env, out, "rounds", rounds)
	setI64(env, out, "recycled", int64(recycled))
	setI64(env, out, "staleAccepted", int64(staleAccepted))
	setI64(env, out, "liveScopeClosed", int64(liveClosed))
	return out
}

// deferredAliasProbe runs the same question against promises. The consequence
// is worse than a crash: settling through a stale deferred that aliased a live
// one resolves the *wrong* promise and leaves the right one pending forever —
// a silent wrong answer rather than a fault.
func deferredAliasProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	const rounds = 64
	out := newObj(env)
	// Two distinct resolutions, so "the live promise was settled by a handle to
	// a dead one" is observable as a value, not just as a status.
	resA, _ := napi.CreateObject(env)
	resB, _ := napi.CreateObject(env)
	var recycled, staleAccepted, liveBroken int
	for i := 0; i < rounds; i++ {
		_, dA, errA := napi.CreatePromise(env)
		if errA != nil {
			break
		}
		addrA := fmt.Sprintf("%p", dA)
		if err := napi.ResolveDeferred(env, dA, resA); err != nil {
			break
		}

		_, dB, errB := napi.CreatePromise(env)
		if errB != nil {
			break
		}
		if fmt.Sprintf("%p", dB) == addrA {
			recycled++
		}

		// A stale settle aimed at the live deferred. If it is accepted, B's
		// promise has just been settled by a handle to a dead one.
		if napi.ResolveDeferred(env, dA, resB) == nil {
			staleAccepted++
		}
		// The live deferred must still be settleable. (Resolving rather than
		// rejecting on purpose: 64 rejected promises with no handler would end
		// the process as an unhandled rejection and mask the result.)
		if err := napi.ResolveDeferred(env, dB, resB); err != nil {
			liveBroken++
		}
	}
	setI64(env, out, "rounds", rounds)
	setI64(env, out, "recycled", int64(recycled))
	setI64(env, out, "staleAccepted", int64(staleAccepted))
	setI64(env, out, "liveDeferredSettled", int64(liveBroken))
	return out
}

// asyncCtxAliasProbe is the third instance of the same pattern: async contexts
// are `new node_async_context` on the engine side and a raw pointer here.
func asyncCtxAliasProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	const rounds = 64
	out := newObj(env)
	res, _ := napi.CreateObject(env)
	var recycled, staleAccepted, liveBroken int
	for i := 0; i < rounds; i++ {
		c1, err1 := napi.AsyncInit(env, res, "probe:async-context")
		if err1 != nil || c1 == nil {
			break
		}
		addr1 := fmt.Sprintf("%p", c1)
		if err := napi.AsyncDestroy(env, c1); err != nil {
			break
		}

		c2, err2 := napi.AsyncInit(env, res, "probe:async-context")
		if err2 != nil || c2 == nil {
			break
		}
		if fmt.Sprintf("%p", c2) == addr1 {
			recycled++
		}

		if napi.AsyncDestroy(env, c1) == nil {
			staleAccepted++
		}
		if err := napi.AsyncDestroy(env, c2); err != nil {
			liveBroken++
		}
	}
	setI64(env, out, "rounds", rounds)
	setI64(env, out, "recycled", int64(recycled))
	setI64(env, out, "staleAccepted", int64(staleAccepted))
	setI64(env, out, "liveContextDestroyed", int64(liveBroken))
	return out
}

// rawTsfnProbe drives the threadsafe-function sequence with no Go-side
// bookkeeping at all: raw create / acquire / call / release through bridge.go.
// A negative phase means "create two threads, abort, release the rest" back to
// back; otherwise it runs the single numbered step so the driver can let the
// event loop turn in between.
//
// Its only purpose is attribution. If the binding-free sequence dies where the
// binding's does, the engine is what cannot survive it.
func rawTsfnProbe(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	phase := int(num(env, arg(cb, 0), 0))
	out := newObj(env)

	fn, err := napi.CreateFunction(env, "rawTsfnTarget", tsfnTarget)
	if err != nil {
		setStr(env, out, "fnErr", err.Error())
		return out
	}
	if phase < 0 {
		setStr(env, out, "create", rawTsfnStep(env, fn, 0))
		setStr(env, out, "abort", rawTsfnStep(env, fn, 4))
		setStr(env, out, "release", rawTsfnStep(env, fn, 3))
		return out
	}
	setStr(env, out, "status", rawTsfnStep(env, fn, phase))
	return out
}

func init() {
	entry.Export("rawProbe", rawProbe)
	entry.Export("tsfnProbe", tsfnProbe)
	entry.Export("rawTsfnProbe", rawTsfnProbe)
	entry.Export("saveStale", saveStale)
	entry.Export("useStale", useStale)
	entry.Export("goMemNoPin", goMemNoPin)
	entry.Export("goMemPinned", goMemPinned)
	entry.Export("grabV8Mem", grabV8Mem)
	entry.Export("readV8Mem", readV8Mem)
	entry.Export("dropCrossGc", dropCrossGc)
	entry.Export("externalMemoryReport", externalMemoryReport)
	entry.Export("refAliasProbe", refAliasProbe)
	entry.Export("scopeAliasProbe", scopeAliasProbe)
	entry.Export("deferredAliasProbe", deferredAliasProbe)
	entry.Export("asyncCtxAliasProbe", asyncCtxAliasProbe)
	entry.Export("weakTarget", weakTarget)
	entry.Export("classChurn", classChurn)
	entry.Export("propChurn", propChurn)
	entry.Export("rawCls", rawCls)
	entry.Export("stats", statsProbe)
	entry.Export("freeGo", freeGo)
	entry.Export("panicSized", panicSized)
	entry.Export("wrapRemove", wrapRemove)
	entry.Export("wrapFinRemove", wrapFinRemove)
	entry.Export("wrapTwice", wrapTwice)
	entry.Export("removeWrapTwice", removeWrapTwice)
	entry.Export("ndFunction", ndFunction)
	entry.Export("defineBad", defineBad)
	entry.Export("escapeTwice", escapeTwice)
	entry.Export("scopeMisuse", scopeMisuse)
	entry.Export("clearNoExc", clearNoExc)
	entry.Export("throwRoundtrip", throwRoundtrip)
	entry.Export("coerceProbe", coerceProbe)
	entry.Export("taMake", taMake)
	entry.Export("taInfo", taInfo)
	entry.Export("dvMake", dvMake)
	entry.Export("dvInfo", dvInfo)
	entry.Export("abMake", abMake)
	entry.Export("abInfo", abInfo)
	entry.Export("abDetach", abDetach)
	entry.Export("abLifecycle", abLifecycle)
	entry.Export("utf16Probe", utf16Probe)
	entry.Export("latin1Probe", latin1Probe)
	entry.Export("utf8Edge", utf8Edge)
	entry.Export("bigintWords", bigintWords)
	entry.Export("bigintMake", bigintMake)
	entry.Export("bigintCreate", bigintCreate)
	entry.Export("argProbe", argProbe)
	entry.Export("newTargetProbe", newTargetProbe)
	entry.Export("callThrow", callThrow)
	entry.Export("runScriptThrow", runScriptThrow)
	entry.Export("callFn", callFn)
	entry.Export("newInstance", newInstance)
	entry.Export("instanceDataTwice", instanceDataTwice)
	entry.Export("finalizerTwice", finalizerTwice)
	entry.Export("probeCounters", probeCounters)
	entry.Export("wrapWithFinalize", wrapWithFinalize)
	entry.Export("wrapFinPanic", wrapFinPanic)
	entry.Export("wrapFinPanicReport", wrapFinPanicReport)
	entry.Export("refProbe", refProbe)
	entry.Export("asyncWork", asyncWork)
	entry.Export("asyncFull", asyncFull)
	entry.Export("asyncReport", asyncReport)
	entry.Export("removableHook", removableHook)
	entry.Export("asyncHookProbe", asyncHookProbe)
	entry.Export("externalProbe", externalProbe)
	entry.Export("jsProbe", jsProbe)
	entry.Export("bufferCopyEmpty", bufferCopyEmpty)
	entry.Export("promiseProbe", promiseProbe)
	entry.Export("extErrInfo", extErrInfo)
	entry.Export("versionProbe", versionProbe)
	entry.Export("rawMallocProbe", rawMallocProbe)
}

// unsafe is used by the cgo bridge in bridge.go
var _ = unsafe.Pointer(nil)
