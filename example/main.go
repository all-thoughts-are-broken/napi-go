// Command example is a demonstration addon exercising every subsystem of
// the napi-go binding. Build it with:
//
//	go build -buildmode=c-shared -o goaddon.node ./example
//
// and run example/test.mjs against it.
package main

import (
	"fmt"
	"os"
	"sort"
	"time"
	"unsafe"

	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/js"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// ---- primitives -----------------------------------------------------------

func hello(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	name, err := napi.GetValueStringUtf8(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "hello expects a string")
		return nil
	}
	out, _ := napi.CreateStringUtf8(env, "hello, "+name)
	return out
}

func add(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	a, _ := napi.GetValueDouble(env, cb.Args[0])
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	out, _ := napi.CreateDouble(env, a+b)
	return out
}

func isOdd(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])
	out, _ := napi.GetBoolean(env, n%2 != 0)
	return out
}

func concat(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetArrayLength(env, cb.Args[0])
	parts := make([]byte, 0, 64)
	for i := uint32(0); i < n; i++ {
		el, _ := napi.GetElement(env, cb.Args[0], i)
		s, _ := napi.GetValueStringUtf8(env, el)
		parts = append(parts, s...)
	}
	out, _ := napi.CreateStringUtf8(env, string(parts))
	return out
}

// ---- objects ---------------------------------------------------------------

func readProps(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	keys, _ := napi.GetPropertyNames(env, cb.Args[0])
	n, _ := napi.GetArrayLength(env, keys)

	result, _ := napi.CreateArray(env)
	sum := 0.0
	for i := uint32(0); i < n; i++ {
		key, _ := napi.GetElement(env, keys, i)
		keyStr, _ := napi.GetValueStringUtf8(env, key)
		val, _ := napi.GetNamedProperty(env, cb.Args[0], keyStr)
		num, _ := napi.GetValueDouble(env, val)
		sum += num
		s, _ := napi.CreateStringUtf8(env, fmt.Sprintf("%s=%.0f", keyStr, num))
		napi.SetElement(env, result, i, s)
	}
	s, _ := napi.CreateStringUtf8(env, fmt.Sprintf("sum=%v", sum))
	napi.SetNamedProperty(env, result, "summary", s)
	return result
}

func makeUser(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	id, _ := napi.GetValueInt64(env, cb.Args[0])
	name, _ := napi.GetValueStringUtf8(env, cb.Args[1])

	obj, _ := napi.CreateObject(env)
	idv, _ := napi.CreateInt64(env, id)
	namev, _ := napi.CreateStringUtf8(env, name)
	napi.SetNamedProperty(env, obj, "id", idv)
	napi.SetNamedProperty(env, obj, "name", namev)
	return obj
}

func freezeDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	obj, _ := napi.CreateObject(env)
	v, _ := napi.CreateInt32(env, 7)
	napi.SetNamedProperty(env, obj, "v", v)
	napi.ObjectFreeze(env, obj)
	return obj
}

// ---- binary data -----------------------------------------------------------

func makeBuffer(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])
	v, data, err := napi.CreateBuffer(env, uint64(n))
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	for i := range data {
		data[i] = byte(i)
	}
	return v
}

func bufferSum(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	bi, err := napi.GetBufferInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a Buffer")
		return nil
	}
	var sum uint64
	for _, b := range bi.Data {
		sum += uint64(b)
	}
	out, _ := napi.CreateBigIntUint64(env, sum)
	return out
}

func typedArraySum(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ta, err := napi.GetTypedArrayInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a TypedArray")
		return nil
	}
	// ta.Data already points at the first element of the view (byte offset
	// applied by the engine).
	var sum uint64
	for _, b := range ta.Data {
		sum += uint64(b)
	}
	out, _ := napi.CreateBigIntUint64(env, sum)
	return out
}

func makeArrayBufferCopy(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	src, err := napi.GetBufferInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a Buffer")
		return nil
	}
	ab, data, err2 := napi.CreateArrayBuffer(env, uint64(len(src.Data)))
	if err2 != nil {
		napi.ThrowError(env, "", err2.Error())
		return nil
	}
	copy(data, src.Data)
	view, _ := napi.CreateTypedArray(env, napi.Uint8Array, uint64(len(data)), ab, 0)
	return view
}

// ---- classes and wrapping ----------------------------------------------------

var counterTag = napi.TypeTag{Lower: 0x6e6170692d676f21, Upper: 0x636f756e74657200}

func makeCounterClass(env napi.Env, info napi.CallbackInfo) napi.Value {
	cls, err := counterClass(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return cls
}

func counterClass(env napi.Env) (napi.Value, error) {
	ctor := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		newTarget, _ := napi.GetNewTarget(e, cbi)
		if newTarget == nil {
			napi.ThrowTypeError(e, "", "Counter must be constructed with new")
			return nil
		}
		cb, _ := napi.GetCbInfo(e, cbi)
		start, _ := napi.GetValueInt64(e, cb.Args[0])
		if err := napi.Wrap(e, cb.This, &start, nil); err != nil {
			napi.ThrowError(e, "", err.Error())
			return nil
		}
		// Brand the instance so Go code can validate it later; a failure
		// here is not fatal (the object stays usable, just untagged).
		_ = napi.TypeTagObject(e, cb.This, counterTag)
		return nil
	}

	inc := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb, _ := napi.GetCbInfo(e, cbi)
		v, _ := napi.Unwrap(e, cb.This)
		p := v.(*int64)
		*p++
		out, _ := napi.CreateInt64(e, *p)
		return out
	}
	getValue := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb, _ := napi.GetCbInfo(e, cbi)
		v, _ := napi.Unwrap(e, cb.This)
		out, _ := napi.CreateInt64(e, *v.(*int64))
		return out
	}
	setValue := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb, _ := napi.GetCbInfo(e, cbi)
		v, _ := napi.Unwrap(e, cb.This)
		nv, _ := napi.GetValueInt64(e, cb.Args[0])
		*v.(*int64) = nv
		return nil
	}
	describe := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		out, _ := napi.CreateStringUtf8(e, "Counter counts things")
		return out
	}

	return napi.DefineClass(env, napi.ClassDescriptor{
		Name:        "Counter",
		Constructor: ctor,
		Properties: []napi.PropertyDescriptor{
			{Name: "inc", Method: inc, Attributes: napi.DefaultMethod},
			{Name: "value", Getter: getValue, Setter: setValue, Attributes: napi.DefaultMethod},
			{Name: "describe", Method: describe, Attributes: napi.Static | napi.DefaultMethod},
		},
	})
}

