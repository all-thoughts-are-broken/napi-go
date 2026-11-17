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
// Every Acquire must be matched by exactly one Release. The function stops
// being usable, and every method answers napi_closing from then on, when
//
//   - the last thread releases it;
//   - any thread releases it with napi.Abort; or
//   - its environment shuts down — the Worker that created it ends, or the
//     process exits.
//
// The third happens without any call from Go. A goroutine looping over Call
// learns of it from the error, and should stop; nothing it does with the handle
// afterwards reaches the engine, including the Release it still "owes".
//
// The zero ThreadsafeFunction is not usable.
type ThreadsafeFunction struct {
	// state is the handle's identity. It is a Go object, so it is unique by
	// construction and cannot be mistaken for a recycled engine address; the
	// engine pointer lives inside it and is only reachable while it is valid.
	state *tsfnState
}

// tsfnState is the binding's own record of one threadsafe function.
//
// The object behind a napi_threadsafe_function is freed by the engine, on the
// loop thread, at a moment no call from Go chooses — and every function that
// takes the handle dereferences it unchecked. Two generations of the engine
// disagree about when that moment is (node_api.cc, class ThreadSafeFunction):
//
//   - Node.js 18, 20, 22 and 24 up to 24.13 delete the object as soon as it has
//     been finalized, whatever its thread count says. Finalization follows the
//     last release, an abort, or the environment shutting down, so a thread that
//     still "holds" the function is left with a dangling pointer. Measured on
//     22.22.2: a goroutine calling a function whose Worker had been terminated
//     is 0xC0000005; a release one loop turn after an abort is 0xC0000028.
//   - Node.js 24.14 and later keep the finalized object until every thread has
//     released it, and the release that takes the count to zero deletes it.
//     There the use-after-free is one call too many.
//
// What both agree on is the finalize callback: it runs on the loop thread while
// the object is still allocated, before either kind of deletion. The state is
// built around that one point.
//
//   - alive guards the engine pointer. Every call into the engine holds it for
//     reading; the finalizer takes it for writing, so it waits out the calls
//     already inside the engine, and none can start afterwards.
//   - Inside the finalizer, with the object certainly alive, the binding hands
//     back every thread count Go still holds. The newer engine can then delete
//     the object straight away instead of waiting for releases that will never
//     be forwarded; to the older one it makes no difference.
//   - From then on the pointer is dead to this package: engine is nil and every
//     method answers napi_closing.
//
// Holding alive across an engine call is only sound if the call cannot block.
// That is one of the two reasons Call never asks the engine to block; see Call.
type tsfnState struct {
	alive  sync.RWMutex
	engine unsafe.Pointer // guarded by alive; nil once finalized

	mu   sync.Mutex
	cond *sync.Cond // on mu: a payload was dispatched, or the state changed

	// threads is how many thread counts are held through this package. It
	// tracks the engine's own count: every engine call that changes one changes
	// the other.
	threads int
	// aborted records that Release(Abort) ran.
	aborted bool
	// closing records that the engine is tearing the function down: it
	// answered napi_closing, or its finalizer has run.
	closing bool
	// dispatched counts the payloads the engine has taken off the queue. A
	// blocked Call watches it to know when there may be room again.
	dispatched uint64
}

func newTSFNState(threads int) *tsfnState {
	st := &tsfnState{threads: threads}
	st.cond = sync.NewCond(&st.mu)
	return st
}

// raw returns the pointer to hand the engine. alive must be held.
func (st *tsfnState) raw() C.napi_threadsafe_function {
	return C.napi_threadsafe_function(st.engine)
}

// unusableLocked returns why the function cannot be used, or nil when it can.
// mu must be held.
func (st *tsfnState) unusableLocked(op string) error {
	switch {
	case st.aborted:
		return tsfnAbortedErr(op)
	case st.closing:
		return tsfnClosingErr(op)
	case st.threads <= 0:
		return tsfnNotLiveErr(op)
	}
	return nil
}

