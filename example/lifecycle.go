package main

// Exports behind the "process lifecycle" checks of test.mjs: what happens to a
// threadsafe function when the code using it and the environment owning it
// disagree about how long it lives.

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// statusText renders an error as the napi_status name it carries.
func statusText(err error) string {
	if err == nil {
		return "ok"
	}
	if st, ok := napi.AsStatus(err); ok {
		return st.String()
	}
	return err.Error()
}

// tickerReport is what the goroutine started by tsfnTicker observed. It is a
// package-level value on purpose: the environment that started the goroutine is
// gone by the time anybody asks, and the Go runtime is shared by every
// environment that loaded this file.
var tickerReport struct {
	sync.Mutex
	done       bool
	dispatched int64
	callErr    string
	releaseErr string
	lateErr    string
}

// tsfnTicker(fn) starts the textbook goroutine loop over a threadsafe
// function: call it until a call fails, then release the thread. It then
// calls once more, the way a retry loop that missed the error would.
func tsfnTicker(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	tsfn, err := napi.NewThreadsafeFunction(env, cb.Args[0], 0, 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	go func() {
		var dispatched atomic.Int64
		var callErr error
		for callErr == nil {
			callErr = tsfn.Call(func(napi.Env, napi.Value) { dispatched.Add(1) }, napi.Blocking)
			time.Sleep(time.Millisecond)
		}
		releaseErr := tsfn.Release(napi.Release)
		lateErr := tsfn.Call(func(napi.Env, napi.Value) {}, napi.NonBlocking)

		tickerReport.Lock()
		defer tickerReport.Unlock()
		tickerReport.done = true
		tickerReport.dispatched = dispatched.Load()
		tickerReport.callErr = statusText(callErr)
		tickerReport.releaseErr = statusText(releaseErr)
		tickerReport.lateErr = statusText(lateErr)
	}()
	return nil
}

// tsfnTickerReport() returns what the tsfnTicker goroutine saw.
func tsfnTickerReport(env napi.Env, _ napi.CallbackInfo) napi.Value {
	tickerReport.Lock()
	defer tickerReport.Unlock()
	out, _ := napi.CreateObject(env)
	setBoolProp(env, out, "done", tickerReport.done)
	setBoolProp(env, out, "dispatchedSome", tickerReport.dispatched > 0)
	setStringProp(env, out, "callErr", tickerReport.callErr)
	setStringProp(env, out, "releaseErr", tickerReport.releaseErr)
	setStringProp(env, out, "lateErr", tickerReport.lateErr)
	return out
}

// tsfnAbortBlocked(fn, n) fills a queue of one, parks n goroutines on blocking
// calls behind it, aborts the function, and returns what each of them got.
//
// Everything happens inside this one callback, so the JS thread never gets to
// dispatch and the queue stays full for as long as the goroutines wait.
func tsfnAbortBlocked(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	n, _ := napi.GetValueInt32(env, cb.Args[1])

	tsfn, err := napi.NewThreadsafeFunction(env, cb.Args[0], 1, 1)
	if err != nil {
		napi.ThrowError(env, "", err.Error())
		return nil
	}
	noop := func(napi.Env, napi.Value) {}
	first := tsfn.Call(noop, napi.NonBlocking)  // takes the only slot
	second := tsfn.Call(noop, napi.NonBlocking) // must be refused

	results := make(chan string, n)
	var parked sync.WaitGroup
	for i := int32(0); i < n; i++ {
		if err := tsfn.Acquire(); err != nil {
			napi.ThrowError(env, "", err.Error())
			return nil
		}
		parked.Add(1)
		go func() {
			parked.Done()
			results <- statusText(tsfn.Call(noop, napi.Blocking))
		}()
	}
	parked.Wait()
	time.Sleep(100 * time.Millisecond) // let every goroutine reach its wait

	abortErr := tsfn.Release(napi.Abort)

	// Give the goroutines three seconds between them to come back; whoever
	// has not by then is reported as still blocked.
	blocked, _ := napi.CreateArrayWithLength(env, int(n))
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	expired := false
	for i := int32(0); i < n; i++ {
		got := "still blocked"
		if expired {
			select {
			case got = <-results:
			default:
			}
		} else {
			select {
			case got = <-results:
			case <-deadline.C:
				expired = true
			}
		}
		v, _ := napi.CreateStringUtf8(env, got)
		napi.SetElement(env, blocked, uint32(i), v)
	}

	out, _ := napi.CreateObject(env)
	setStringProp(env, out, "first", statusText(first))
	setStringProp(env, out, "second", statusText(second))
	setStringProp(env, out, "abort", statusText(abortErr))
	napi.SetNamedProperty(env, out, "blocked", blocked)
	return out
}

func init() {
	entry.Export("tsfnTicker", tsfnTicker)
	entry.Export("tsfnTickerReport", tsfnTickerReport)
	entry.Export("tsfnAbortBlocked", tsfnAbortBlocked)
}
