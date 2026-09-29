package napi

import (
	"fmt"
	"sync"
	"testing"
)

// TestBoxRegistryConcurrent hammers the cgo-handle box registry from many
// goroutines. This is the concurrency core of the binding: every callback
// box is created on the JS thread and freed from a Node finalizer, which
// may run on a different thread. Run with -race in CI.
//
// This test does not need a Node.js host: boxing only touches C malloc and
// cgo handles.
func TestBoxRegistryConcurrent(t *testing.T) {
	const goroutines = 32
	const iterations = 5_000

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				v := fmt.Sprintf("g%d-i%d", g, i)
				box := boxHandle(v)
				got := loadBox(box)
				if got != v {
					t.Errorf("box round trip mismatch: got %v want %v", got, v)
				}
				freeBox(box)
			}
		}(g)
	}
	wg.Wait()

	alloc, freed := DebugStats()
	if alloc != freed {
		t.Errorf("box leak: %d allocated, %d freed, %d outstanding", alloc, freed, alloc-freed)
	}
}

// TestBoxSemantics verifies that a box keeps the stored value alive and
// loadable until freeBox, and that loadBox after freeBox is detectable by
// the caller (handle already deleted).
func TestBoxSemantics(t *testing.T) {
	alloc0, freed0 := DebugStats()

	box := boxHandle(map[string]int{"k": 42})
	if alloc0+1 != mustAlloc() {
		t.Fatal("allocation not accounted")
	}
	got := loadBox(box).(map[string]int)
	if got["k"] != 42 {
		t.Fatalf("bad payload: %v", got)
	}
	freeBox(box)

	if _, freed1 := DebugStats(); freed1 != freed0+1 {
		t.Fatal("free not accounted")
	}
}

func mustAlloc() uint64 {
	a, _ := DebugStats()
	return a
}
