# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- `AsStatus` sees through wrapped errors (`errors.As`). An error annotated with
  `fmt.Errorf("...: %w", err)` used to lose its status.
- `CreateExternalArrayBufferFromBytes` and `CreateExternalBufferFromBytes` pin
  the slice's storage with `runtime.Pinner` until the buffer is collected.
  Keeping the slice reachable was already enough to keep the address valid,
  but only because today's collector never moves heap objects; the cgo rules
  require Go memory that C keeps a pointer to to be pinned, and now it is.

### Fixed

- **An addon held only by a Worker crashed the process when the Worker
  exited.** Node.js closes an addon's library handle when the environment that
  loaded it is destroyed; if nothing else holds the library it is unmapped, and
  the Go runtime inside it cannot be shut down — its threads keep running in
  freed memory. Measured on Windows with Node.js 22: `0xC0000005` shortly after
  the Worker ends, both when the addon is required only inside a Worker and
  when a Worker loads its own copy of the file. The addon now pins itself in
  memory when it is loaded: `GetModuleHandleEx(GET_MODULE_HANDLE_EX_FLAG_PIN)`
  on Windows, an extra `RTLD_NODELETE` reference on macOS. ELF platforms were
  already covered by the Go linker's `-z nodelete`.

  This also corrects the reasoning behind the "every worker must require the
  same path" rule. Two copies of the addon required on one thread run side by
  side, so the crash seen with a copy per worker was the unload, not "multiple
  Go runtimes in one process". A shared path is still recommended — every copy
  is a separate runtime with separate state — but doing otherwise no longer
  crashes.
- **A method or accessor defined with `DefineProperties` ran the wrong Go
  callback once its object had been collected.** The callback boxes were tied
  to the object the properties were defined on, but a method read off that
  object (`const m = obj.m`) or an accessor taken from its property descriptor
  is a function of its own, and JavaScript can keep it after the object is gone.
  Collecting the object freed the boxes underneath those functions, and the next
  call read recycled storage. Measured: the extracted method returned another
  function's result, with no error, and the extracted getter failed with
  "getter data missing".

  Every such function now owns its box, exactly as a `CreateFunction` function
  does. The binding creates the function itself and defines a method as a data
  property holding it (what the engine does with a method anyway) and an
  accessor through `Reflect.defineProperty`. What ends up on the object is
  unchanged — same keys, same order, same attributes, same statuses on failure —
  and the debug box counter now shows one box per function instead of one per
  descriptor plus one per call. `DefineClass` is unaffected: its boxes are tied
  to the class, which the engine never collects.
- **A goroutine that used a threadsafe function after its environment had shut
  down crashed the process.** When a Worker ends, the engine tears down the
  threadsafe functions it created. The binding registered no finalize callback,
  so it was never told: its own record still said "live", and the goroutine's
  next `Call` reached a freed object. Measured on Node.js 22: `0xC0000005`, every
  time.

  The binding now registers the finalize callback and builds the function's
  lifetime on it. That callback is the one point at which the engine object is
  known to be valid for the last time on every Node.js release — Node.js up to
  24.13 deletes the object right after it, 24.14 and later keep it until every
  thread has released (`docs/DESIGN.md`, "Known limitations", has the two side
  by side). Calls into the engine hold a read lock that the finalizer takes for
  writing; the finalizer hands back the thread counts Go still holds; afterwards
  every method returns `napi_closing` and nothing is forwarded. A goroutine
  looping over `Call` sees that error and stops, and the `Release` it would have
  made is refused instead of faulting.

  Measured on Node.js 22. On the engine that Node.js 24.14 and later ship, the
  same code path is derived from the engine's source and has not been run
  locally; CI's Node.js 24 leg runs the checks against it.
- **An abort left all but one of the goroutines blocked in `Call` blocked for
  good.** With several callers waiting behind a full queue, the engine wakes one
  of them when the function closes — `Signal`, where it has to wake them all —
  and goes on to delete the object the others are waiting on. Measured: four
  blocked callers, one returned `napi_closing`, three never returned.
  `Call(..., napi.Blocking)` now waits in Go: the engine is only ever asked to
  queue without blocking, and a full queue is retried after the next dispatch.
  Every blocked caller is woken when the function ends.