// enter begins a call into the engine. On success it returns with alive held
// for reading — the engine object cannot be finalized until the caller unlocks
// it — and the caller must call st.alive.RUnlock.
func (st *tsfnState) enter(op string) error {
	st.alive.RLock()
	st.mu.Lock()
	err := st.unusableLocked(op)
	st.mu.Unlock()
	if err != nil {
		st.alive.RUnlock()
	}
	return err
}

// markClosing records that the engine answered napi_closing. consumed says
// whether the engine also took the calling thread's count, which
// napi_call_threadsafe_function does and napi_acquire_threadsafe_function does
// not.
func (st *tsfnState) markClosing(consumed bool) {
	st.mu.Lock()
	st.closing = true
	if consumed && st.threads > 0 {
		st.threads--
	}
	st.cond.Broadcast()
	st.mu.Unlock()
}

// noteDispatch records that the engine took one payload off the queue, which
// is the moment a slot becomes free. It runs on the loop thread.
func (st *tsfnState) noteDispatch() {
	st.mu.Lock()
	st.dispatched++
	st.cond.Signal() // one slot, one waiter
	st.mu.Unlock()
}

// finalize is the engine's notice that the function is about to go away. It
// runs on the loop thread, from the finalize callback, while the engine object
// is still allocated.
func (st *tsfnState) finalize() {
	// Turn away new calls and wake every Call waiting for room.
	st.mu.Lock()
	st.closing = true
	st.cond.Broadcast()
	st.mu.Unlock()

	// Wait for the calls already inside the engine. They cannot block there,
	// so this is bounded by the engine's own short critical sections.
	st.alive.Lock()
	defer st.alive.Unlock()

	// Hand back what Go still holds. See the type comment: this is what lets
	// an engine that waits for every thread delete the object now.
	st.mu.Lock()
	held := st.threads
	st.threads = 0
	st.mu.Unlock()
	for ; held > 0; held-- {
		if C.napi_release_threadsafe_function(st.raw(), C.napi_tsfn_release) != C.napi_ok {
			break
		}
	}
	st.engine = nil
}

func tsfnNotLiveErr(op string) error {
	return &StatusError{
		Code: StatusClosing,
		Message: "threadsafe function is not live (" + op + "): every acquired thread has been " +
			"released already, or the handle did not come from NewThreadsafeFunction",
	}
}

// tsfnClosingErr reports a function the engine has started to tear down
// without being asked to by this handle: its environment is shutting down.
func tsfnClosingErr(op string) error {
	return &StatusError{
		Code: StatusClosing,
		Message: "threadsafe function is closing (" + op + "): its environment is shutting down or " +
			"has shut down, so no further call on this handle is safe",
	}
}

// tsfnAbortedErr reports any use of a function that has been aborted.
//
// Abort is not a state the caller can work through. Node-API documents the
// function as staying alive until every thread has released, and
// napi_release_threadsafe_function in Release mode as the way to get there. On
// the engines that delete the object at finalization (see tsfnState) that
// release is the fault itself. Measured with raw Node-API on Node.js 22.22.2,
// no binding code involved, initial thread counts 1, 2 and 3, bounded and
// unbounded queues:
//
//	create + abort + release                    -> all napi_ok
//	create + abort, one loop turn, then acquire -> 0xC0000028
//	create + abort, one loop turn, then release -> 0xC0000028
//
// 0xC0000028 is STATUS_ASSERTION_FAILURE, which is what Windows raises when a
// deleted CRITICAL_SECTION is entered — the engine's mutex is gone. So the
// whole handle is retired at the abort, and the counts the other threads still
// hold are handed back by the finalizer, where it is safe on every engine.
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

	st := newTSFNState(initialThreadCount)

	// The finalize callback is how the binding learns that the engine object
	// is going away; its data is a box holding the state, released by
	// napiGoTSFNFinalize.
	finalizeBox := boxHandle(st)

	var result C.napi_threadsafe_function
	code := C.napi_create_threadsafe_function(
		C.napi_env(env), C.napi_value(jsFunc),
		nil, C.napi_value(resourceName),
		C.size_t(maxQueueSize), C.size_t(initialThreadCount),
		finalizeBox, C.napi_finalize(C.napiGoTSFNFinalize), // thread finalize
		nil, // context
		C.napi_threadsafe_function_call_js(C.napiGoTSFNCallJs),
		&result,
	)
	if code != C.napi_ok {
		// No function was created, so the finalizer will never run.
		freeBox(finalizeBox)
		return ThreadsafeFunction{}, errOf(env, Status(code))
	}
	st.engine = unsafe.Pointer(result)
	return ThreadsafeFunction{state: st}, nil
}