func wrapMap(env napi.Env, info napi.CallbackInfo) napi.Value {
	store := map[string]string{}

	set := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb, _ := napi.GetCbInfo(e, cbi)
		k, _ := napi.GetValueStringUtf8(e, cb.Args[0])
		v, _ := napi.GetValueStringUtf8(e, cb.Args[1])
		store[k] = v
		return nil
	}
	get := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb, _ := napi.GetCbInfo(e, cbi)
		k, _ := napi.GetValueStringUtf8(e, cb.Args[0])
		v, ok := store[k]
		if !ok {
			return nil
		}
		out, _ := napi.CreateStringUtf8(e, v)
		return out
	}
	size := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		out, _ := napi.CreateInt32(e, int32(len(store)))
		return out
	}

	obj, err := napi.CreateObject(env)
	if err != nil {
		return nil
	}
	if err := napi.Wrap(env, obj, &store, func(e napi.Env, data any) {
		fmt.Printf("[go] mapStore finalized with %T\n", data)
	}); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	props := []napi.PropertyDescriptor{
		{Name: "set", Method: set, Attributes: napi.DefaultMethod},
		{Name: "get", Method: get, Attributes: napi.DefaultMethod},
		{Name: "size", Getter: size, Attributes: napi.DefaultMethod},
	}
	if err := napi.DefineProperties(env, obj, props); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return obj
}

// ---- externals, references, instance data --------------------------------------

type goResource struct{ Name string }

func externalRoundtrip(env napi.Env, info napi.CallbackInfo) napi.Value {
	ext, err := napi.CreateExternal(env, &goResource{Name: "hello"}, func(e napi.Env, data any) {
		fmt.Printf("[go] external finalized: %v\n", data.(*goResource).Name)
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	back, _ := napi.GetValueExternal(env, ext)
	out, _ := napi.CreateStringUtf8(env, back.(*goResource).Name)
	return out
}

var keptRefs []napi.Ref

func keepValue(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ref, err := napi.CreateReference(env, cb.Args[0], 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	keptRefs = append(keptRefs, ref)
	out, _ := napi.CreateInt32(env, int32(len(keptRefs)))
	return out
}

func takeKept(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	idx, _ := napi.GetValueInt64(env, cb.Args[0])
	if idx < 1 || int(idx) > len(keptRefs) {
		napi.ThrowRangeError(env, "", "bad index")
		return nil
	}
	ref := keptRefs[idx-1]
	v, _ := napi.GetReferenceValue(env, ref)
	napi.DeleteReference(env, ref)
	keptRefs = append(keptRefs[:idx-1], keptRefs[idx:]...)
	return v
}

func instanceDataDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	v, _ := napi.GetInstanceData(env)
	if v == nil {
		if err := napi.SetInstanceData(env, &goResource{Name: "instance"}, nil); err != nil {
			napi.ThrowError(env, "", err.Error())
			return nil
		}
		v, _ = napi.GetInstanceData(env)
	}
	out, _ := napi.CreateStringUtf8(env, v.(*goResource).Name)
	return out
}

func setupCleanup(env napi.Env, info napi.CallbackInfo) napi.Value {
	err := napi.AddEnvCleanupHook(env, func() {
		fmt.Println("[go] env cleanup hook ran")
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
	}
	return nil
}

// ---- async ----------------------------------------------------------------------

func asyncDouble(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])

	promise, deferred, err := napi.CreatePromise(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	var work *napi.AsyncWork
	work, err = napi.NewAsyncWork(env, "asyncDouble",
		func(e napi.Env) { // libuv worker thread: pure Go only
			time.Sleep(30 * time.Millisecond)
		},
		func(e napi.Env, werr error) { // JS thread
			defer func() {
				if derr := work.Delete(e); derr != nil {
					fmt.Fprintln(os.Stderr, "[go] work delete failed:", derr)
				}
			}()
			if werr != nil {
				msg, _ := napi.CreateStringUtf8(e, werr.Error())
				napi.RejectDeferred(e, deferred, msg)
				return
			}
			out, _ := napi.CreateInt64(e, n*2)
			napi.ResolveDeferred(e, deferred, out)
		})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := work.Queue(env); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return promise
}

func tsfnGreet(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	name, _ := napi.GetValueStringUtf8(env, cb.Args[0])
	jsCallback := cb.Args[1]

	promise, deferred, err := napi.CreatePromise(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	tsfn, err := napi.NewThreadsafeFunction(env, jsCallback, 0, 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	// The TSFN keeps the event loop alive until every queued payload has
	// been dispatched, then Release drops it.

	go func() {
		time.Sleep(30 * time.Millisecond)
		err := tsfn.Call(func(e napi.Env, fn napi.Value) {
			msg, _ := napi.CreateStringUtf8(e, "pong: "+name)
			if fn != nil {
				undef, _ := napi.GetUndefined(e)
				_, _ = napi.CallFunction(e, undef, fn, msg)
			}
			_ = napi.ResolveDeferred(e, deferred, msg)
		}, napi.Blocking)
		if err != nil {
			fmt.Fprintln(os.Stderr, "[go] tsfn call failed:", err)
		}
		tsfn.Release(napi.Release)
	}()
	return promise
}

func callWithAsyncHooks(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ctx, err := napi.AsyncInit(env, nil, "go-async-op")
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	defer napi.AsyncDestroy(env, ctx)
	ret, err := napi.MakeCallback(env, ctx, cb.This, cb.Args[0])
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return ret
}

// ---- errors and panics ------------------------------------------------------

func throwBoomer(env napi.Env, info napi.CallbackInfo) napi.Value {
	napi.ThrowError(env, "ERR_BOOM", "explicit error from Go")
	return nil
}

func panicFn(env napi.Env, info napi.CallbackInfo) napi.Value {
	panic("go panic inside callback")
}

// ---- misc --------------------------------------------------------------------

func runScriptDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	v, err := napi.RunScript(env, "(6 * 7)")
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return v
}

func bigintOps(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	sign, words, err := napi.GetValueBigIntWords(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a BigInt")
		return nil
	}
	out, err := napi.CreateBigIntWords(env, sign, words)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return out
}

func nowDate(env napi.Env, info napi.CallbackInfo) napi.Value {
	out, _ := napi.CreateDate(env, float64(time.Now().UnixMilli()))
	return out
}

func makeSymbol(env napi.Env, info napi.CallbackInfo) napi.Value {
	desc, _ := napi.CreateStringUtf8(env, "go-symbol")
	out, _ := napi.CreateSymbol(env, desc)
	return out
}

func versionInfo(env napi.Env, info napi.CallbackInfo) napi.Value {
	napiVer, _ := napi.GetVersion(env)
	nodeVer, _ := napi.GetNodeVersion(env)
	obj, _ := napi.CreateObject(env)
	a, _ := napi.CreateUint32(env, napiVer)
	napi.SetNamedProperty(env, obj, "napi", a)
	b, _ := napi.CreateStringUtf8(env, fmt.Sprintf("v%d.%d.%d", nodeVer.Major, nodeVer.Minor, nodeVer.Patch))
	napi.SetNamedProperty(env, obj, "node", b)
	return obj
}

// jsDemo shows the syscall/js-style layer end to end: reads fields, calls a
// method with the object as `this`, and returns a Go-built structure.
func jsDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj := js.Wrap(env, cb.Args[0])
	greeting := obj.Get("greeting").String()
	times := obj.Get("times").Int()
	shouted := obj.Call("shout", greeting).String()

	back := map[string]any{
		"line": fmt.Sprintf("%s (%s) x%d", shouted, greeting, times),
		"list": []any{"a", 2, true},
	}
	return js.ValueOf(env, back).Raw()
}

func keysDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj := js.Wrap(env, cb.Args[0])
	keys := obj.Keys()
	sort.Strings(keys)
	return js.ValueOf(env, keys).Raw()
}

// counterClassRef keeps the class created at module load alive so the
// Go-side NewInstance/InstanceOf demos can use it from later callbacks.
// A raw napi.Value captured at registration time would be a dead handle
// outside the registration callback's scope — use a reference.
var counterClassRef napi.Ref

// ---- advanced coverage: classes from Go, accessors, symbols, scopes ------

// newCounterFromGo constructs an instance with `new Counter(n)` from the Go
// side (napi.NewInstance) — the class itself was defined in Go.
func newCounterFromGo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	cls, err := napi.GetReferenceValue(env, counterClassRef)
	if err != nil || cls == nil {
		napi.ThrowError(env, "", "counter class not available")
		return nil
	}
	inst, err := napi.NewInstance(env, cls, cb.Args[0])
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	isInst, _ := napi.InstanceOf(env, inst, cls)
	obj, _ := napi.CreateObject(env)
	bv, _ := napi.GetBoolean(env, isInst)
	napi.SetNamedProperty(env, obj, "instance", inst)
	napi.SetNamedProperty(env, obj, "isInstanceOf", bv)
	return obj
}

