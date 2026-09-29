# Design notes

**English** · [简体中文](DESIGN.zh-CN.md)

## Overall architecture

```
JS (V8)                    C ABI (Node-API 8)                 Go
──────                     ──────────────────                 ──
addon.node  ◄──load──►  node.exe (exports napi_*)  ◄──cgo──►  package napi
   │                                                         │
   │  napi_register_module_v1 ◄───── //export ─────────────  package entry
   │  napiGoExecuteCallback  ◄───── //export ─────────────   callback trampoline
   └── value exchange: napi_value ↔ napi.Value(unsafe.Pointer)
```

- **Stable C ABI only.** `NAPI_VERSION=8`, resolved entirely through `node.exe`
  on Windows and through the host process's dynamic symbols on Linux/macOS.
  Nothing links against V8 and nothing touches C++.
- **Headers are vendored.** `include/node/` holds the official `node_api.h` /
  `js_native_api.h` (MIT) and `windows/node.lib` is an import stub (symbol table
  only, no code). The `#cgo` directives in `napi/cgo.go` wire everything up, so
  users install no Node.js components at all.

## How the cgo code is organised

Two hard cgo constraints dictate the file layout:

1. **A preamble is per-file.** Shared C declarations live in
   `napi/napi_preamble.h`, and every cgo-using file does
   `#include "napi_preamble.h"`.
2. **A `//export` C wrapper is visible only to the file that declares it.**
   Other files obtain those function pointers through `extern` prototypes in the
   preamble header (the linker resolves them to the same symbol). Every
   trampoline lives in `napi/registry.go`.

## Callback bridging (the trampoline)

Every Go function exposed to JS goes through one trampoline:

```
JS calls f(x)
  → node calls napiGoExecuteCallback(env, cbinfo)      // C wrapper
     → napi_get_cb_info to fetch the data pointer      // = box pointer
     → loadBox: cgo.Handle → cbData{cb}                // Go-side registry
     → cb(env, cbinfo)                                 // the user's Go function
     → returns napi_value (nil = undefined)
```

- **The box.** A pointer slot allocated with C `malloc` holding a `cgo.Handle`.
  This sidesteps Go's `unsafe.Pointer` rules (the uintptr→pointer conversion
  happens in C), so the handle can legally live in a C struct's `void *data`.
- **Automatic cleanup.** `CreateFunction` registers a `napi_add_finalizer` on the
  function object; when JS collects it, the finalizer deletes the cgo handle and
  frees the box. Property-descriptor boxes hang off the host object the same way
  (`napiGoPropsFinalizeCallback`).
- **One box even with a user finalizer.** `Wrap` / `CreateExternal` /
  `AddFinalizer` / `SetInstanceData` pack "Go value + user finalizer" into a
  single `finalizePayload` box rather than occupying the data slot and the hint
  slot separately. The reason: after `napi_remove_wrap` the engine never calls
  that finalizer again, so a separate hint box would leak forever (probe
  measurement: 5000 `Wrap`+`RemoveWrap` cycles leaked 5000 boxes). With one box,
  `Unwrap` / `RemoveWrap` can hand the value back and free the box in the same
  step, keeping the books exact. `SetInstanceData` likewise frees the box it
  replaces — the engine lets you overwrite the slot without calling the old
  data's finalizer (its source simply does `delete old_data`).
- **Crash guards.** For the APIs that crash when misused, the Go side keeps a
  live set and turns a use-after-free into `napi_invalid_arg`:
  `DeleteReference` and the reference operations (`liveRefs`),
  `ResolveDeferred`/`RejectDeferred` (`liveDeferreds` — a second settle makes the
  engine unconditionally `delete deferred_ref`), and
  `AsyncWork.Delete/Queue/Cancel` (a state machine). Measured, those three
  misuse classes were `0xC0000374` (heap corruption), `0xC0000028` and
  `0xC0000005` (access violation); now they only return errors. In the same
  spirit, invalid descriptors (a `PropertyDescriptor` with no name, an empty
  `ClassDescriptor`, a nil constructor) are rejected before reaching the engine —
  a NULL name is a dereference crash (`0xC0000028`).
