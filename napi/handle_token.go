package napi

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

// tokenSet maps live tokens to the engine pointers they stand for.
//
// One set per handle family, deliberately: a Ref token presented where a
// HandleScope is expected is then rejected rather than silently reinterpreted.
type tokenSet struct {
	name string
	m    sync.Map // unsafe.Pointer(token) -> unsafe.Pointer(engine)
}

func newTokenSet(name string) *tokenSet { return &tokenSet{name: name} }

// issue records a new handle and returns the token that identifies it.
func (s *tokenSet) issue(engine unsafe.Pointer) unsafe.Pointer {
	tok := unsafe.Pointer(new(handleToken))
	s.m.Store(tok, engine)
	return tok
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
	return v.(unsafe.Pointer), true
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
	return v.(unsafe.Pointer), true
}