// symbolProps defines a property keyed by a JS symbol
// (PropertyDescriptor.NameValue).
func symbolProps(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj, _ := napi.CreateObject(env)
	val, _ := napi.CreateStringUtf8(env, "via symbol")
	err := napi.DefineProperties(env, obj, []napi.PropertyDescriptor{
		{NameValue: cb.Args[0], Value: val, Attributes: napi.Writable | napi.Enumerable | napi.Configurable},
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	plain, _ := napi.CreateStringUtf8(env, "plain")
	napi.SetNamedProperty(env, obj, "plain", plain)
	return obj
}

// readOnlyProp defines an enumerable but non-writable property: strict-mode
// assignment from JS must throw.
func readOnlyProp(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj, _ := napi.CreateObject(env)
	err := napi.DefineProperties(env, obj, []napi.PropertyDescriptor{
		{Name: "ro", Value: cb.Args[0], Attributes: napi.Enumerable | napi.Configurable},
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return obj
}

// makeAccessor returns an object with a full getter+setter accessor backed
// by wrapped Go state.
func makeAccessor(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	init, _ := napi.GetValueDouble(env, cb.Args[0])
	p := &init

	obj, _ := napi.CreateObject(env)
	if err := napi.Wrap(env, obj, p, nil); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	getter := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		out, _ := napi.CreateDouble(e, *p) // use the callback's env, not the outer one
		return out
	}
	setter := func(e napi.Env, cbi napi.CallbackInfo) napi.Value {
		cb2, _ := napi.GetCbInfo(e, cbi)
		nv, _ := napi.GetValueDouble(e, cb2.Args[0])
		*p = nv * 2 // setters can transform
		return nil
	}
	if err := napi.DefineProperties(env, obj, []napi.PropertyDescriptor{
		{Name: "value", Getter: getter, Setter: setter, Attributes: napi.DefaultMethod},
	}); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return obj
}

// escapeDemo creates many values inside an escapable handle scope and
// escapes exactly one out (the standard way to build a return value in a
// loop without leaking handles).
func escapeDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	scope, err := napi.OpenEscapableHandleScope(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	defer napi.CloseEscapableHandleScope(env, scope)

	var last napi.Value
	for i := 0; i < 10; i++ {
		last, _ = napi.CreateInt32(env, int32(i))
	}
	esc, err := napi.EscapeHandle(env, scope, last)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return esc
}

// safeCall invokes a JS function and reports a thrown exception through
// IsExceptionPending + GetAndClearLastException instead of propagating it.
func safeCall(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	undef, _ := napi.GetUndefined(env)

	res, err := napi.CallFunction(env, undef, cb.Args[0])
	if err == nil {
		obj, _ := napi.CreateObject(env)
		ok, _ := napi.GetBoolean(env, true)
		napi.SetNamedProperty(env, obj, "ok", ok)
		napi.SetNamedProperty(env, obj, "value", res)
		return obj
	}

	pending, _ := napi.IsExceptionPending(env)
	if !pending {
		napi.ThrowError(env, "", err.Error()) // engine-level failure, not JS
		return nil
	}
	ex, _ := napi.GetAndClearLastException(env)
	msgV, _ := napi.GetNamedProperty(env, ex, "message")
	msg, _ := napi.GetValueStringUtf8(env, msgV)

	obj, _ := napi.CreateObject(env)
	ok, _ := napi.GetBoolean(env, false)
	mv, _ := napi.CreateStringUtf8(env, msg)
	napi.SetNamedProperty(env, obj, "ok", ok)
	napi.SetNamedProperty(env, obj, "message", mv)
	_ = ok
	return obj
}

// coerceDemo exercises the JS coercions.
func coerceDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	b, _ := napi.CoerceToBool(env, cb.Args[0])
	s, _ := napi.CoerceToString(env, cb.Args[0])
	obj, _ := napi.CreateObject(env)
	bv, _ := napi.GetValueBool(env, b)
	sv, _ := napi.GetValueStringUtf8(env, s)
	ab, _ := napi.GetBoolean(env, bv)
	ss, _ := napi.CreateStringUtf8(env, sv)
	napi.SetNamedProperty(env, obj, "asBool", ab)
	napi.SetNamedProperty(env, obj, "asString", ss)
	return obj
}

// errorValueDemo creates an Error as a VALUE (not thrown) and checks it
// with IsError.
func errorValueDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	code, _ := napi.CreateStringUtf8(env, "ERR_DEMO")
	msg, _ := napi.CreateStringUtf8(env, "error as a value")
	e, err := napi.CreateError(env, code, msg)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	isErr, _ := napi.IsError(env, e)
	obj, _ := napi.CreateObject(env)
	bv, _ := napi.GetBoolean(env, isErr)
	napi.SetNamedProperty(env, obj, "isError", bv)
	napi.SetNamedProperty(env, obj, "error", e)
	return obj
}

// asyncFail returns a rejected Promise — the rejection path of async work.
func asyncFail(env napi.Env, info napi.CallbackInfo) napi.Value {
	promise, deferred, err := napi.CreatePromise(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	code, _ := napi.CreateStringUtf8(env, "ERR_FAIL")
	msg, _ := napi.CreateStringUtf8(env, "async failure from Go")
	errV, _ := napi.CreateError(env, code, msg)
	if err := napi.RejectDeferred(env, deferred, errV); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return promise
}

// ---- binary data: copy, external arraybuffer, DataView, detach -------------

func bufferCopy(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	src, err := napi.GetBufferInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected Buffer")
		return nil
	}
	v, err := napi.CreateBufferCopy(env, src.Data)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return v
}

// externalArrayBufferDemo wraps Go-owned memory zero-copy. The Go slice is
// kept alive through the finalData slot until the ArrayBuffer is collected.
func externalArrayBufferDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])

	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	ab, err := napi.CreateExternalArrayBuffer(
		env, unsafe.Pointer(&b[0]), uint64(len(b)),
		nil, b, // finalData pins b until the finalizer runs
	)
	if err != nil {
		// Some Node builds disallow external buffers entirely.
		fmt.Fprintln(os.Stderr, "[go] external arraybuffer unsupported:", err)
		return nil
	}
	view, _ := napi.CreateTypedArray(env, napi.Uint8Array, uint64(len(b)), ab, 0)
	return view
}