- **Panic safety.** Every trampoline `defer recover()`s. A panic on the JS thread
  becomes a `napi_throw_error` (the process survives); on a pool thread (async
  work `execute`) it is logged. Converting a message to a C string is always a
  paired `C.CString`/`C.free` — an early version missed the `free`, leaking 31MB
  of C heap over 20k 1KB panics.
- **Release must be a `defer`.** Every `freeBox` inside a trampoline must not be
  inlined after the user callback: Go's `recover()` ends the function outright,
  so once a panic is thrown, the statements after it never run (measured: 2000
  panicking finalizers pinned 2000 boxes forever). There must also be exactly
  **one** release site per function — inline release plus a release in the
  recover branch is a double free, and the C `free` has already run, so it
  corrupts the heap. Type assertions in trampolines always use the `, ok` form:
  a failed assertion both panics and skips the release.
- **DebugStats.** `boxHandle`/`freeBox` maintain atomic counters; the stress
  suite asserts `outstanding == baseline` to prove there is no leak.

## Thread model

| Context | Allowed |
|---|---|
| JS thread (callbacks, TSFN payloads, async complete) | everything |
| libuv pool thread (async work `execute`) | pure Go only; never touch a `Value` |
| Any goroutine | only `ThreadsafeFunction.Call/Acquire/Release/Ref/Unref` |
| GC finalizer (on the JS thread, during GC) | "basic" APIs only; we do pure Go cleanup |

The registry (cgo handles plus atomic counters) is itself thread-safe and is
covered by `napi/registry_test.go` under `-race`.

## Value lifetime rules (from the Node-API docs plus measurement)

1. A `napi.Value` received by a callback is valid only within that callback's
   scope. **Capturing it in a closure and using it later is undefined
   behaviour** (stress scenarios #15's NaNs and #18's 300 leaked boxes are the
   evidence). To keep it, use `CreateReference` and release it from a finalizer
   on the host object (see the `makeStepper` example).
2. **A strong reference plus a JS closure forms an unreclaimable cycle** (the ref
   pins `jsLevel`, `jsLevel`'s closure holds `goStep`, so `goStep`'s finalizer
   never fires). Break the cycle on the JS side (assign null) or avoid capturing.
3. **Synchronous reentrancy is bounded by the V8 JS stack** (stress #15: stack
   overflow at 1000 levels). Turn deep recursion into promises or TSFN calls.
4. **Releasing a TSFN while its queue is not drained**: node dispatches the
   backlog with a NULL env (just to free the data). The binding discards it
   safely and reclaims the boxes, but your closure never runs. The correct
   pattern is to release from a drain-marker payload (see `tsfnFlood` in
   `stress/main.go`).
5. **TSFN and the event loop**: a referenced TSFN keeps the loop alive; once
   unreferenced, node may exit before dispatching if the main thread holds no
   other handle.
6. A buffer/array-buffer `[]byte` view is valid only while the JS object lives.

## Handle identity: tokens, not engine pointers

Every handle the engine hands back (`napi_ref`, `napi_deferred`,
`napi_async_context`, `napi_handle_scope`, `napi_callback_scope`,
`napi_threadsafe_function`) is a raw pointer, and the engine performs **no
validation** on it. The binding therefore has to keep its own books and reject
handles that were already returned rather than forwarding them —
`napi_delete_reference` and friends are literally a `delete`, and a second call
is a double free.

**The key design decision: the bookkeeping key must not be the engine pointer.**
The C++ heap allocator hands the address of a just-freed object straight to the
next same-sized allocation, so a stale handle "becomes" the name of a live
object: the live-set lookup succeeds, the operation is allowed through, and the
thing that gets destroyed is one the caller never mentioned. Measured (64 rounds
of create/release until the address is recycled):

