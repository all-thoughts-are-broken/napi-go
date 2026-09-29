package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

// This file provides the high-level wrappers for async work and threadsafe
// functions. Both are the only sanctioned ways to move data between plain Go
// goroutines and the JS thread:
//
//   - AsyncWork: run a Go function on the libuv thread pool, then deliver
//     the result to JS on the JS thread.
//   - ThreadsafeFunction: call a JS function from any goroutine, any number
//     of times, with an arbitrary payload closure.

// AsyncWork wraps napi_create_async_work.
//
// The handle is opaque and Node-API does not validate its state: deleting a
// work that is still queued, deleting it twice, or queueing a deleted one
// dereferences freed engine memory and corrupts the heap. The binding keeps
// its own state machine so those misuse patterns come back as Go errors
// instead of a dying process.
type AsyncWork struct {
	handle unsafe.Pointer
	box    unsafe.Pointer
	state  atomic.Int32
}

// Async work lifecycle states.
const (
	asyncIdle    int32 = iota // created, not queued
	asyncQueued               // queued, not yet completed
	asyncDone                 // execute+complete have run
	asyncDeleted              // handle released; no further use is legal
)

// NewAsyncWork creates a work item. execute runs on a libuv worker thread:
// it must not touch any Value or call Node-API functions that affect JS
// state. complete runs on the JS thread afterwards; its err argument is
// nil on success or the cancellation/failure status.
//
// The item must be released with Delete. Deleting it from the complete
// callback is the canonical pattern; an item that is never deleted keeps its
// callback data (and everything it references) alive for the lifetime of the
// process.
func NewAsyncWork(env Env, resourceName string, execute func(Env), complete func(Env, error)) (*AsyncWork, error) {
	name, err := CreateStringUtf8(env, resourceName)
	if err != nil {
		return nil, err
	}

	w := &AsyncWork{}
	d := &asyncWorkData{execute: execute, complete: complete, owner: w}
	box := boxHandle(d)
	w.box = box

	var result C.napi_async_work
	st := C.napi_create_async_work(
		C.napi_env(env), nil, C.napi_value(name),
		C.napi_async_execute_callback(C.napiGoAsyncExecute),
		C.napi_async_complete_callback(C.napiGoAsyncComplete),
		box,
		&result,
	)
	if st != C.napi_ok {
		freeBox(box)
		w.box = nil
		return nil, errOf(env, Status(st))
	}
	w.handle = unsafe.Pointer(result)
	return w, nil
}

// Queue schedules the work item. It may only be called once per item.
func (w *AsyncWork) Queue(env Env) error {
	switch w.state.Load() {
	case asyncDeleted:
		return &StatusError{Code: StatusInvalidArg, Message: "async work already deleted"}
	case asyncQueued, asyncDone:
		return &StatusError{Code: StatusInvalidArg, Message: "async work already queued"}
	}
	st := C.napi_queue_async_work(C.napi_env(env), C.napi_async_work(w.handle))
	if st != C.napi_ok {
		return errOf(env, Status(st))
	}
	w.state.Store(asyncQueued)
	return nil
}

// Cancel attempts to cancel a queued (not yet running) work item. The
// complete callback still runs, with a cancellation error.
func (w *AsyncWork) Cancel(env Env) error {
	switch w.state.Load() {
	case asyncDeleted:
		return &StatusError{Code: StatusInvalidArg, Message: "async work already deleted"}
	case asyncDone:
		return &StatusError{Code: StatusInvalidArg, Message: "async work already completed"}
	case asyncIdle:
		return &StatusError{Code: StatusInvalidArg, Message: "async work was never queued"}
	}
	st := C.napi_cancel_async_work(C.napi_env(env), C.napi_async_work(w.handle))
	return errOf(env, Status(st))
}