- **A Windows addon only loaded into a host whose executable was called
  `node.exe`.** Linking the `node.exe` import library writes that file name
  into the addon's import table. Measured with a renamed copy of `node.exe` as
  the host: `ERR_DLOPEN_FAILED` ("The specified module could not be found"), or
  — when a `node.exe` is on `PATH` — a silent exit with `0xC0000028`, because the
  import is then satisfied by that other file instead of the host. Electron,
  single-executable applications and embedders all have "another name".

  The addon now imports nothing from the host. Each `napi_*` function is defined
  in the addon as a jump through a pointer, and the pointers are filled in when
  the addon is loaded, from whichever module of the process exports Node-API:
  the executable, then `libnode.dll`, then `node.dll`. A function the host does
  not export fails with `napi_generic_failure` and an error that says why. See
  "How the Node-API functions are found on Windows" in `docs/DESIGN.md`.
- **Every JavaScript exception that crossed the `js` layer wrote a "could not
  throw" line to stderr.** The `js` package reports failures by panicking, and
  a callee's exception arrives as a panic carrying `napi_pending_exception`.
  The callback trampoline tried to throw that panic as a new error, was refused
  because an exception was already pending, and logged the refusal — while the
  original exception propagated correctly all along. A panic that carries
  `napi_pending_exception` while an exception really is pending is now
  recognised as that exception on its way up and left alone.
- **A panicking `SetInstanceData` finalizer leaked its box.** The release was
  written inline after the user's callback, and a panic unwinds past it — the
  mistake already fixed in the other finalizers. Measured: one box left behind
  per environment torn down. The release is now deferred. The class-property
  finalizer's unchecked type assertion also takes the `, ok` form the other
  trampolines use.

### Removed

- `windows/node.lib`. Nothing is linked against Node.js any more, on any
  platform, so the 3 MB x64-only import library is gone and with it the reason
  the Windows build was limited to x64 (other architectures are untested).
  C code of your own that used *other* symbols of `node.exe` through it —
  libuv, V8 — now has to bring its own import library.

### Documented

- `CreateError` / `CreateTypeError` / `CreateRangeError` take a `code` that
  must be a string or `nil`. Passing `undefined` returns
  `napi_string_expected` — easy to miss, because the call returns a
  `(Value, error)` pair, and the value it hands back in that case cannot be
  used as an `Error`. Found while building an addon that rejected a promise
  with it: the rejection reached JavaScript as a bare string.
- What a throwing JS callback does to a threadsafe-function dispatch. Measured
  on Node 22.22.2: `CallFunction` reports `napi_pending_exception`, the
  exception stays pending on the environment, and the rest of the dispatch
  still works — `create_object`, `create_string_utf8` and `get_undefined` all
  return `napi_ok` while it is pending, and `GetAndClearLastException` clears
  it. The engine, not the binding, decides the outcome: by default it logs
  `DEP0168` and drops the exception; under
  `--force-node-api-uncaught-exceptions-policy=true` it becomes an uncaught
  exception.

## [0.1.1] - 2026-09-29

### Fixed

- The `entry` package did not pass the vendored header directory to the C
  compiler. `register.go` includes `entry_preamble.h`, which includes
  `<node_api.h>`, but the package carried no `-I` directive for `include/node`,
  so it only compiled where the headers happened to be reachable already — for
  example through a local `CGO_CFLAGS` override. On a clean checkout anything
  that compiles the package failed with
  `fatal error: node_api.h: No such file or directory`, which is what broke CI.
  `entry` now carries the same `#cgo CFLAGS` directive as `napi`, so the module
  builds on a machine that has nothing configured beyond Go and a C compiler.

## [0.1.0] - 2026-09-29

Initial release.

### Added

- Complete Node-API 8 (stable ABI) bindings: values, objects, properties,
  classes, references, handle scopes, errors, promises, BigInt, date,
  ArrayBuffer/TypedArray/DataView/Buffer, external values, instance data,
  async work, threadsafe functions (with full payload support), async contexts,
  callback scopes, env/async cleanup hooks, and external memory accounting.
- `entry` package: module registration through `napi_register_module_v1` plus
  `node_api_module_get_api_version_v1`, exported from Go with no C glue.
- `js` package: a `syscall/js`-style convenience layer.
- Bundled Node.js headers and a Windows `node.lib` import stub, so no Node.js
  installation and no node-gyp are required.