func makeDataView(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])
	ab, data, err := napi.CreateArrayBuffer(env, uint64(n))
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	for i := range data {
		data[i] = byte(i)
	}
	dv, err := napi.CreateDataView(env, uint64(n), ab, 0)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return dv
}

// detachDemo detaches an ArrayBuffer and reports both the Go-side predicate
// and the detached object.
func detachDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	ab, _, err := napi.CreateArrayBuffer(env, 16)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.DetachArrayBuffer(env, ab); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	detached, _ := napi.IsDetachedArrayBuffer(env, ab)
	obj, _ := napi.CreateObject(env)
	bv, _ := napi.GetBoolean(env, detached)
	napi.SetNamedProperty(env, obj, "ab", ab)
	napi.SetNamedProperty(env, obj, "detached", bv)
	return obj
}

// utf16Demo round-trips a string through UTF-16 code units.
func utf16Demo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	units, err := napi.GetValueStringUtf16(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected string")
		return nil
	}
	out, err := napi.CreateStringUtf16(env, units)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return out
}

// ---- misc API surface --------------------------------------------------------

// adjustMemoryDemo exercises napi_adjust_external_memory. The API returns the
// post-adjustment total, so +1MiB followed by -1MiB must land back on the
// original counter — asserted from JS as a 1MiB drop, which is what proves the
// delta actually reached V8 (a no-op binding would report two equal values).
func adjustMemoryDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	before, err := napi.AdjustExternalMemory(env, 1<<20)
	if err != nil {
		napi.ThrowError(env, "", "AdjustExternalMemory(+1MiB): "+err.Error())
		return nil
	}
	after, err := napi.AdjustExternalMemory(env, -(1 << 20))
	if err != nil {
		napi.ThrowError(env, "", "AdjustExternalMemory(-1MiB): "+err.Error())
		return nil
	}
	obj, _ := napi.CreateObject(env)
	b, _ := napi.CreateInt64(env, before)
	a, _ := napi.CreateInt64(env, after)
	napi.SetNamedProperty(env, obj, "before", b)
	napi.SetNamedProperty(env, obj, "after", a)
	return obj
}