// Delete frees the work item. It is legal once the work has completed
// (including from inside its own complete callback) or if it was never
// queued; it is not legal while the work is still queued, and it is not
// idempotent — the engine's handle is gone afterwards.
func (w *AsyncWork) Delete(env Env) error {
	switch w.state.Load() {
	case asyncDeleted:
		return &StatusError{Code: StatusInvalidArg, Message: "async work already deleted"}
	case asyncQueued:
		return &StatusError{Code: StatusInvalidArg, Message: "async work is still queued; delete it from its complete callback"}
	}
	st := C.napi_delete_async_work(C.napi_env(env), C.napi_async_work(w.handle))
	if st != C.napi_ok {
		return errOf(env, Status(st))
	}
	w.state.Store(asyncDeleted)
	if w.box != nil {
		freeBox(w.box)
		w.box = nil
	}
	return nil
}

// ThreadsafeFunction wraps napi_threadsafe_function with full payload
// support: every Call schedules a Go closure to run on the JS thread with
// the JS function value in hand, so it can create arguments, call the
// function, or even ignore it.
//
// Every Acquire must be matched by exactly one Release. When the last thread
// releases, the function is done: further use is undefined in Node-API and
// refused by this binding. Release(napi.Abort) ends it the same way — the
// engine destroys the function on the next event-loop turn, so an aborted
// handle is retired outright and nothing further is forwarded, not even the
// Release of a thread that is still outstanding (see tsfnAbortedErr).
type ThreadsafeFunction struct {
	token unsafe.Pointer // identity token, never the engine pointer
}

// tsfnState mirrors the engine's own (thread_count, state) pair, which the
// binding has to keep because the engine never exposes it: node_api.cc's
// ThreadSafeFunction has three states (kOpen, kClosing, kClosed) and moves
// between them on the loop thread, after the call that caused the move has
// already returned.
//
// Two facts make the mirror necessary rather than optional:
//
//   - A normal Release leaves the engine in kOpen; only when the queue drains
//     does the loop thread decide kClosing, then kClosed, then run
//     ReleaseResources. Nothing observable from Go marks the transition.
//   - Release in Abort mode moves to kClosing while thread_count is still
//     positive, so "thread count above zero" does not mean "usable" — and the
//     engine then destroys the function on its next turn regardless of the
//     threads still outstanding.
//
// Counting threads without the abort flag was a real hole. After
// Release(Abort) on a two-thread function the count reads 1, so a stray Call
// was still forwarded; the engine's Push, on a function that is no longer open,
// does not merely refuse — it counts the last thread away and deletes the
// object. The Release that follows then locks the mutex of freed memory.
// Measured: 0xC0000028 (STATUS_ASSERTION_FAILURE, a deleted CRITICAL_SECTION)
// with no output at all.
type tsfnState struct {
	mu      sync.Mutex
	engine  unsafe.Pointer
	threads int
	aborted bool // Release(Abort) ran: the engine will destroy the function
}

// tsfnTable maps a live threadsafe function token to its state; the engine
// pointer lives inside the state, so it is reachable only through a live token.
var tsfnTable sync.Map // unsafe.Pointer(token) -> *tsfnState

func (f ThreadsafeFunction) state() *tsfnState {
	if f.token == nil {
		return nil
	}
	if v, ok := tsfnTable.Load(f.token); ok {
		return v.(*tsfnState)
	}
	return nil
}

// engine returns the raw pointer to hand the engine. Only call it on a state
// obtained from live() or state().
func (st *tsfnState) raw() C.napi_threadsafe_function {
	return C.napi_threadsafe_function(st.engine)
}