- Platforms: Windows x64 (MinGW-w64 gcc, verified); Linux and macOS link flags
  are wired and exercised in CI.
- Test suites: 71 functional assertions (`example`, covering all 138 exported
  `napi` functions), 18 stress scenarios (`stress`), an adversarial probe suite
  (`probe`), and `-race`-tested registry unit tests.
- `probe/`: an adversarial suite (`probe/main.go`, `probe/bridge.go`,
  `probe/run.mjs`, `probe/edge.mjs`) that targets documented-but-undefined edge
  cases: invalid descriptors, scope misuse, exceptions and coercions,
  TypedArray/DataView with a byte offset, access after detach, string encoding
  boundaries (embedded NUL, lone surrogates, invalid UTF-8), multi-word BigInt,
  setting instance data twice, finalizer bookkeeping (including **panicking
  finalizers**), reference lifetimes, async-work lifetimes, cleanup hooks,
  external values, and the panic behaviour of the `js` layer. A set of **raw C
  probes** (`rawProbe` / `probe_*`) bypasses the binding entirely to decide
  whether a behaviour belongs to the binding or to the engine.
  - Hard-crash cases are observed in isolated child processes (`spawnProbe`),
    turning a crash into an assertable exit code; a parallel process crash does
    not affect the other cases.
  - `probe/one.mjs` can force a GC in the child and then read a probe again
    (`spawnProbe(name, args, afterProbeName)`), which is how counters advanced by
    finalizers become visible instead of only showing the pre-GC ledger.
  - Leak assertions always look at the **steady-state slope** rather than a single
    sample: the panic-path RSS gate discards four warm-up rounds (Go arena/span
    watermark growth produces false +11/+4/+4/+3MB signals) and then judges the
    last three (converging to ±0.4MB; a real leak would be a constant ~19MB per
    round, +229MB over 12 rounds, whereas the measured total growth was +25MB).

### Changed — a live handle's identity is a token, not the engine pointer

Previously every handle guard (`Ref` / `HandleScope` / `EscapableHandleScope` /
`Deferred` / `AsyncContext` / `ThreadsafeFunction`) used the **raw pointer the
engine returned** as the identity, with a `sync.Map` recording "this address is
still alive". That assumption is wrong: once the engine frees a handle it
**reallocates the same address** for the next same-sized object, so a stale
handle "collides" with a live object in the live set — the lookup succeeds, the
operation is allowed through, and **a live object is destroyed**.

Measured (each round is create → release → create until the allocator returns the
address; 64 rounds):

| Handle | Rounds the address was recycled | Stale operations allowed |
| --- | --- | --- |
| `napi_ref` | 28 / 64 | 28 / 28 |
| handle scope | 8 / 64 | 8 / 8 |
| `napi_deferred` | 1 / 64 | 1 / 1 |
| async context | 1 / 64 | 1 / 1 |

The deferred row is the quietest and the worst: the stale handle settles a
**live** promise, and the one that should have settled stays pending forever —
no error, just a Promise that never resolves.

Each handle now gets a Go-allocated identity token (`napi/handle_token.go`), and
the `Ref`/`Deferred`/`AsyncContext`/… value the caller holds is the **token
address** rather than the engine pointer:

- Go never gives two **live** objects the same address, so a token is unique;
- a token is referenced both by the caller's variable (`unsafe.Pointer`, which
  the GC scans) and by the token table, so as long as either side holds it the
  address cannot be reclaimed;
- therefore "already released" is always decidable: the token table does not
  contain it, and a new handle has a new address.

The engine pointer exists only inside the token table, unreachable except while
the token is live. The public type is unchanged (still `unsafe.Pointer`), `nil`
comparison still works, and no caller code needs to change.