func getAllKeys(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	all, _ := napi.GetAllPropertyNames(env, cb.Args[0], napi.IncludePrototypes, napi.KeyAllProperties, napi.KeepNumbers)
	own, _ := napi.GetAllPropertyNames(env, cb.Args[0], napi.OwnOnly, napi.KeyAllProperties, napi.KeepNumbers)
	obj, _ := napi.CreateObject(env)
	napi.SetNamedProperty(env, obj, "all", all)
	napi.SetNamedProperty(env, obj, "own", own)
	return obj
}

func strictEq(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	eq, _ := napi.StrictEquals(env, cb.Args[0], cb.Args[1])
	out, _ := napi.GetBoolean(env, eq)
	return out
}

func deleteProp(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	key, _ := napi.CreateStringUtf8(env, "gone")
	deleted, err := napi.DeleteProperty(env, cb.Args[0], key)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.GetBoolean(env, deleted)
	return out
}

// removableHook registers an env cleanup hook and removes it immediately;
// nothing is printed on exit, which is the observable effect.
func removableHook(env napi.Env, info napi.CallbackInfo) napi.Value {
	tok, err := napi.AddEnvCleanupHookRemovable(env, func() {
		fmt.Println("[go] this hook was removed and must not run")
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.RemoveEnvCleanupHook(tok); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	ok, _ := napi.GetBoolean(env, true)
	return ok
}

// ---- weak references ---------------------------------------------------------

var weakRefs []napi.Ref

func keepWeak(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ref, err := napi.CreateReference(env, cb.Args[0], 0) // refcount 0 = weak
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	weakRefs = append(weakRefs, ref)
	out, _ := napi.CreateInt64(env, int64(len(weakRefs)-1))
	return out
}

func weakAlive(env napi.Env, info napi.CallbackInfo) napi.Value {
	alive := int64(0)
	for _, ref := range weakRefs {
		if v, err := napi.GetReferenceValue(env, ref); err == nil && v != nil {
			alive++
		}
	}
	out, _ := napi.CreateInt64(env, alive)
	return out
}

func resetWeak(env napi.Env, info napi.CallbackInfo) napi.Value {
	for _, ref := range weakRefs {
		napi.DeleteReference(env, ref)
	}
	weakRefs = nil
	return nil
}

// ---- js package extras: FuncOf and persistent refs -----------------------------

// goFuncDemo returns a Go-implemented function to JavaScript via js.FuncOf.
func goFuncDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	fn := js.FuncOf(env, func(this js.Value, args []js.Value) any {
		return fmt.Sprintf("hello %s (from Go)", args[0].String())
	})
	return fn.Raw()
}

var storedRef *js.Ref

// storeCallback keeps a JS callback alive across callbacks (js.MakeRef).
func storeCallback(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	if storedRef != nil {
		storedRef.Release()
	}
	storedRef = js.Wrap(env, cb.Args[0]).MakeRef()
	return nil
}

// invokeStored calls the previously stored callback.
func invokeStored(env napi.Env, info napi.CallbackInfo) napi.Value {
	if storedRef == nil {
		napi.ThrowError(env, "", "no stored callback")
		return nil
	}
	return storedRef.Value().Invoke("from stored ref").Raw()
}

// releaseStored drops the stored callback reference.
func releaseStored(env napi.Env, info napi.CallbackInfo) napi.Value {
	if storedRef != nil {
		storedRef.Release()
		storedRef = nil
	}
	return nil
}

// ---- full-surface coverage ---------------------------------------------------
//
// The demos below exist so that every exported napi function is reached by a
// runnable path; test.mjs asserts each one. The only deliberate exception is
// FatalError/FatalException, which terminate the process by design and
// therefore cannot share a test run with anything else (see docs/API.md).

func setBoolProp(env napi.Env, obj napi.Value, name string, v bool) {
	bv, _ := napi.GetBoolean(env, v)
	napi.SetNamedProperty(env, obj, name, bv)
}

func setInt32Prop(env napi.Env, obj napi.Value, name string, v int32) {
	iv, _ := napi.CreateInt32(env, v)
	napi.SetNamedProperty(env, obj, name, iv)
}

func setStringProp(env napi.Env, obj napi.Value, name, v string) {
	sv, _ := napi.CreateStringUtf8(env, v)
	napi.SetNamedProperty(env, obj, name, sv)
}

func valueTypeName(t napi.ValueType) string {
	switch t {
	case napi.TypeUndefined:
		return "undefined"
	case napi.TypeNull:
		return "null"
	case napi.TypeBoolean:
		return "boolean"
	case napi.TypeNumber:
		return "number"
	case napi.TypeString:
		return "string"
	case napi.TypeSymbol:
		return "symbol"
	case napi.TypeObject:
		return "object"
	case napi.TypeFunction:
		return "function"
	case napi.TypeExternal:
		return "external"
	case napi.TypeBigInt:
		return "bigint"
	default:
		return fmt.Sprintf("unknown(%d)", uint32(t))
	}
}

// typeOfDemo maps napi_typeof onto the JS typeof name the caller expects.
func typeOfDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	t, err := napi.Typeof(env, cb.Args[0])
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "type", valueTypeName(t))
	return out
}

// coerceMoreDemo covers the numeric and object coercions. It is fed a
// primitive from JS, so neither coercion can throw.
func coerceMoreDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	num, err := napi.CoerceToNumber(env, cb.Args[0])
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	boxed, err := napi.CoerceToObject(env, cb.Args[0])
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "type", valueTypeName(mustTypeof(env, boxed)))
	napi.SetNamedProperty(env, out, "number", num)
	napi.SetNamedProperty(env, out, "boxed", boxed)
	return out
}

func mustTypeof(env napi.Env, v napi.Value) napi.ValueType {
	t, _ := napi.Typeof(env, v)
	return t
}

func mustNumber(env napi.Env, f float64) napi.Value {
	v, _ := napi.CreateDouble(env, f)
	return v
}