| Handle | Address recycled | Stale operations allowed |
| --- | --- | --- |
| `napi_ref` | 28 | 28 |
| `napi_handle_scope` | 8 | 8 |
| `napi_deferred` | 1 | 1 |
| `napi_async_context` | 1 | 1 |

The `napi_deferred` row has the worst failure mode: **not a crash but a wrong
value** — the stale handle settles a *live* promise, leaving the one that should
have settled pending forever.

The fix lives in `napi/handle_token.go`: allocate a Go object per handle and use
**its address** as the handle value the caller sees (the type stays
`unsafe.Pointer`), with the engine pointer kept only in the token table.

- Go never gives two **live** objects the same address, so a token is unique.
- A token has two holders: the caller's variable (GC scans `unsafe.Pointer`) and
  the token table. As long as either holds it, the address cannot be reclaimed,
  so a stale token can never be "taken over" by a fresh handle.
- Therefore "already returned" is decidable rather than probabilistic.
- Each handle kind gets its own token table (`tokenSet`): handing a `Ref` token
  to `CloseHandleScope` is rejected instead of being reinterpreted.

This is also why `AsyncWork` was correct from the start — it is a Go struct
pointer, unique by construction. Tokens simply extend the same fact to the
objects the engine allocates.

## worker_threads (multiple environments)

Several Workers inside one process share a single Go runtime, provided they
`require` the **same addon file path** (Windows `LoadLibrary` de-duplicates by
path and shares the reference count). Stress scenario #16 verifies 4 workers plus
the main environment, 5 environments total, working concurrently on one runtime.

**Warning: never ship a separate copy of the addon file per worker.** That
instantiates multiple Go runtimes in one process (signal handlers, TLS, thread
registration all conflict) and always crashes. An early version of the stress
suite made this mistake: a 100% reproducible segmentation fault that disappeared
once the path was shared.

## Registration protocol

- `napi_register_module_v1` (`//export`, lands in the DLL export table): node
  calls it on load; it creates the functions registered with `entry.Export` one
  by one and attaches them to `exports`.
- `node_api_module_get_api_version_v1`: returns 8, the equivalent of the
  `NAPI_MODULE` macro's version probe, which node uses to check ABI
  compatibility.

## Why Windows links against node.lib and nothing else

- A PE DLL may not contain unresolved symbols, so Linux's
  `--unresolved-symbols=ignore-all` route is not available on Windows.
- `-lnode` locates `node.lib`, and both GNU ld and lld-link understand it — which
  is why both MinGW gcc and clang can link the addon.
- `node.lib` declares symbols only and pulls in no implementation; the artifact
  binds to the host `node.exe` at run time.

## Known limitations

- **After `napi_release_threadsafe_function(..., napi_tsfn_abort)`, a single
  event-loop turn destroys the object, contradicting the documented thread-count
  semantics.** Measured with plain Node-API (the raw probe in
  `probe/bridge.go`, Node.js 22, Windows):

  | Sequence | Result |
  | --- | --- |
  | `create(2) + abort + release` (same turn, no loop turn) | all `napi_ok` |
  | `create(2) + abort` → one loop turn → `acquire` | `0xC0000028` |
  | `create(2) + abort` → one loop turn → `release` | `0xC0000028` |

  Thread counts 1/2/3 and bounded/unbounded queues all behave the same; only "no
  event-loop turn" is safe. `0xC0000028` is `STATUS_ASSERTION_FAILURE`, which on
  Windows is the classic symptom of entering a **deleted
  `CRITICAL_SECTION`** — the engine's mutex went away with the object. The
  Node-API documentation says the object is destroyed only once every thread has
  released; the implementation disagrees.

  The binding's response: after an abort, **everything returns `napi_closing`
  and nothing is forwarded**, including the "remaining threads release" step the
  documentation recommends for winding down. This does not pin the environment —
  the engine has already run `ReleaseResources` before destroying the object
  (`env->Unref()` plus removing the cleanup hook), so the process still exits.
  The cost is that the engine object itself leaks; it cannot be reclaimed safely.