`CallbackScope` previously had **no guard at all**: `napi_close_callback_scope`
is a bare `delete`, protected only by the `open_callback_scopes` counter. That
counter stops an ordinary double close but not the real mistake — with two scopes
open, closing the **first** one twice passes the counter both times and deletes
the same object. It is now behind the token guard too (a non-live handle returns
`napi_callback_scope_mismatch`, which is the engine's own answer for "I don't
have this scope").

### Fixed — `ThreadSafeFunction`: after an abort, any call is a use-after-free

The previous round added a **thread-count** guard to TSFN but missed the engine's
**state bit**. `Release(Abort)` pushes the engine state to `kClosing` without
touching the thread count, so the count still read 1 and the guard let the call
through — while the engine was already unusable.

Measured (`probe/edgecase.mjs tsfnAbort`; before the fix both later calls were
allowed through): `NewThreadsafeFunction(env, fn, 0, 2)` → `Release(Abort)` → one
event-loop turn → `Call` → `Release` exits the process **silently** with
`0xC0000028` (`STATUS_ASSERTION_FAILURE`).

**Attribution**: reproduced with plain Node-API (the raw probe in
`probe/bridge.go`, zero Go binding code) — `create(2) + abort` → one turn → both
`acquire` and `release` produce the same `0xC0000028`. Thread counts 1/2/3 and
bounded/unbounded queues behave identically; only "no event-loop turn after the
abort" is safe. On Windows `0xC0000028` is the symptom of entering a **deleted
`CRITICAL_SECTION`**, i.e. the engine's mutex disappeared along with the object.
Conclusion: **after an abort the object is destroyed on the next event-loop
turn**, and the Node-API documentation's "destroyed only once all threads have
released" does not hold on this runtime.

The binding's response: after `Release(Abort)` **nothing is forwarded any more**,
including the "remaining threads `Release`" step the documentation suggests for
winding down — that would touch freed memory. It now returns `napi_closing` with
an explanation. Refusing it has been verified **not** to pin the environment: the
engine has already run `ReleaseResources` before destroying the object
(`env->Unref()` plus removing the cleanup hook), so the abort sequence's process
still exits normally (the `tsfnAbort` case: both later calls are refused, exit
code 0).

### Documented — three places where the two collectors cannot see each other

Go's GC only traces Go references and V8's only traces V8 references, so a raw
pointer crossing the boundary is invisible to **the collector that owns that
memory**. All three cases are measured, and none can be fixed by the binding —
it can only offer safe forms:

- **V8 pointing at Go memory**: `CreateExternalArrayBuffer(..., finalData = nil)`
  wrapping a Go slice directly means that after one Go GC plus `FreeOSMemory`, a
  subsequent read from V8 is an access violation (exit code `0xC0000005`).
  Added `CreateExternalArrayBufferFromBytes` / `CreateExternalBufferFromBytes`,
  where passing the slice is constructed rather than remembered.
- **Go pointing at V8 memory**: the `[]byte` returned by `GetArrayBufferInfo` /
  `GetTypedArrayInfo` / `GetDataViewInfo` / `GetBufferInfo` aliases V8 storage.
  Reading that slice after the ArrayBuffer is collected is a fatal Go runtime
  error (`runtime.createfing`, exit code 2). Hold a `napi_ref` and re-read it
  whenever you need it.
- **A V8 handle outliving its handle scope**: a callback argument is a `Local`,
  and its slot becomes reusable the moment the callback returns. Go will happily
  store it anyway, so the failure mode is **not a crash but a wrong value** —
  measured, the stale handle reads back another callback's argument
  (`"MARKER"`). To keep something across callbacks, use `CreateReference`.

All three live in `probe/edge.mjs` as permanent `[hazard]` gates: they turn
`[ok]` once fixed and reproduce the same verdict if they regress.

### Fixed — memory leaks

- `Wrap` / `CreateExternal` / `AddFinalizer` / `SetInstanceData` now pack "Go
  value + user finalizer" into a **single box** (`finalizePayload`) instead of
  letting the finalizer occupy a separate hint box. After `napi_remove_wrap` the
  engine never calls that finalizer again, so the old scheme leaked the hint box
  forever — the probe measured 5000 leaked boxes over 5000 `Wrap`+`RemoveWrap`
  cycles and 3000 over 3000 finalizer-bearing ones. With the fix the books balance
  exactly.
- `SetInstanceData` now frees the box it replaces. The engine lets you overwrite
  the slot **without calling the old data's finalizer** (its source does a plain
  `delete old_data`), which leaked the old box permanently; the binding now
  remembers the current box and reclaims it on overwrite.
- `panicToJS` leaked C heap: `C.CString(msg)` had no matching `C.free`. 20000
  1KB panics leaked about 31MB (a same-scale JS `throw` control leaked only
  1.1MB).