// predicatesDemo runs every napi_is_* predicate against one value.
func predicatesDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	v := cb.Args[0]
	out, _ := napi.CreateObject(env)
	if b, err := napi.IsArray(env, v); err == nil {
		setBoolProp(env, out, "array", b)
	}
	if b, err := napi.IsArrayBuffer(env, v); err == nil {
		setBoolProp(env, out, "arrayBuffer", b)
	}
	if b, err := napi.IsTypedArray(env, v); err == nil {
		setBoolProp(env, out, "typedArray", b)
	}
	if b, err := napi.IsDataView(env, v); err == nil {
		setBoolProp(env, out, "dataView", b)
	}
	if b, err := napi.IsDate(env, v); err == nil {
		setBoolProp(env, out, "date", b)
	}
	if b, err := napi.IsPromise(env, v); err == nil {
		setBoolProp(env, out, "promise", b)
	}
	if b, err := napi.IsBuffer(env, v); err == nil {
		setBoolProp(env, out, "buffer", b)
	}
	return out
}

// propOpsDemo exercises the Value-keyed property API plus elements and the
// prototype accessor.
func propOpsDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	obj, key := cb.Args[0], cb.Args[1]
	fail := func(what string, err error) napi.Value {
		napi.ThrowError(env, "", what+": "+err.Error())
		return nil
	}

	val, _ := napi.CreateStringUtf8(env, "set-by-go")
	if err := napi.SetProperty(env, obj, key, val); err != nil {
		return fail("SetProperty", err)
	}
	got, err := napi.GetProperty(env, obj, key)
	if err != nil {
		return fail("GetProperty", err)
	}
	gotStr, _ := napi.GetValueStringUtf8(env, got)
	hasProp, _ := napi.HasProperty(env, obj, key)
	hasOwn, _ := napi.HasOwnProperty(env, obj, key)

	// "toString" lives on Object.prototype: inherited but not own.
	ts, _ := napi.CreateStringUtf8(env, "toString")
	hasInherited, _ := napi.HasProperty(env, obj, ts)
	hasInheritedOwn, _ := napi.HasOwnProperty(env, obj, ts)

	hasNamed, _ := napi.HasNamedProperty(env, obj, "injected")

	proto, err := napi.GetPrototype(env, obj)
	if err != nil {
		return fail("GetPrototype", err)
	}

	elem, _ := napi.CreateInt32(env, 7)
	if err := napi.SetElement(env, obj, 0, elem); err != nil {
		return fail("SetElement", err)
	}
	gotElem, err := napi.GetElement(env, obj, 0)
	if err != nil {
		return fail("GetElement", err)
	}
	elemVal, _ := napi.GetValueInt32(env, gotElem)
	hasElemBefore, _ := napi.HasElement(env, obj, 0)
	deleted, err := napi.DeleteElement(env, obj, 0)
	if err != nil {
		return fail("DeleteElement", err)
	}
	hasElemAfter, _ := napi.HasElement(env, obj, 0)

	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "value", gotStr)
	setBoolProp(env, out, "hasProperty", hasProp)
	setBoolProp(env, out, "hasOwnProperty", hasOwn)
	setBoolProp(env, out, "hasInherited", hasInherited)
	setBoolProp(env, out, "hasInheritedOwn", hasInheritedOwn)
	setBoolProp(env, out, "hasNamed", hasNamed)
	setInt32Prop(env, out, "element", elemVal)
	setBoolProp(env, out, "hasElementBefore", hasElemBefore)
	setBoolProp(env, out, "deleted", deleted)
	setBoolProp(env, out, "hasElementAfter", hasElemAfter)
	setBoolProp(env, out, "protoIsObject", proto != nil && mustTypeof(env, proto) == napi.TypeObject)
	return out
}

// valueReadsDemo covers the remaining extractors: 32-bit integer reads,
// Latin-1 round trip and the BigInt accessors.
func valueReadsDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	num, str, big := cb.Args[0], cb.Args[1], cb.Args[2]

	i32, err := napi.GetValueInt32(env, num)
	if err != nil {
		napi.ThrowTypeError(env, "", "expected an int32-range number")
		return nil
	}
	u32, err := napi.GetValueUint32(env, num)
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a uint32-range number")
		return nil
	}
	latin, err := napi.GetValueStringLatin1(env, str)
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a string")
		return nil
	}
	latinBack, err := napi.CreateStringLatin1(env, latin)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	created, err := napi.CreateBigIntInt64(env, -9007199254740993) // beyond 2^53
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	signed, lossless, err := napi.GetValueBigIntInt64(env, big)
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a BigInt")
		return nil
	}
	unsigned, _, _ := napi.GetValueBigIntUint64(env, big)
	unsignedBig, err := napi.CreateBigIntUint64(env, unsigned)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	out, _ := napi.CreateObject(env)
	setInt32Prop(env, out, "int32", i32)
	iv, _ := napi.CreateUint32(env, u32)
	napi.SetNamedProperty(env, out, "uint32", iv)
	napi.SetNamedProperty(env, out, "latin1Back", latinBack)
	napi.SetNamedProperty(env, out, "createdBigInt", created)
	sv, _ := napi.CreateInt64(env, signed) // number conversion is lossy past 2^53
	napi.SetNamedProperty(env, out, "signed", sv)
	napi.SetNamedProperty(env, out, "unsigned", unsignedBig)
	setBoolProp(env, out, "lossless", lossless)
	return out
}

// handleScopeDemo creates a thousand values inside a non-escapable scope and
// a fresh value after closing it. Only the last value may be returned.
func handleScopeDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	scope, err := napi.OpenHandleScope(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	for i := 0; i < 1000; i++ {
		_, _ = napi.CreateInt32(env, int32(i))
	}
	if err := napi.CloseHandleScope(env, scope); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateInt32(env, 4242)
	return out
}

// callbackScopeDemo wraps MakeCallback in the callback scope that Node-API
// requires when the call does not originate from an ordinary JS callback
// (the TSFN dispatch case).
func callbackScopeDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ctx, err := napi.AsyncInit(env, nil, "go-callback-scope")
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	defer napi.AsyncDestroy(env, ctx)

	scope, err := napi.OpenCallbackScope(env, nil, ctx)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	ret, callErr := napi.MakeCallback(env, ctx, cb.This, cb.Args[0])
	closeErr := napi.CloseCallbackScope(env, scope)
	if callErr != nil {
		napi.ThrowError(env, "", callErr.Error())
		return nil
	}
	if closeErr != nil {
		napi.ThrowError(env, "", closeErr.Error())
		return nil
	}
	return ret
}