- Node-API 9/10 APIs (external strings, property keys, syntax errors, module file
  names, …) are deliberately unbound: they would introduce new symbol
  dependencies and break the "build once, load anywhere" promise.
- clang with the MSVC target compiles and links, but Go runtime initialisation
  hangs at load time; under investigation (suspected interaction between the Go
  runtime and the TLS/startup path of lld-link output). MinGW gcc is the
  supported toolchain.
- On Windows, finalizers during worker-env teardown run on the worker thread; the
  binding only frees memory there, which is safe — but a user finalizer must not
  call Node-API.
- **`DefineClass` boxes are pinned by the engine; this is engine behaviour the
  binding cannot avoid.** `napi_define_class` uses a V8 `FunctionTemplate`, and
  V8 caches the instantiated class/constructor in a `FixedArray` on the
  `NativeContext`, strongly referenced by the template's serial number
  (`TemplateInfo::CacheTemplateInstantiation`, falling back to a global cache
  once the fast cache is full), and that cache **is never cleared for the
  lifetime of the context**. Every class object and its box therefore live until
  the environment exits — measured: `classChurn(300)` leaks 669 boxes with no
  change after 12 forced GCs, while objects from `DefineProperties` /
  `CreateFunction` / `Wrap` / `AddFinalizer` / `CreateExternal` collect normally
  (control group Δ0). Reproduced with raw C calling `napi_define_class`
  directly, which proves it is unrelated to the binding. Conclusion: **treat
  `DefineClass` as a one-time module-initialisation operation** (define a fixed
  set of classes at the top level) and keep it out of hot paths.
- **The engine has a family of handle-taking APIs that use a bare `delete`, so
  the binding must maintain live sets itself.** `napi_delete_reference`,
  `napi_remove_async_cleanup_hook`, `napi_async_destroy` and the
  `napi_close_handle_scope` family all `delete <pointer>` directly, and beyond a
  NULL check (and the scope counters) **there is no way to tell whether a handle
  is still valid**. Measured failure modes:

  | Misuse | Engine behaviour | Exit code |
  |---|---|---|
  | `DeleteReference` twice | bare `delete` | `0xC0000374` |
  | `RemoveAsyncCleanupHook` twice | bare `delete` | `0xC0000028` |
  | `AsyncDestroy` twice | bare `delete` | `0xC0000374` |
  | `AsyncDestroy` with a foreign pointer | bare `delete` | `0xC0000028` |
  | Closing a non-innermost scope twice | counter has slack → `delete` twice | `0xC0000374` |
  | Closing an escapable scope through the plain entry point | object reinterpreted | undefined |

  Note that `napi_close_handle_scope`'s counter **only prevents underflow** (it
  returns `napi_handle_scope_mismatch` only when `open_handle_scopes == 0`), so
  with another scope still on the stack it cannot stop a double close — which is
  exactly the easiest misuse to write. The binding's answer is a live set per
  handle family (`liveRefs`, `liveDeferreds`, `asyncContexts`,
  `asyncCleanupLive`, `liveHandleScopes`, `liveEscapableScopes`), checked
  **before** anything is handed to the engine, returning `napi_invalid_arg` on a
  hit. The cost is one map operation per handle scope (~20ns uncontended),
  negligible next to the cgo boundary itself, and the payoff is that misuse no
  longer corrupts the heap.
- **`AsyncCleanupHookFunc` must close its own handshake.** The engine treats an
  async cleanup hook as an **asynchronous handshake**: only the handle's
  destructor calls `AsyncCleanupHookInfo::done_cb_` (and thus
  `DecreaseWaitingRequestCounter`), and that destructor only runs inside
  `napi_remove_async_cleanup_hook`. If the callback never calls it, **process
  exit blocks forever** (measured: the hook ran, the process was still alive
  after 5 seconds, and had to be killed with `SIGTERM`). The binding cannot paper
  over this — it can only guarantee that calling it is safe and that a repeat
  call returns an error; it cannot call it for you.
