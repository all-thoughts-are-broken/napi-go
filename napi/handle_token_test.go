package napi

import (
	"sync"
	"testing"
	"unsafe"
)

// These tests use put rather than issue: issue also registers a cleanup hook
// with the engine, and there is no engine in a test binary.

// fakePointers hands out distinct non-nil addresses to stand in for the
// engine's pointers and for environments. They are never dereferenced.
type fakePointers struct{ backing [64]byte }

func (f *fakePointers) at(i int) unsafe.Pointer { return unsafe.Pointer(&f.backing[i]) }

func TestTokenSetIssueResolveRetire(t *testing.T) {
	var f fakePointers
	s := &tokenSet{name: "test"}
	env := &envRecord{env: Env(f.at(0))}

	tok := s.put(env, f.at(1))
	if got, ok := s.resolve(tok); !ok || got != f.at(1) {
		t.Fatalf("resolve = (%p, %v), want the engine pointer", got, ok)
	}
	if got, ok := s.retire(tok); !ok || got != f.at(1) {
		t.Fatalf("retire = (%p, %v), want the engine pointer", got, ok)
	}
	// A retired token is gone for good: this is what makes "already released"
	// decidable.
	if _, ok := s.resolve(tok); ok {
		t.Fatal("a retired token still resolves")
	}
	if _, ok := s.retire(tok); ok {
		t.Fatal("a token was retired twice")
	}
	if _, ok := s.resolve(nil); ok {
		t.Fatal("the nil token resolves")
	}
}

// Two handles never share a token, even when the engine gives them the same
// address one after the other — the reason tokens exist.
func TestTokensAreDistinctForARecycledEngineAddress(t *testing.T) {
	var f fakePointers
	s := &tokenSet{name: "test"}
	env := &envRecord{env: Env(f.at(0))}

	stale := s.put(env, f.at(1))
	s.retire(stale)
	fresh := s.put(env, f.at(1)) // same engine address, new handle

	if stale == fresh {
		t.Fatal("a new handle was given a retired token")
	}
	if _, ok := s.resolve(stale); ok {
		t.Fatal("the stale token resolves to the handle that reused its address")
	}
	if _, ok := s.resolve(fresh); !ok {
		t.Fatal("the fresh token does not resolve")
	}
}

// A token of one family is not accepted by another.
func TestTokenSetsDoNotShareTokens(t *testing.T) {
	var f fakePointers
	refs, scopes := &tokenSet{name: "reference"}, &tokenSet{name: "handle scope"}
	tok := refs.put(&envRecord{env: Env(f.at(0))}, f.at(1))
	if _, ok := scopes.resolve(tok); ok {
		t.Fatal("a reference token resolved as a handle scope")
	}
}

// When an environment shuts down, the engine frees every handle it still
// owned. forgetEnv has to drop exactly those tokens, from every family, and
// leave the other environments' alone.
func TestForgetEnvDropsOnlyThatEnvironmentsTokens(t *testing.T) {
	var f fakePointers
	saved := tokenSets
	defer func() { tokenSets = saved }()
	a, b := &tokenSet{name: "a"}, &tokenSet{name: "b"}
	tokenSets = []*tokenSet{a, b}

	// Both stand for the same address: an environment and the one that
	// reused its address must still be told apart.
	dying, living := &envRecord{env: Env(f.at(0))}, &envRecord{env: Env(f.at(0))}
	var dead, alive []struct {
		set *tokenSet
		tok unsafe.Pointer
	}
	for i := 0; i < 20; i++ {
		for _, s := range tokenSets {
			dead = append(dead, struct {
				set *tokenSet
				tok unsafe.Pointer
			}{s, s.put(dying, f.at(2+i))})
			alive = append(alive, struct {
				set *tokenSet
				tok unsafe.Pointer
			}{s, s.put(living, f.at(2+i))})
		}
	}

	forgetEnv(dying)

	for _, h := range dead {
		if _, ok := h.set.resolve(h.tok); ok {
			t.Fatalf("a token of the environment that shut down still resolves (set %s)", h.set.name)
		}
	}
	for _, h := range alive {
		if _, ok := h.set.resolve(h.tok); !ok {
			t.Fatalf("a token of another environment was dropped (set %s)", h.set.name)
		}
	}
}

// Environments shut down on their own threads while others keep working.
func TestForgetEnvIsSafeAlongsideOtherEnvironments(t *testing.T) {
	var f fakePointers
	saved := tokenSets
	defer func() { tokenSets = saved }()
	s := &tokenSet{name: "test"}
	tokenSets = []*tokenSet{s}

	var wg sync.WaitGroup
	for e := 0; e < 8; e++ {
		wg.Add(1)
		go func(env *envRecord) {
			defer wg.Done()
			for round := 0; round < 200; round++ {
				toks := make([]unsafe.Pointer, 16)
				for i := range toks {
					toks[i] = s.put(env, f.at(32))
				}
				for _, tok := range toks[:8] {
					if _, ok := s.retire(tok); !ok {
						t.Error("a live token of a running environment was lost")
						return
					}
				}
				forgetEnv(env) // the rest go with the environment
				for _, tok := range toks[8:] {
					if _, ok := s.resolve(tok); ok {
						t.Error("a token outlived its environment")
						return
					}
				}
			}
		}(&envRecord{env: Env(f.at(e))})
	}
	wg.Wait()
}