- **Box leak when a user finalizer panics**: `napiGoFinalizeCallback` /
  `napiGoExternalFinalizeCallback` / `napiGoCleanupHook` /
  `napiGoAsyncCleanupHook` all inlined `freeBox` *after* the user callback, and
  Go's `recover()` ends the function outright — once a panic is thrown, the
  release statement never runs. Measured: 2000 panicking finalizers pinned 2000
  boxes (and their cgo handles) forever. The release is now itself a `defer`, so
  it runs on the recover path too; a standalone Go program confirms the
  difference: `inline, panic → freed = 0`, `defer, panic → freed = 1`.
- **`napiGoTSFNCallJs` double free**: after the inline `freeBox`, the recover
  branch freed the same box again (`cgo.Handle.Delete` panics on a second call,
  and the C `free` had already run, corrupting the heap). The function now uses
  exactly **one** `defer freeBox`, so ownership is unique.
- Four unchecked type assertions in trampolines (`.(CleanupHook)`,
  `.(*tsfnCall)`, `.(*externalFinalizeData)`, `.(AsyncCleanupHookFunc)`) would
  both panic and leak the box if the data were polluted; all now use the `, ok`
  form and release on failure too.
- `napiGoCleanupHook` asserted the env cleanup hook payload without checking, so
  a failed assertion or a panicking hook skipped the release; now a deferred
  release plus a type check.

### Fixed — process crashes (all now return `napi_invalid_arg`)

- **References**: a second `DeleteReference`, and `ReferenceRef` /
  `ReferenceUnref` / `GetReferenceValue` on a deleted reference, were
  use-after-frees (measured `0xC0000374`, heap corruption). The binding keeps a
  `liveRefs` live set to intercept them.
- **Promises**: a second settle through `ResolveDeferred` / `RejectDeferred`
  makes the engine unconditionally `delete deferred_ref` (measured
  `0xC0000028`). A `liveDeferreds` live set intercepts it and returns
  `napi_invalid_arg`.
- **AsyncWork**: "delete after queue", "double delete", and "queue after delete"
  were `0xC0000005` / `0xC0000028` / `0xC0000005`. `AsyncWork` is now driven by an
  `idle/queued/done/deleted` state machine, so misuse always returns an error;
  the `complete` trampoline advances the state before running the user callback,
  so a reentrant delete from inside the callback cannot collide.
- **Invalid descriptors**: a `PropertyDescriptor` with no `name` and an empty
  `ClassDescriptor` made the engine dereference NULL (measured `0xC0000028`, a
  stack buffer overrun check failure). `DefineProperties` / `DefineClass` now
  validate up front and return an error; `DefineClass` also rejects a nil
  constructor, which previously panicked when called.
- Three callback trampolines dereferenced NULL when `data == nil`: `loadBox` now
  has a nil guard and the trampolines check up front, so `napiGoExecuteCallback`
  and friends report a JS exception instead of crashing the process.
- `napiGoTSFNCallJs` gained a `recover` and a box release; `napiGoAsyncExecute` /
  `napiGoAsyncComplete` gained type assertions so that externally polluted data
  cannot crash the type assertion.
- **Handle scopes**: the engine's only guard on `napi_close_handle_scope` is the
  per-environment `open_handle_scopes` counter (which rejects only at zero), and
  it cannot catch the real mistake — with slack on the counter, **closing the same
  scope twice really does `delete` twice** (measured `0xC0000374`).
  `OpenHandleScope` now records a live set, so a double close, closing an
  escapable scope through the plain entry point, or a reverse mismatch all return
  `napi_invalid_arg`, while the correct path is unaffected.
- **Async contexts**: `napi_async_destroy` is a bare `delete node_async_context`.
  A second `AsyncDestroy` measured `0xC0000374`, and any pointer not produced by
  `AsyncInit` measured `0xC0000028`. The binding added an `asyncContexts` live
  set; both cases now return `napi_invalid_arg`.
- **Async cleanup hooks**: `napi_remove_async_cleanup_hook` is a bare
  `delete handle` in the engine (with only a NULL check). Removing twice measured
  `0xC0000028`. The `asyncCleanupLive` live set intercepts it.

### Fixed — contracts and documentation