// refCountDemo walks the reference life cycle: weak(0) -> strong(1) -> weak(0).
func refCountDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ref, err := napi.CreateReference(env, cb.Args[0], 0)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	strong, err := napi.ReferenceRef(env, ref)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	got, gotErr := napi.GetReferenceValue(env, ref)
	weak, err := napi.ReferenceUnref(env, ref)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.DeleteReference(env, ref); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setInt32Prop(env, out, "strong", int32(strong))
	setInt32Prop(env, out, "weak", int32(weak))
	setBoolProp(env, out, "valueAlive", got != nil && gotErr == nil)
	return out
}

// wrapRemoveDemo detaches a wrapped Go value; Unwrap must fail afterwards.
func wrapRemoveDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	obj, err := napi.CreateObject(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.Wrap(env, obj, &goResource{Name: "wrapped-value"}, nil); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	before, err := napi.Unwrap(env, obj)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	removed, err := napi.RemoveWrap(env, obj)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	_, afterErr := napi.Unwrap(env, obj)

	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "before", before.(*goResource).Name)
	if removed != nil {
		setStringProp(env, out, "removed", removed.(*goResource).Name)
	}
	setBoolProp(env, out, "detached", afterErr != nil)
	return out
}

// sealDemo seals an object: properties stay readable but become non-addable.
func sealDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	obj, err := napi.CreateObject(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	v, _ := napi.CreateInt32(env, 7)
	if err := napi.SetNamedProperty(env, obj, "v", v); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.ObjectSeal(env, obj); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return obj
}

// tagDemo reports whether the argument carries the Counter type tag, and
// brands a fresh object with it.
func tagDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	argTagged, err := napi.CheckObjectTypeTag(env, cb.Args[0], counterTag)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	tagged, _ := napi.CreateObject(env)
	if err := napi.TypeTagObject(env, tagged, counterTag); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	freshTagged, err := napi.CheckObjectTypeTag(env, tagged, counterTag)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setBoolProp(env, out, "argTagged", argTagged)
	setBoolProp(env, out, "freshTagged", freshTagged)
	napi.SetNamedProperty(env, out, "tagged", tagged)
	return out
}

// externalBufferDemo wraps Go-owned memory as a node:Buffer without copying.
// Some Node builds disallow external buffers; the demo reports which
// happened instead of hiding it.
func externalBufferDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	data := []byte("zero-copy node:Buffer backed by Go memory")
	buf, err := napi.CreateExternalBuffer(
		env, unsafe.Pointer(&data[0]), uint64(len(data)),
		nil, data, // pins data until the Buffer is collected
	)
	out, _ := napi.CreateObject(env)
	if err != nil {
		code := "unknown"
		if st, ok := napi.AsStatus(err); ok {
			code = st.String()
		}
		setBoolProp(env, out, "supported", false)
		setStringProp(env, out, "code", code)
		return out
	}
	setBoolProp(env, out, "supported", true)
	setInt32Prop(env, out, "length", int32(len(data)))
	napi.SetNamedProperty(env, out, "buffer", buf)
	return out
}

// errorCtorDemo builds a TypeError or RangeError as a value, carrying a code.
func errorCtorDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kind, _ := napi.GetValueStringUtf8(env, cb.Args[0])
	code, _ := napi.CreateStringUtf8(env, "ERR_CTOR")
	msg, _ := napi.CreateStringUtf8(env, "created in Go: "+kind)

	var (
		v   napi.Value
		err error
	)
	switch kind {
	case "range":
		v, err = napi.CreateRangeError(env, code, msg)
	default:
		v, err = napi.CreateTypeError(env, code, msg)
	}
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return v
}

// throwPlainDemo throws a non-Error value with napi_throw.
func throwPlainDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	v, err := napi.CreateStringUtf8(env, "thrown-plain-value")
	if err != nil {
		return nil
	}
	if err := napi.Throw(env, v); err != nil {
		fmt.Fprintln(os.Stderr, "[go] napi_throw failed:", err)
	}
	return nil
}

// extendedErrorInfoDemo deliberately misuses an accessor and reports the
// engine's error info for it.
func extendedErrorInfoDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	s, _ := napi.CreateStringUtf8(env, "not a number")
	_, err := napi.GetValueDouble(env, s)
	if err == nil {
		napi.ThrowError(env, "", "GetValueDouble unexpectedly accepted a string")
		return nil
	}
	msg, code, ierr := napi.GetExtendedErrorInfo(env)
	if ierr != nil {
		napi.ThrowError(env, "", "GetExtendedErrorInfo: "+ierr.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "code", code.String())
	setStringProp(env, out, "message", msg)
	if st, ok := napi.AsStatus(err); ok {
		setStringProp(env, out, "status", st.String())
	}
	return out
}

// uvLoopDemo surfaces the libuv event loop pointer.
func uvLoopDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	loop, err := napi.GetUVEventLoop(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setBoolProp(env, out, "nonNil", loop != nil)
	return out
}

// dateReadDemo extracts the millisecond value out of a Date.
func dateReadDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ms, err := napi.GetDateValue(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a Date")
		return nil
	}
	return mustNumber(env, ms)
}

// nullGlobalDemo returns null plus the global object, and brands the global
// so JS can prove the returned handle is the real thing.
func nullGlobalDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	n, err := napi.GetNull(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	g, err := napi.GetGlobal(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	marker, _ := napi.CreateStringUtf8(env, "from-go")
	if err := napi.SetNamedProperty(env, g, "__goGlobalMarker", marker); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	napi.SetNamedProperty(env, out, "null", n)
	napi.SetNamedProperty(env, out, "global", g)
	return out
}

// asyncCleanupHookDemo registers and then removes an async cleanup hook.
func asyncCleanupHookDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	h, err := napi.AddAsyncCleanupHook(env, func(handle napi.AsyncCleanupHookHandle) {
		fmt.Println("[go] async cleanup hook ran")
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	if err := napi.RemoveAsyncCleanupHook(h); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	out, _ := napi.CreateObject(env)
	setBoolProp(env, out, "registered", true)
	setBoolProp(env, out, "removed", true)
	return out
}

// arrayBufferInfoDemo reads back the raw storage of an ArrayBuffer.
func arrayBufferInfoDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	inf, err := napi.GetArrayBufferInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected an ArrayBuffer")
		return nil
	}
	sum := 0
	for _, b := range inf.Data {
		sum += int(b)
	}
	out, _ := napi.CreateObject(env)
	setInt32Prop(env, out, "byteLength", int32(len(inf.Data)))
	setInt32Prop(env, out, "sum", int32(sum))
	return out
}