// live returns the state when the function can still be used, and an error
// saying why it cannot otherwise: a token this package never issued, every
// thread already released, or an abort that has left the engine's object
// unusable.
func (f ThreadsafeFunction) live(op string) (*tsfnState, error) {
	st := f.state()
	if st == nil {
		return nil, tsfnNotLiveErr(op)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.aborted {
		return nil, tsfnAbortedErr(op)
	}
	if st.threads <= 0 {
		return nil, tsfnNotLiveErr(op)
	}
	return st, nil
}

func tsfnNotLiveErr(op string) error {
	return &StatusError{
		Code: StatusClosing,
		Message: "threadsafe function is not live (" + op + "): every acquired thread has been " +
			"released or aborted already, or the handle did not come from NewThreadsafeFunction",
	}
}

// tsfnAbortedErr reports any use of a function that has been aborted.
//
// Abort is not a state the caller can work through. Node-API documents the
// function as staying alive until every thread has released, and
// napi_release_threadsafe_function in Release mode as the way to get there —
// but the engine destroys the function on the next event-loop turn regardless
// of how many threads are still outstanding, so the release that is supposed to
// finish the job faults instead. Measured with raw Node-API on this machine, no
// binding code involved, initial thread counts 1, 2 and 3, bounded and unbounded
// queues:
//
//	create + abort + release                    -> all napi_ok
//	create + abort, one loop turn, then acquire -> 0xC0000028
//	create + abort, one loop turn, then release -> 0xC0000028
//
// 0xC0000028 is STATUS_ASSERTION_FAILURE, which is what Windows raises when a
// deleted CRITICAL_SECTION is entered — the engine's mutex is gone. There is
// nothing left to forward, so the whole handle is retired here.
func tsfnAbortedErr(op string) error {
	return &StatusError{
		Code: StatusClosing,
		Message: "threadsafe function was aborted (" + op + "): the engine destroys it on the next " +
			"event-loop turn, so no further call on this handle is safe",
	}
}

// NewThreadsafeFunction wraps an existing JS function (or any callable).
// maxQueueSize bounds how many pending calls may queue up (0 = unbounded);
// initialThreadCount is how many threads may call it concurrently (start at
// 1 and use Acquire for more).
func NewThreadsafeFunction(env Env, jsFunc Value, maxQueueSize, initialThreadCount int) (ThreadsafeFunction, error) {
	if jsFunc == nil {
		return ThreadsafeFunction{}, &StatusError{Code: StatusInvalidArg, Message: "jsFunc must not be nil"}
	}

	// The engine requires a non-null resource name for async_hooks tracking.
	resourceName, err := CreateStringUtf8(env, "napi-go:threadsafe_function")
	if err != nil {
		return ThreadsafeFunction{}, err
	}

	var result C.napi_threadsafe_function
	st := C.napi_create_threadsafe_function(
		C.napi_env(env), C.napi_value(jsFunc),
		nil, C.napi_value(resourceName),
		C.size_t(maxQueueSize), C.size_t(initialThreadCount),
		nil, nil, // thread finalize
		nil, // context
		C.napi_threadsafe_function_call_js(C.napiGoTSFNCallJs),
		&result,
	)
	if st != C.napi_ok {
		return ThreadsafeFunction{}, errOf(env, Status(st))
	}
	f := ThreadsafeFunction{token: unsafe.Pointer(new(handleToken))}
	tsfnTable.Store(f.token, &tsfnState{engine: unsafe.Pointer(result), threads: initialThreadCount})
	return f, nil
}

// TSFNCallFunc is the payload of one dispatch: it runs on the JS thread with
// the wrapped JS function (nil if the function was released mid-flight).
type TSFNCallFunc func(env Env, jsFunc Value)

// Call schedules fn to run on the JS thread. Safe from any goroutine. The
// blocking variants block the calling goroutine while the queue is full.
//
// Calling after the last Release, or at all after an Abort, returns napi_closing
// instead of reaching the engine. Forwarding such a call is not harmless: see
// tsfnAbortedErr.
func (f ThreadsafeFunction) Call(fn TSFNCallFunc, mode ThreadsafeFunctionCallMode) error {
	st, err := f.live("call")
	if err != nil {
		return err
	}
	box := boxHandle(&tsfnCall{fn: fn})
	code := C.napi_call_threadsafe_function(
		st.raw(), box,
		C.napi_threadsafe_function_call_mode(mode),
	)
	if code != C.napi_ok {
		// The call was not queued (closing / queue full); nobody else will
		// free the box for us.
		freeBox(box)
		return &StatusError{Code: Status(code)}
	}
	return nil
}

// GetContext returns the context pointer passed at creation time (nil in
// this binding; provided for API completeness).
func (f ThreadsafeFunction) GetContext() (unsafe.Pointer, error) {
	st, err := f.live("get context")
	if err != nil {
		return nil, err
	}
	var ctx unsafe.Pointer
	code := C.napi_get_threadsafe_function_context(st.raw(), &ctx)
	return ctx, errOf(nil, Status(code))
}

// Acquire registers another thread (returns an error once the function is
// closing). Every Acquire must be matched by a Release.
//
// Acquiring after the last Release, or after an Abort, is refused: the engine
// would allow the first and faults on the second, and a revived thread count is
// what keeps the process from ever exiting.
func (f ThreadsafeFunction) Acquire() error {
	st, err := f.live("acquire")
	if err != nil {
		return err
	}
	if code := C.napi_acquire_threadsafe_function(st.raw()); code != C.napi_ok {
		return errOf(nil, Status(code))
	}
	st.mu.Lock()
	st.threads++
	st.mu.Unlock()
	return nil
}

// Release drops one acquired thread. With mode Release the function closes once
// the count reaches zero; Abort closes it immediately, dropping queued calls
// (note: dropped payloads are never dispatched and their closures are garbage
// collected with the cgo handle when the process exits).
//
// Every Acquire must be matched by exactly one Release, and an unmatched extra
// Release is reported rather than forwarded — that one really is a mistake, and
// it is what leaves the environment pinned so the process cannot exit.
//
// Release is refused once the function has been aborted, including the release
// of a thread that is still outstanding. Node-API's documentation says that
// release is how an aborted function is finished off, but the engine destroys
// the function on the next event-loop turn instead, so the call faults; see
// tsfnAbortedErr. Nothing about the abort trap can be repaired from here, so
// the caller is told rather than allowed to crash.
func (f ThreadsafeFunction) Release(mode ThreadsafeFunctionReleaseMode) error {
	st := f.state()
	if st == nil {
		return tsfnNotLiveErr("release")
	}
	st.mu.Lock()
	if st.aborted {
		st.mu.Unlock()
		return tsfnAbortedErr("release")
	}
	if st.threads <= 0 {
		st.mu.Unlock()
		return &StatusError{
			Code:    StatusInvalidArg,
			Message: "threadsafe function has no acquired thread left to release",
		}
	}
	st.threads--
	if mode == Abort {
		// The engine moves to kClosing without touching thread_count and never
		// moves back; the loop then tears the function down on its next turn.
		st.aborted = true
	}
	last := st.threads == 0
	st.mu.Unlock()

	code := C.napi_release_threadsafe_function(st.raw(), C.napi_threadsafe_function_release_mode(mode))
	if code != C.napi_ok {
		st.mu.Lock()
		st.threads++ // the engine refused; keep the two counts in step
		st.mu.Unlock()
		return errOf(nil, Status(code))
	}
	if last {
		// The engine finishes tearing the function down on the loop thread;
		// forgetting the token here is what makes every later call report "not
		// live" instead of reviving it.
		tsfnTable.Delete(f.token)
	}
	return nil
}

// Unref detaches the function from the event loop so it does not keep the
// process alive by itself.
func (f ThreadsafeFunction) Unref(env Env) error {
	st, err := f.live("unref")
	if err != nil {
		return err
	}
	code := C.napi_unref_threadsafe_function(C.napi_env(env), st.raw())
	return errOf(env, Status(code))
}

// Ref re-attaches the function to the event loop.
func (f ThreadsafeFunction) Ref(env Env) error {
	st, err := f.live("ref")
	if err != nil {
		return err
	}
	code := C.napi_ref_threadsafe_function(C.napi_env(env), st.raw())
	return errOf(env, Status(code))
}