- `GetValueBigIntWords` used to return `napi_invalid_arg` for `0n`. The engine's
  valid call pattern is "(env,v,nil,&wordCount,nil) to query" followed by
  "(env,v,&sign,&wordCount,words) to read", and `0n` has a word count of 0 with
  no words to write in the second stage — now short-circuited to return
  `sign=0, words=[]`.
- Corrected the `TypedArrayInfo.Data` / `DataViewInfo.Data` comment: measured,
  that slice **points at the view's first element and its length is the view's
  length** (in elements), not the whole backing store. `ByteOffset` is the view's
  offset within the backing store, useful for locating the underlying
  ArrayBuffer.
- Documented the engine's pinning behaviour for `DefineClass` (see below).
- **The handshake contract of `AsyncCleanupHookFunc`** (a new hard requirement):
  the callback **must** call `RemoveAsyncCleanupHook(handle)`, otherwise
  environment teardown blocks forever. The engine calls
  `AsyncCleanupHookInfo::done_cb_` only from the handle's destructor, and that
  destructor only runs inside `napi_remove_async_cleanup_hook`. Control
  measurement: the callback ran (stderr showed the marker) but the process was
  still alive after 5 seconds and had to be killed with `SIGTERM`. The contract is
  documented on `AsyncCleanupHookFunc` and guarded by a probe.
- Corrected the `AddEnvCleanupHook` comment: the hook cannot be removed **because
  this function returns no token**, not because the engine forbids it — use
  `AddEnvCleanupHookRemovable` when you need removal.
- Documented on `AddAsyncCleanupHook` why "call the engine first, register the box
  after" is not a race: `CleanupQueue::Add` only inserts and never invokes a
  callback synchronously, the queue is drained at env teardown, and both
  registration and draining happen on the same JS thread, so registration always
  precedes any callback.
- **Fixed a regression introduced by the previous round's fix**: to intercept a
  double removal, `napiGoAsyncCleanupHook` briefly returned early when the box
  mapping was missing — which skipped the user callback too, and since teardown
  was waiting on that handshake, the process never exited. The callback now runs
  **unconditionally**, and the two things that had been conflated are separated:
  `asyncCleanupBoxes` owns only the closure's memory (first to arrive frees,
  exactly once), while `asyncCleanupLive` owns only whether the engine handle is
  still alive (i.e. whether it may be handed to the engine for `delete`). Merging
  the two into one set was the shared root cause of both earlier bugs.

### Fixed — carried over from the previous round

- `CreateExternalArrayBuffer` / `CreateExternalBuffer` no longer register a
  finalizer whose `finalize_data` slot carries the Go payload. Node-API forces
  that slot to be the raw external pointer, so treating it as a box corrupted the
  handle registry (`misuse of an invalid Handle`). The Go payload now travels
  through the hint slot and a dedicated exported finalizer. Both functions take
  an `onFinalize FinalizeFunc` plus a `finalData any` pair; pass the backing slice
  as `finalData` to pin it for the buffer's lifetime.
- `RemoveAsyncCleanupHook` leaked the box (and the cgo handle) holding the Go
  closure, because Node never invokes the callback of a removed hook. The
  handle→box mapping is now claimed by whoever runs first (the remover or the
  trampoline), so the release is exactly-once and race-safe.
- `AdjustExternalMemory`'s demo asserted the wrong invariant; the API returns the
  post-adjustment total, so `+1MiB` followed by `-1MiB` must show a 1MiB drop.
- The `example` class factory export was renamed `Counter` → `makeCounter`
  (`CounterClass` remains the class value) so the factory no longer shadows the
  class name.

### Known limitation — `DefineClass` boxes live as long as the context

`napi_define_class` uses a V8 `FunctionTemplate`, and V8 **caches the
instantiated class in a `FixedArray` on the `NativeContext`, strongly referenced
by the template's serial number, and never clears it**, so every class object and
its box live until the environment exits. Reproduced by calling
`napi_define_class` directly with raw C, which shows it is unrelated to the
binding; `DefineProperties` / `CreateFunction` / `Wrap` and friends collect
normally (probe control group Δ0). **Treat `DefineClass` as a one-time
module-initialisation operation and keep it out of hot paths.** See the known
limitations in `docs/DESIGN.md`.

[Unreleased]: https://github.com/all-thoughts-are-broken/napi-go/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/all-thoughts-are-broken/napi-go/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/all-thoughts-are-broken/napi-go/releases/tag/v0.1.0