// TSFNCallFunc is the payload of one dispatch: it runs on the JS thread with
// the wrapped JS function (nil if the function was released mid-flight).
type TSFNCallFunc func(env Env, jsFunc Value)

// Call schedules fn to run on the JS thread. Safe from any goroutine. With
// napi.Blocking it waits while the queue is full; with napi.NonBlocking it
// returns napi_queue_full instead.
//
// Calling after the last Release, after an Abort, or once the environment has
// begun to shut down returns napi_closing instead of reaching the engine. When
// that error comes back, stop using the handle: there is nothing left to
// release.
//
// The waiting is done here, not in the engine — the engine is only ever asked
// to queue without blocking, and a full queue is retried when a payload has
// been dispatched. Two properties of the engine's own blocking make it
// unusable:
//
//   - It wakes one waiter when the function closes, not all of them. Measured:
//     four goroutines blocked behind a full queue of one, then an abort — one
//     came back with napi_closing and three stayed blocked for good, on an
//     object the engine went on to delete.
//   - A thread blocked inside the engine cannot be waited out, so the finalizer
//     could not make sure nobody is still in there (see tsfnState).
func (f ThreadsafeFunction) Call(fn TSFNCallFunc, mode ThreadsafeFunctionCallMode) error {
	st := f.state
	if st == nil {
		return tsfnNotLiveErr("call")
	}
	st.mu.Lock()
	err := st.unusableLocked("call")
	st.mu.Unlock()
	if err != nil {
		return err
	}

	box := boxHandle(&tsfnCall{fn: fn, owner: st})
	for {
		code, seen, err := st.push(box)
		if err != nil {
			freeBox(box)
			return err
		}
		switch {
		case code == C.napi_ok:
			return nil // the box now belongs to the queue
		case code == C.napi_queue_full && mode != NonBlocking:
			st.awaitRoom(seen)
		case code == C.napi_closing:
			freeBox(box)
			return tsfnClosingErr("call")
		default:
			// Not queued (the queue is full, or the engine refused); nobody
			// else will free the box.
			freeBox(box)
			return &StatusError{Code: Status(code)}
		}
	}
}

// push offers one payload to the engine without blocking. seen is the dispatch
// count read before the attempt: if the queue turns out to be full, any
// dispatch after that reading may have made room, so the caller waits for the
// count to move past it and cannot miss a wake-up.
func (st *tsfnState) push(box unsafe.Pointer) (code C.napi_status, seen uint64, err error) {
	st.alive.RLock()
	defer st.alive.RUnlock()

	st.mu.Lock()
	err = st.unusableLocked("call")
	seen = st.dispatched
	st.mu.Unlock()
	if err != nil {
		return 0, 0, err
	}

	code = C.napi_call_threadsafe_function(st.raw(), box, C.napi_tsfn_nonblocking)
	if code == C.napi_closing {
		// The engine took this thread's count along with the refusal.
		st.markClosing(true)
	}
	return code, seen, nil
}

