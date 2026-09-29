// Command stress is the load-test addon for napi-go. Its exports are driven
// by stress/run_all.mjs, which asserts throughput, memory and correctness
// bounds for every subsystem.
package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// ---- instrumentation ---------------------------------------------------------

func debugStats(env napi.Env, info napi.CallbackInfo) napi.Value {
	alloc, freed := napi.DebugStats()
	obj, _ := napi.CreateObject(env)
	a, _ := napi.CreateDouble(env, float64(alloc))
	f, _ := napi.CreateDouble(env, float64(freed))
	napi.SetNamedProperty(env, obj, "boxesAllocated", a)
	napi.SetNamedProperty(env, obj, "boxesFreed", f)
	return obj
}

func goMem(env napi.Env, info napi.CallbackInfo) napi.Value {
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	obj, _ := napi.CreateObject(env)
	heap, _ := napi.CreateDouble(env, float64(ms.HeapAlloc))
	sys, _ := napi.CreateDouble(env, float64(ms.Sys))
	numGC, _ := napi.CreateInt64(env, int64(ms.NumGC))
	napi.SetNamedProperty(env, obj, "heapAlloc", heap)
	napi.SetNamedProperty(env, obj, "goSys", sys)
	napi.SetNamedProperty(env, obj, "numGC", numGC)
	return obj
}

func freeGoMemory(env napi.Env, info napi.CallbackInfo) napi.Value {
	runtime.GC()
	debug.FreeOSMemory()
	return nil
}

// drainFinalizers runs two full GC cycles so that Go finalizers (and the
// Node finalizers they trigger) execute while the environment is still
// healthy, instead of during env teardown on a dying worker thread.
func drainFinalizers(env napi.Env, info napi.CallbackInfo) napi.Value {
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
	return nil
}

// ---- throughput --------------------------------------------------------------

func noop(env napi.Env, info napi.CallbackInfo) napi.Value {
	return nil
}

// addDoubles adds two numbers — the cheapest useful round trip.
func addDoubles(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	a, _ := napi.GetValueDouble(env, cb.Args[0])
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	out, _ := napi.CreateDouble(env, a+b)
	return out
}

// ---- value churn ---------------------------------------------------------------

// churnRound creates and drops k composite values (object + string + array),
// all inside one callback: GC pressure without JS round trips.
func churnRound(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	k, _ := napi.GetValueInt64(env, cb.Args[0])
	for i := int64(0); i < k; i++ {
		obj, _ := napi.CreateObject(env)
		nv, _ := napi.CreateInt64(env, i)
		napi.SetNamedProperty(env, obj, "n", nv)
		sv, _ := napi.CreateStringUtf8(env, fmt.Sprintf("value-%d", i))
		napi.SetNamedProperty(env, obj, "name", sv)
		arr, _ := napi.CreateArrayWithLength(env, 10)
		for j := 0; j < 10; j++ {
			ev, _ := napi.CreateDouble(env, float64(j))
			napi.SetElement(env, arr, uint32(j), ev)
		}
		napi.SetNamedProperty(env, obj, "elems", arr)
	}
	return nil
}

// churnObject builds one composite value per call: JS-side drop + V8 GC churn.
func churnObject(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	i, _ := napi.GetValueInt64(env, cb.Args[0])
	obj, _ := napi.CreateObject(env)
	nv, _ := napi.CreateInt64(env, i)
	napi.SetNamedProperty(env, obj, "n", nv)
	sv, _ := napi.CreateStringUtf8(env, fmt.Sprintf("value-%d", i))
	napi.SetNamedProperty(env, obj, "name", sv)
	arr, _ := napi.CreateArrayWithLength(env, 10)
	for j := 0; j < 10; j++ {
		ev, _ := napi.CreateDouble(env, float64(j))
		napi.SetElement(env, arr, uint32(j), ev)
	}
	napi.SetNamedProperty(env, obj, "elems", arr)
	return obj
}

// ---- wrap & function churn (finalizer / registry leak check) ---------------------

var wrapFinalized atomic.Int64

