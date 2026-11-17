package napi

import (
	"sync"
	"testing"
	"time"
)

// These tests cover the part of a threadsafe function's state that lives in
// Go: who may call, and how a Call that found the queue full is woken. None of
// them needs a Node.js host — the engine is never reached.

// waitOrFail fails the test if done is not closed in time.
func waitOrFail(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: still blocked after 5s", what)
	}
}

// A dispatch that lands between "the queue was full" and "start waiting" must
// not be lost: the waiter compares against the count it read before the
// attempt, not the count at the time it starts to wait.
func TestTSFNAwaitRoomDoesNotMissAnEarlierDispatch(t *testing.T) {
	st := newTSFNState(1)

	st.mu.Lock()
	seen := st.dispatched
	st.mu.Unlock()

	st.noteDispatch() // room appears before the caller gets to wait

	done := make(chan struct{})
	go func() {
		st.awaitRoom(seen)
		close(done)
	}()
	waitOrFail(t, done, "awaitRoom after an earlier dispatch")
}

// One dispatch frees one slot, so it needs to wake one waiter — and as many
// dispatches as there are waiters must bring all of them back, however the
// goroutines are scheduled.
func TestTSFNAwaitRoomWakesOnDispatch(t *testing.T) {
	st := newTSFNState(1)
	const waiters = 8

	var parked, woken sync.WaitGroup
	returned := make(chan struct{}, waiters)
	for i := 0; i < waiters; i++ {
		parked.Add(1)
		woken.Add(1)
		go func() {
			defer woken.Done()
			st.mu.Lock()
			seen := st.dispatched
			st.mu.Unlock()
			parked.Done() // the count is read; a dispatch from here on counts
			st.awaitRoom(seen)
			returned <- struct{}{}
		}()
	}
	parked.Wait()

	for i := 0; i < waiters; i++ {
		st.noteDispatch()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatalf("dispatch %d woke nobody", i+1)
		}
	}

	done := make(chan struct{})
	go func() {
		woken.Wait()
		close(done)
	}()
	waitOrFail(t, done, "waiters after as many dispatches")
}

// Closing has to wake every waiter, not one: this is the guarantee the engine's
// own blocking does not give.
func TestTSFNCloseWakesEveryWaiter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(*tsfnState)
		want  Status
	}{
		{"engine answered napi_closing", func(st *tsfnState) { st.markClosing(false) }, StatusClosing},
		{"abort", func(st *tsfnState) {
			st.mu.Lock()
			st.aborted = true
			st.cond.Broadcast()
			st.mu.Unlock()
		}, StatusClosing},
		{"last thread released", func(st *tsfnState) {
			st.mu.Lock()
			st.threads = 0
			st.cond.Broadcast()
			st.mu.Unlock()
		}, StatusClosing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTSFNState(1)
			const waiters = 16

			var parked, woken sync.WaitGroup
			for i := 0; i < waiters; i++ {
				parked.Add(1)
				woken.Add(1)
				go func() {
					defer woken.Done()
					st.mu.Lock()
					seen := st.dispatched
					st.mu.Unlock()
					parked.Done()
					st.awaitRoom(seen)
				}()
			}
			parked.Wait()

			tc.close(st)

			done := make(chan struct{})
			go func() {
				woken.Wait()
				close(done)
			}()
			waitOrFail(t, done, "waiters after "+tc.name)

			st.mu.Lock()
			err := st.unusableLocked("call")
			st.mu.Unlock()
			if code, ok := AsStatus(err); !ok || code != tc.want {
				t.Fatalf("unusableLocked = %v, want status %v", err, tc.want)
			}
		})
	}
}

// The engine takes the calling thread's count when it refuses a call with
// napi_closing, and leaves it alone when it refuses an acquire. The record has
// to follow, because the finalizer hands back exactly what it says is held.
func TestTSFNMarkClosingFollowsTheEngineCount(t *testing.T) {
	st := newTSFNState(3)

	st.markClosing(true) // a refused call
	if st.threads != 2 || !st.closing {
		t.Fatalf("after a refused call: threads=%d closing=%v, want 2 true", st.threads, st.closing)
	}
	st.markClosing(false) // a refused acquire
	if st.threads != 2 {
		t.Fatalf("after a refused acquire: threads=%d, want 2", st.threads)
	}

	// The count never goes below zero, whatever the engine reports.
	st = newTSFNState(0)
	st.markClosing(true)
	if st.threads != 0 {
		t.Fatalf("threads=%d, want 0", st.threads)
	}
}

// A handle that never came from NewThreadsafeFunction is refused by every
// method without touching anything.
func TestTSFNZeroValueIsRefused(t *testing.T) {
	var f ThreadsafeFunction
	_, contextErr := f.GetContext()
	checks := map[string]error{
		"Call":       f.Call(func(Env, Value) {}, NonBlocking),
		"Acquire":    f.Acquire(),
		"Release":    f.Release(Release),
		"Unref":      f.Unref(nil),
		"Ref":        f.Ref(nil),
		"GetContext": contextErr,
	}
	for name, err := range checks {
		if code, ok := AsStatus(err); !ok || code != StatusClosing {
			t.Errorf("%s on the zero value = %v, want napi_closing", name, err)
		}
	}
}