// awaitRoom blocks until a payload has been dispatched since seen, or the
// function stops being usable.
func (st *tsfnState) awaitRoom(seen uint64) {
	st.mu.Lock()
	for st.dispatched == seen && !st.aborted && !st.closing && st.threads > 0 {
		st.cond.Wait()
	}
	st.mu.Unlock()
}

// GetContext returns the context pointer passed at creation time (nil in
// this binding; provided for API completeness).
func (f ThreadsafeFunction) GetContext() (unsafe.Pointer, error) {
	st := f.state
	if st == nil {
		return nil, tsfnNotLiveErr("get context")
	}
	if err := st.enter("get context"); err != nil {
		return nil, err
	}
	defer st.alive.RUnlock()

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
	st := f.state
	if st == nil {
		return tsfnNotLiveErr("acquire")
	}
	if err := st.enter("acquire"); err != nil {
		return err
	}
	defer st.alive.RUnlock()

	switch code := C.napi_acquire_threadsafe_function(st.raw()); code {
	case C.napi_ok:
		st.mu.Lock()
		st.threads++
		st.mu.Unlock()
		return nil
	case C.napi_closing:
		st.markClosing(false)
		return tsfnClosingErr("acquire")
	default:
		return errOf(nil, Status(code))
	}
}

// Release drops one acquired thread. With mode Release the function closes once
// the count reaches zero and the queue has drained; Abort closes it at once.
// Payloads still queued when it closes are never dispatched: the engine hands
// each one back without an environment and the binding discards it.
//
// Every Acquire must be matched by exactly one Release, and an unmatched extra
// Release is reported rather than forwarded — that one really is a mistake, and
// it is what leaves the environment pinned so the process cannot exit.
//
// Release is refused once the function has been aborted or its environment has
// begun to shut down, including the release of a thread that is still
// outstanding. Node-API's documentation says that release is how such a
// function is finished off, but on the engines that delete the object at
// finalization the call faults; see tsfnAbortedErr. The binding returns those
// outstanding counts itself, from the finalizer, so the caller has nothing
// left to do and is told so.
func (f ThreadsafeFunction) Release(mode ThreadsafeFunctionReleaseMode) error {
	st := f.state
	if st == nil {
		return tsfnNotLiveErr("release")
	}
	st.alive.RLock()
	defer st.alive.RUnlock()

	st.mu.Lock()
	if err := st.unusableLocked("release"); err != nil {
		st.mu.Unlock()
		return err
	}
	st.threads--
	if mode == Abort {
		// The engine starts closing without waiting for the other threads and
		// never goes back.
		st.aborted = true
	}
	st.cond.Broadcast() // a waiting Call has to look again
	st.mu.Unlock()

	code := C.napi_release_threadsafe_function(st.raw(), C.napi_threadsafe_function_release_mode(mode))
	if code != C.napi_ok {
		// The engine refused; keep the two records in step.
		st.mu.Lock()
		st.threads++
		if mode == Abort {
			st.aborted = false
		}
		st.mu.Unlock()
		return errOf(nil, Status(code))
	}
	return nil
}

// Unref detaches the function from the event loop so it does not keep the
// process alive by itself.
func (f ThreadsafeFunction) Unref(env Env) error {
	st := f.state
	if st == nil {
		return tsfnNotLiveErr("unref")
	}
	if err := st.enter("unref"); err != nil {
		return err
	}
	defer st.alive.RUnlock()

	code := C.napi_unref_threadsafe_function(C.napi_env(env), st.raw())
	return errOf(env, Status(code))
}

// Ref re-attaches the function to the event loop.
func (f ThreadsafeFunction) Ref(env Env) error {
	st := f.state
	if st == nil {
		return tsfnNotLiveErr("ref")
	}
	if err := st.enter("ref"); err != nil {
		return err
	}
	defer st.alive.RUnlock()

	code := C.napi_ref_threadsafe_function(C.napi_env(env), st.raw())
	return errOf(env, Status(code))
}