func newWrapped(env napi.Env, info napi.CallbackInfo) napi.Value {
	obj, err := napi.CreateObject(env)
	if err != nil {
		return nil
	}
	v := new(int64)
	*v = time.Now().UnixNano()
	napi.Wrap(env, obj, v, func(e napi.Env, data any) {
		wrapFinalized.Add(1)
	})
	return obj
}

func wrapFinalizedCount(env napi.Env, info napi.CallbackInfo) napi.Value {
	out, _ := napi.CreateInt64(env, wrapFinalized.Load())
	return out
}

func createFn(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	name, _ := napi.GetValueStringUtf8(env, cb.Args[0])
	fn, err := napi.CreateFunction(env, name, func(e napi.Env, i napi.CallbackInfo) napi.Value { return nil })
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return fn
}

// ---- TSFN flood ----------------------------------------------------------------

// tsfnFlood(total, concurrency, jsCallback) -> Promise<int delivered>.
// Every call carries a payload closure that invokes jsCallback with a
// sequence number; the promise resolves with the count the JS side observed
// (asserted equal to `total` by the driver).
func tsfnFlood(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	total, _ := napi.GetValueInt64(env, cb.Args[0])
	concurrency, _ := napi.GetValueInt64(env, cb.Args[1])
	jsCallback := cb.Args[2]

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
	// Deliberately NOT unref'd: a ref'd TSFN keeps the event loop alive
	// until the flood is fully dispatched, so the driver's await cannot be
	// abandoned by an empty loop.

	if concurrency < 1 {
		concurrency = 1
	}
	per := total / concurrency

	var wg sync.WaitGroup
	for c := int64(0); c < concurrency; c++ {
		wg.Add(1)
		go func(base int64) {
			defer wg.Done()
			for i := int64(0); i < per; i++ {
				seq := base*per + i
				_ = tsfn.Call(func(e napi.Env, fn napi.Value) {
					v, _ := napi.CreateInt64(e, seq)
					undef, _ := napi.GetUndefined(e)
					_, _ = napi.CallFunction(e, undef, fn, v)
				}, napi.NonBlocking)
			}
		}(c)
	}

	go func() {
		wg.Wait()
		// Drain marker: FIFO dispatch guarantees it runs after every queued
		// payload, so the queue is empty exactly when this runs. Release
		// from inside the payload — releasing earlier with a full queue
		// would make Node drop the pending payloads with a NULL env.
		err := tsfn.Call(func(e napi.Env, fn napi.Value) {
			out, _ := napi.CreateInt64(e, total)
			_ = napi.ResolveDeferred(e, deferred, out)
			tsfn.Release(napi.Release)
		}, napi.Blocking)
		if err != nil {
			// Could not enqueue the marker (closing etc.); reject so the
			// driver surfaces the failure instead of hanging.
			msg, _ := napi.CreateStringUtf8(env, "tsfn flood: marker enqueue failed: "+err.Error())
			_ = napi.RejectDeferred(env, deferred, msg)
			tsfn.Release(napi.Release)
		}
	}()
	return promise
}