// dataViewInfoDemo reads back a DataView's view window and backing buffer.
func dataViewInfoDemo(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	inf, err := napi.GetDataViewInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected a DataView")
		return nil
	}
	sum := 0
	for _, b := range inf.Data {
		sum += int(b)
	}
	out, _ := napi.CreateObject(env)
	setInt32Prop(env, out, "byteLength", int32(inf.ByteLength))
	setInt32Prop(env, out, "byteOffset", int32(inf.ByteOffset))
	setInt32Prop(env, out, "sum", int32(sum))
	setBoolProp(env, out, "hasArrayBuffer", inf.ArrayBuffer != nil)
	return out
}

func init() {
	entry.Export("hello", hello)
	entry.Export("add", add)
	entry.Export("isOdd", isOdd)
	entry.Export("concat", concat)
	entry.Export("readProps", readProps)
	entry.Export("makeUser", makeUser)
	entry.Export("freezeDemo", freezeDemo)
	entry.Export("makeBuffer", makeBuffer)
	entry.Export("bufferSum", bufferSum)
	entry.Export("typedArraySum", typedArraySum)
	entry.Export("makeArrayBufferCopy", makeArrayBufferCopy)
	entry.Export("makeCounter", makeCounterClass)
	entry.ExportValue("CounterClass", func(env napi.Env) (napi.Value, error) {
		v, err := counterClass(env)
		if err == nil && v != nil {
			counterClassRef, _ = napi.CreateReference(env, v, 1)
		}
		return v, err
	})
	entry.Export("wrapMap", wrapMap)
	entry.Export("externalRoundtrip", externalRoundtrip)
	entry.Export("keepValue", keepValue)
	entry.Export("takeKept", takeKept)
	entry.Export("instanceDataDemo", instanceDataDemo)
	entry.Export("setupCleanup", setupCleanup)
	entry.Export("asyncDouble", asyncDouble)
	entry.Export("tsfnGreet", tsfnGreet)
	entry.Export("callWithAsyncHooks", callWithAsyncHooks)
	entry.Export("throwBoomer", throwBoomer)
	entry.Export("panicFn", panicFn)
	entry.Export("runScriptDemo", runScriptDemo)
	entry.Export("bigintOps", bigintOps)
	entry.Export("nowDate", nowDate)
	entry.Export("makeSymbol", makeSymbol)
	entry.Export("versionInfo", versionInfo)
	entry.Export("jsDemo", jsDemo)
	entry.Export("keysDemo", keysDemo)

	// advanced coverage
	entry.Export("newCounterFromGo", newCounterFromGo)
	entry.Export("symbolProps", symbolProps)
	entry.Export("readOnlyProp", readOnlyProp)
	entry.Export("makeAccessor", makeAccessor)
	entry.Export("escapeDemo", escapeDemo)
	entry.Export("safeCall", safeCall)
	entry.Export("coerceDemo", coerceDemo)
	entry.Export("errorValueDemo", errorValueDemo)
	entry.Export("asyncFail", asyncFail)
	entry.Export("bufferCopy", bufferCopy)
	entry.Export("externalArrayBufferDemo", externalArrayBufferDemo)
	entry.Export("makeDataView", makeDataView)
	entry.Export("detachDemo", detachDemo)
	entry.Export("utf16Demo", utf16Demo)
	entry.Export("adjustMemoryDemo", adjustMemoryDemo)
	entry.Export("getAllKeys", getAllKeys)
	entry.Export("strictEq", strictEq)
	entry.Export("deleteProp", deleteProp)
	entry.Export("removableHook", removableHook)
	entry.Export("keepWeak", keepWeak)
	entry.Export("weakAlive", weakAlive)
	entry.Export("resetWeak", resetWeak)
	entry.Export("goFuncDemo", goFuncDemo)
	entry.Export("storeCallback", storeCallback)
	entry.Export("invokeStored", invokeStored)
	entry.Export("releaseStored", releaseStored)

	// full-surface coverage
	entry.Export("typeOfDemo", typeOfDemo)
	entry.Export("coerceMoreDemo", coerceMoreDemo)
	entry.Export("predicatesDemo", predicatesDemo)
	entry.Export("propOpsDemo", propOpsDemo)
	entry.Export("valueReadsDemo", valueReadsDemo)
	entry.Export("handleScopeDemo", handleScopeDemo)
	entry.Export("callbackScopeDemo", callbackScopeDemo)
	entry.Export("refCountDemo", refCountDemo)
	entry.Export("wrapRemoveDemo", wrapRemoveDemo)
	entry.Export("sealDemo", sealDemo)
	entry.Export("tagDemo", tagDemo)
	entry.Export("externalBufferDemo", externalBufferDemo)
	entry.Export("errorCtorDemo", errorCtorDemo)
	entry.Export("throwPlainDemo", throwPlainDemo)
	entry.Export("extendedErrorInfoDemo", extendedErrorInfoDemo)
	entry.Export("uvLoopDemo", uvLoopDemo)
	entry.Export("dateReadDemo", dateReadDemo)
	entry.Export("nullGlobalDemo", nullGlobalDemo)
	entry.Export("asyncCleanupHookDemo", asyncCleanupHookDemo)
	entry.Export("arrayBufferInfoDemo", arrayBufferInfoDemo)
	entry.Export("dataViewInfoDemo", dataViewInfoDemo)
}

func main() {}
