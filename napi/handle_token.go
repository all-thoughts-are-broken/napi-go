package napi

/*
#include "napi_preamble.h"
*/
import "C"

import (
	"sync"
	"unsafe"
)

// This file gives every engine-backed handle a Go-side identity.
//
// The engine hands out raw pointers (napi_ref, napi_deferred, napi_async_context,
// napi_handle_scope, ...) and validates nothing about them, so this package
// keeps its own liveness records and rejects handles it has already retired.
// Keying those records on the engine pointer alone is not sound, because the
// engine's allocator recycles addresses: the moment a handle is released, the
// next allocation of the same size can land on the very same address, and the
// stale handle then *names a live object*. The liveness lookup succeeds, the
// operation is forwarded, and it destroys or corrupts something the caller
// never mentioned.
//
// That is not a corner case. Measured on this machine, by creating and
// releasing handles until the allocator handed the address back:
//
//	napi_ref         28 of 64 rounds reused the address, 28 of 28 accepted
//	handle scopes     8 of 64 rounds reused the address,  8 of  8 accepted
//	napi_deferred     1 of 64 rounds reused the address,  1 of  1 accepted
//	async context     1 of 64 rounds reused the address,  1 of  1 accepted
//
// Every accepted one harmed a live object: a live reference deleted, a live
// scope closed, a live promise settled by a handle to a dead one, a live async
// context destroyed. For promises the damage is silent — the wrong promise
// resolves and the right one stays pending forever.
//
// The fix is to stop using the engine pointer as the caller's identity. A token
// is a separate Go allocation:
//
//   - Go never gives two live objects the same address, so a token identifies
//     one handle unambiguously.
//   - The caller's variable is an unsafe.Pointer, which the collector scans,
//     and this file's table holds the token while the handle is live — so the
//     token stays reachable from both sides and its address cannot be recycled
//     underneath either owner.
//   - A retired token therefore stays distinguishable forever: it is gone from
//     the table, and a fresh handle is a fresh allocation at a different
//     address. "Already released" stops being a guess.
//
// The engine pointer itself lives only in the table, so the only way to reach it
// is through a token that is currently live.

// handleToken is the object whose address serves as a handle's identity. It is
// never dereferenced by this package: the address alone is the token, and the
// engine pointer it stands for is kept in the tokenSet that issued it. (A
// pointer-valued struct rather than a byte array, so the allocator cannot fold
// it into a shared tiny-allocation block.)
type handleToken struct {
	engine unsafe.Pointer
}

// tokenEntry is what a live token stands for: the engine's pointer, and the
// environment that owns it.
type tokenEntry struct {
	engine unsafe.Pointer
	owner  *envRecord
}

// tokenSet maps live tokens to the engine pointers they stand for.
//
// One set per handle family, deliberately: a Ref token presented where a
// HandleScope is expected is then rejected rather than silently reinterpreted.
type tokenSet struct {
	name string
	m    sync.Map // unsafe.Pointer(token) -> tokenEntry
}

// tokenSets lists every set, so that an environment's handles can be dropped
// from all of them once it has shut down. It is only appended to while the
// package's variables are initialised.
var tokenSets []*tokenSet

func newTokenSet(name string) *tokenSet {
	s := &tokenSet{name: name}
	tokenSets = append(tokenSets, s)
	return s
}

// issue records a new handle of env and returns the token that identifies it.
func (s *tokenSet) issue(env Env, engine unsafe.Pointer) unsafe.Pointer {
	return s.put(trackEnv(env), engine)
}

// put is issue for an environment that is already on record.
func (s *tokenSet) put(owner *envRecord, engine unsafe.Pointer) unsafe.Pointer {
	tok := unsafe.Pointer(new(handleToken))
	s.m.Store(tok, tokenEntry{engine: engine, owner: owner})
	return tok
}

// ---- forgetting an environment's handles ------------------------------------
//
// An environment frees every handle it still owns when it shuts down, whether
// or not the addon released them — and addons routinely do not: a reference to
// a constructor or a callback is kept for as long as the environment runs. The
// engine pointers behind those tokens are dead from then on, so the tokens
// have to go too. They used to stay in their tables for the life of the
// process; measured with Workers that each kept 5000 references, 109 bytes per
// reference were never given back.
//
// *When* they go matters. The environment runs the addon's remaining
// finalizers as the last step of its shutdown, and a finalizer deleting a
// reference it owns is the documented way to release one — that
// DeleteReference has to find its token. Dropping the tokens from an ordinary
// cleanup hook is too early: hooks run before those finalizers, and the delete
// came back napi_invalid_arg (measured). So it takes two hooks:
//
//  1. napiGoEnvShutdown, registered when the environment's first token is
//     issued, runs with the other cleanup hooks. It only takes the environment
//     off the live list and registers the second hook.
//  2. napiGoEnvGone runs in the next round. Node.js drains its cleanup hooks in
//     batches and a hook added during a batch waits for the following one,
//     which comes after the hook that tears the Node-API environment down and
//     runs its finalizers. Only then are the tokens dropped.