// tsfnOverflow(total, jsCallback) -> Promise<int enqueued>.
// maxQueueSize=8 with NonBlocking pushes: most calls fail with
// napi_queue_full and must be safely dropped. The promise resolves with the
// number of calls actually enqueued; the driver asserts the JS side received
// exactly that many (no loss, no duplication).
func tsfnOverflow(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	total, _ := napi.GetValueInt64(env, cb.Args[0])
	jsCallback := cb.Args[1]

	promise, deferred, err := napi.CreatePromise(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	tsfn, err := napi.NewThreadsafeFunction(env, jsCallback, 8, 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	var enqueued atomic.Int64
	go func() {
		for i := int64(0); i < total; i++ {
			seq := i
			err := tsfn.Call(func(e napi.Env, fn napi.Value) {
				v, _ := napi.CreateInt64(e, seq)
				undef, _ := napi.GetUndefined(e)
				_, _ = napi.CallFunction(e, undef, fn, v)
			}, napi.NonBlocking)
			if err == nil {
				enqueued.Add(1)
			}
		}
		err := tsfn.Call(func(e napi.Env, fn napi.Value) {
			out, _ := napi.CreateInt64(e, enqueued.Load())
			_ = napi.ResolveDeferred(e, deferred, out)
			tsfn.Release(napi.Release)
		}, napi.Blocking)
		if err != nil {
			msg, _ := napi.CreateStringUtf8(env, "tsfn overflow: marker enqueue failed: "+err.Error())
			_ = napi.RejectDeferred(env, deferred, msg)
			tsfn.Release(napi.Release)
		}
	}()
	return promise
}

// ---- panic storm ------------------------------------------------------------------

func panicWith(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	i, _ := napi.GetValueInt64(env, cb.Args[0])
	panic(fmt.Sprintf("stress-boom-%d", i))
}

// ---- string roundtrips (encodings, size boundaries, embedded NUL, unicode) ---------

func stringRoundtrip(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	s, err := napi.GetValueStringUtf8(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "arg must be a string")
		return nil
	}
	mode, _ := napi.GetValueInt64(env, cb.Args[1])

	var out napi.Value
	switch mode {
	case 0: // utf8
		v, err2 := napi.CreateStringUtf8(env, s)
		if err2 != nil {
			napi.ThrowError(env, "", err2.Error())
			return nil
		}
		out = v
	case 1: // utf16
		units, err2 := napi.GetValueStringUtf16(env, cb.Args[0])
		if err2 != nil {
			napi.ThrowError(env, "", err2.Error())
			return nil
		}
		v, err2 := napi.CreateStringUtf16(env, units)
		if err2 != nil {
			napi.ThrowError(env, "", err2.Error())
			return nil
		}
		out = v
	case 2: // latin1 (only lossless for code points <= 0xFF)
		v, err2 := napi.CreateStringLatin1(env, s)
		if err2 != nil {
			napi.ThrowError(env, "", err2.Error())
			return nil
		}
		out = v
	default:
		napi.ThrowRangeError(env, "", "mode must be 0..2")
		return nil
	}

	res, err := napi.GetValueStringUtf8(env, out)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	r, _ := napi.CreateStringUtf8(env, res)
	return r
}

// bigString builds a Go string of roughly n KiB on the fly and returns it.
func bigString(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	kib, _ := napi.GetValueInt64(env, cb.Args[0])
	unit := "napi-go-字符串-🚀-0123456789-" // ~30 bytes utf8
	s := strings.Repeat(unit, int(kib)*34)
	out, err := napi.CreateStringUtf8(env, s)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return out
}

// ---- BigInt roundtrip ----------------------------------------------------------------

func bigintRoundtrip(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	sign, words, err := napi.GetValueBigIntWords(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected BigInt")
		return nil
	}
	out, err := napi.CreateBigIntWords(env, sign, words)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return out
}

// ---- buffer / arraybuffer ---------------------------------------------------------------

func makeFilledBuffer(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])
	v, data, err := napi.CreateBuffer(env, uint64(n))
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	for i := range data {
		data[i] = byte(i % 251)
	}
	return v
}

func bufferSum(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	bi, err := napi.GetBufferInfo(env, cb.Args[0])
	if err != nil {
		napi.ThrowTypeError(env, "", "expected Buffer")
		return nil
	}
	var sum uint64
	for _, b := range bi.Data {
		sum += uint64(b)
	}
	out, _ := napi.CreateBigIntUint64(env, sum)
	return out
}

// ---- async work flood ----------------------------------------------------------------------

// asyncTask(i) -> Promise<i>, resolved from the libuv thread pool.
var asyncSeq atomic.Int64

func asyncTask(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	i, _ := napi.GetValueInt64(env, cb.Args[0])

	promise, deferred, err := napi.CreatePromise(env)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	var work *napi.AsyncWork
	work, err = napi.NewAsyncWork(env, "stress-async",
		func(e napi.Env) { time.Sleep(time.Millisecond) },
		func(e napi.Env, werr error) {
			defer work.Delete(e)
			asyncSeq.Add(1)
			if werr != nil {
				msg, _ := napi.CreateStringUtf8(e, werr.Error())
				napi.RejectDeferred(e, deferred, msg)
				return
			}
			out, _ := napi.CreateInt64(e, i)
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

// ---- references ------------------------------------------------------------------------------

var strongRefs []napi.Ref

func refChurn(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt64(env, cb.Args[0])
	v, _ := napi.CreateObject(env)
	for i := int64(0); i < n; i++ {
		ref, err := napi.CreateReference(env, v, 1)
		if err != nil {
			napi.ThrowError(env, "", err.Error())
			return nil
		}
		if _, err := napi.GetReferenceValue(env, ref); err != nil {
			napi.ThrowError(env, "", err.Error())
			return nil
		}
		if err := napi.DeleteReference(env, ref); err != nil {
			napi.ThrowError(env, "", err.Error())
			return nil
		}
	}
	return nil
}

var weakRefs []napi.Ref

func keepWeak(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	ref, err := napi.CreateReference(env, cb.Args[0], 0)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	weakRefs = append(weakRefs, ref)
	out, _ := napi.CreateInt64(env, int64(len(weakRefs)-1))
	return out
}

func weakAliveCount(env napi.Env, info napi.CallbackInfo) napi.Value {
	alive := int64(0)
	for _, ref := range weakRefs {
		v, err := napi.GetReferenceValue(env, ref)
		if err == nil && v != nil {
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

// ---- reentrancy (Go -> JS -> Go ... deep) -------------------------------------------------------

func makeStepper(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)

	// jsLevel is only valid within this callback's handle scope. The
	// returned goStep will be called from LATER callbacks, so retain it
	// through a strong reference (see README lifetime rules).
	jsLevelRef, err := napi.CreateReference(env, cb.Args[0], 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}

	fn, err := napi.CreateFunction(env, "goStep", func(e napi.Env, ci napi.CallbackInfo) napi.Value {
		inner, _ := napi.GetCbInfo(e, ci)
		n, _ := napi.GetValueInt64(e, inner.Args[0])
		if n <= 0 {
			zero, _ := napi.CreateInt64(e, 0)
			return zero
		}
		jsLevel, err := napi.GetReferenceValue(e, jsLevelRef)
		if err != nil || jsLevel == nil {
			return nil
		}
		arg, _ := napi.CreateInt64(e, n-1)
		undef, _ := napi.GetUndefined(e)
		v, err := napi.CallFunction(e, undef, jsLevel, arg)
		if err != nil {
			return nil
		}
		return v
	})
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	// Tie the reference's lifetime to the stepper function: when JS
	// collects goStep, the strong reference is released too.
	if err := napi.AddFinalizer(env, fn, jsLevelRef, func(e napi.Env, data any) {
		if ref, ok := data.(napi.Ref); ok {
			_ = napi.DeleteReference(e, ref)
		}
	}); err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	return fn
}

func init() {
	entry.Export("noop", noop)
	entry.Export("addDoubles", addDoubles)
	entry.Export("churnRound", churnRound)
	entry.Export("churnObject", churnObject)
	entry.Export("newWrapped", newWrapped)
	entry.Export("wrapFinalizedCount", wrapFinalizedCount)
	entry.Export("createFn", createFn)
	entry.Export("tsfnFlood", tsfnFlood)
	entry.Export("tsfnOverflow", tsfnOverflow)
	entry.Export("panicWith", panicWith)
	entry.Export("stringRoundtrip", stringRoundtrip)
	entry.Export("bigString", bigString)
	entry.Export("bigintRoundtrip", bigintRoundtrip)
	entry.Export("makeFilledBuffer", makeFilledBuffer)
	entry.Export("bufferSum", bufferSum)
	entry.Export("asyncTask", asyncTask)
	entry.Export("refChurn", refChurn)
	entry.Export("keepWeak", keepWeak)
	entry.Export("weakAliveCount", weakAliveCount)
	entry.Export("resetWeak", resetWeak)
	entry.Export("makeStepper", makeStepper)
	entry.Export("debugStats", debugStats)
	entry.Export("goMem", goMem)
	entry.Export("freeGoMemory", freeGoMemory)
	entry.Export("drainFinalizers", drainFinalizers)
}

func main() {}