// envRecord identifies one environment for as long as it lives. Tokens point
// at the record, not at the Env: the engine reuses an environment's address
// for the next one, and a new Worker must not be mistaken for the one that
// just went away.
type envRecord struct {
	env Env
}

// liveEnvs holds the record of every environment whose shutdown hook is
// registered. The engine aborts on a second registration of the same hook and
// argument, so there is exactly one per environment.
var liveEnvs sync.Map // Env -> *envRecord

// goneEnvs holds the records of environments between the two hooks, oldest
// first per address.
var goneEnvs struct {
	sync.Mutex
	byAddr map[Env][]*envRecord
}

// trackEnv returns the record of env, registering its shutdown hook the first
// time env is seen.
func trackEnv(env Env) *envRecord {
	if env == nil {
		return nil
	}
	if rec, ok := liveEnvs.Load(env); ok {
		return rec.(*envRecord)
	}
	rec := &envRecord{env: env}
	if other, raced := liveEnvs.LoadOrStore(env, rec); raced {
		return other.(*envRecord)
	}
	// The hook's argument is the environment itself: an engine pointer used as
	// an identity, so there is no box and nothing to free.
	st := C.napi_add_env_cleanup_hook(
		C.napi_env(env), C.napi_cleanup_hook(C.napiGoEnvShutdown), unsafe.Pointer(env))
	if st != C.napi_ok {
		// No notice will come. The tokens of this record simply stay until
		// they are released, as all tokens used to.
		liveEnvs.Delete(env)
	}
	return rec
}

// envShuttingDown is the first hook: env has started to shut down, but its
// finalizers have not run yet and may still use its tokens.
func envShuttingDown(env Env) {
	v, ok := liveEnvs.LoadAndDelete(env)
	if !ok {
		return
	}
	rec := v.(*envRecord)

	goneEnvs.Lock()
	if goneEnvs.byAddr == nil {
		goneEnvs.byAddr = make(map[Env][]*envRecord)
	}
	goneEnvs.byAddr[env] = append(goneEnvs.byAddr[env], rec)
	goneEnvs.Unlock()

	// A different function from the one running now: the engine has not yet
	// taken this hook off its list, and a duplicate registration aborts.
	st := C.napi_add_env_cleanup_hook(
		C.napi_env(env), C.napi_cleanup_hook(C.napiGoEnvGone), unsafe.Pointer(env))
	if st != C.napi_ok {
		// There will be no second round. Dropping the tokens now is the lesser
		// evil: a finalizer's delete is refused, but nothing leaks here.
		envGone(env)
	}
}

// envGone is the second hook: the environment's finalizers have run and its
// address is free to be reused, so its tokens stand for nothing.
func envGone(env Env) {
	goneEnvs.Lock()
	queue := goneEnvs.byAddr[env]
	var rec *envRecord
	if len(queue) > 0 {
		rec = queue[0]
		if len(queue) == 1 {
			delete(goneEnvs.byAddr, env)
		} else {
			goneEnvs.byAddr[env] = queue[1:]
		}
	}
	goneEnvs.Unlock()
	if rec != nil {
		forgetEnv(rec)
	}
}

// forgetEnv drops every token that belongs to the environment rec stands for.
// Other environments may be using the tables meanwhile.
func forgetEnv(rec *envRecord) {
	for _, s := range tokenSets {
		s.m.Range(func(tok, entry any) bool {
			if entry.(tokenEntry).owner == rec {
				s.m.Delete(tok)
			}
			return true
		})
	}
}

// resolve returns the engine pointer for a live token.
func (s *tokenSet) resolve(tok unsafe.Pointer) (unsafe.Pointer, bool) {
	if tok == nil {
		return nil, false
	}
	v, ok := s.m.Load(tok)
	if !ok {
		return nil, false
	}
	return v.(tokenEntry).engine, true
}

// retire resolves a live token and forgets it in one step, so two concurrent
// uses of the same token cannot both proceed.
func (s *tokenSet) retire(tok unsafe.Pointer) (unsafe.Pointer, bool) {
	if tok == nil {
		return nil, false
	}
	v, ok := s.m.LoadAndDelete(tok)
	if !ok {
		return nil, false
	}
	return v.(tokenEntry).engine, true
}
