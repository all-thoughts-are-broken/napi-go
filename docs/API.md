# API reference

The `napi` package maps 1:1 onto the Node-API C API: drop the `napi_` prefix and
camel-case the rest. Every function that returns an `error` uses `nil` for
`napi_ok`; when non-nil, `napi.AsStatus(err)` extracts the `Status`.

Unless stated otherwise, every function must be called on the **JS thread**
(inside a callback, or inside a threadsafe-function payload).

**English** · [简体中文](API.zh-CN.md)

## Environment

| Function | Notes |
|---|---|
| `GetVersion(env) (uint32, error)` | Highest Node-API version the engine supports |
| `GetNodeVersion(env) (NodeVersion, error)` | Running Node.js version |
| `GetUVEventLoop(env) (unsafe.Pointer, error)` | libuv event-loop pointer (opaque) |
| `SetInstanceData(env, data any, onFinalize) / GetInstanceData(env)` | One native data slot per environment. A second call **overwrites** it; the binding frees the old box, but the old `onFinalize` is *not* invoked — the engine simply `delete`s the old data. |

## Values: creation

| Function | JS result |
|---|---|
| `GetUndefined` / `GetNull` / `GetGlobal` / `GetBoolean` | singletons and primitives |
| `CreateObject` / `CreateArray` / `CreateArrayWithLength` | objects and arrays |
| `CreateDouble` / `CreateInt32` / `CreateUint32` / `CreateInt64` | number (int64 loses precision above 2^53 — use BigInt) |
| `CreateStringUtf8` / `CreateStringLatin1` / `CreateStringUtf16` | strings in three encodings |
| `CreateSymbol` | symbol |
| `CreateDate` | Date (milliseconds) |
| `CreateBigIntInt64` / `CreateBigIntUint64` / `CreateBigIntWords` | BigInt (words are little-endian 64-bit limbs) |
| `CreateExternal(env, any, onFinalize)` | opaque object wrapping an arbitrary Go value |
| `CreateError` / `CreateTypeError` / `CreateRangeError` | Error objects (code and message are `Value`s) |

## Values: reads and predicates

| Function | Notes |
|---|---|
| `Typeof` | `typeof` classification (null is distinct from object) |
| `GetValueDouble/Int32/Uint32/Int64/Bool` | extract a primitive (an error on type mismatch) |
| `GetValueStringUtf8/Latin1/Utf16` | string extraction (embedded NUL is preserved) |
| `GetValueBigIntInt64/Uint64/Words` | BigInt extraction (Int64/Uint64 report a lossless flag) |
| `GetDateValue` | Date milliseconds |
| `CoerceToBool/Number/Object/String` | JS coercion (may run user code) |
| `IsArray` / `IsArrayBuffer` / `IsTypedArray` / `IsDataView` / `IsDate` / `IsError` / `IsPromise` / `IsBuffer` / `IsDetachedArrayBuffer` | type predicates |
| `GetArrayLength` / `StrictEquals` / `InstanceOf` | length, strict equality, `instanceof` |

## Objects and properties

| Function | Notes |
|---|---|
| `SetProperty/GetProperty/HasProperty/DeleteProperty/HasOwnProperty` | by `Value` key (string or symbol) |
| `SetNamedProperty/GetNamedProperty/HasNamedProperty` | by UTF-8 name (convenience) |
| `SetElement/GetElement/HasElement/DeleteElement` | by index |
| `GetPrototype` / `GetPropertyNames` / `GetAllPropertyNames` | prototype and key enumeration (configurable mode/filter/conversion) |
| `DefineProperties(env, object, []PropertyDescriptor)` | batch definition (methods, getters, setters, data). Each descriptor needs a `Name` or `NameValue`, otherwise `napi_invalid_arg` is returned — the engine dereferences a NULL name and crashes. |
| `ObjectFreeze` / `ObjectSeal` | freeze and seal |
| `TypeTagObject` / `CheckObjectTypeTag` | 128-bit type tags (native type checking) |
| `Wrap(env, obj, any, onFinalize)` / `Unwrap` / `RemoveWrap` | attach a Go value to a JS object (with a finalizer). The value and the finalizer share **one box**, so `RemoveWrap` leaves nothing behind, and the finalizer will not run after it. |
| `AddFinalizer(env, obj, data, onFinalize)` | attach a finalizer only |

## Functions and classes

| Function | Notes |
|---|---|
| `CreateFunction(env, name, Callback)` | Go callback → JS function (cleaned up on GC) |
| `GetCbInfo(env, info) (CbInfoResult, error)` | arguments, `this`, data |
| `GetNewTarget(env, info)` | `new.target` (nil when not called with `new`) |
| `CallFunction(env, recv, fn, args...)` | call a JS function |
| `NewInstance(env, ctor, args...)` | `new` |
| `DefineClass(env, ClassDescriptor)` | define a class (constructor plus prototype/static properties). Returns `napi_invalid_arg` when `Constructor` is nil or every descriptor is empty. ⚠️ **The engine caches class objects in the context with a strong reference and never collects them**, so treat this as a one-time, module-initialisation operation — see the known limitation in `docs/DESIGN.md`. |
| `RunScript(env, script)` | run JS in the current context (use with care) |

## Lifetime

The values of `Ref` / `HandleScope` / `EscapableHandleScope` / `Deferred` /
`AsyncContext` / `CallbackScope` are **not** raw engine pointers: they are
identity tokens issued by the binding (the type is still `unsafe.Pointer`, so
`nil` comparison is unchanged). The engine recycles the address of an object it
just freed for the next same-sized allocation, so a raw pointer cannot tell a
"returned" handle from a "brand new" one. With tokens, a returned handle is
always detectable and can never be mistaken for a live object. See the "handle
identity" section of `docs/DESIGN.md`. Do not pass these values to raw C calls.

| Function | Notes |
|---|---|
| `OpenHandleScope` / `CloseHandleScope` | batch lifetime control. Double close or a mismatched kind returns `napi_invalid_arg` (the engine only guards with a counter, which cannot catch these). |
| `OpenEscapableHandleScope` / `CloseEscapableHandleScope` / `EscapeHandle` | escape one value out of a scope |
| `CreateReference(env, v, refcount)` / `DeleteReference` | strong (≥1) / weak (0) references. Only objects and functions are accepted. A second `DeleteReference`, or any reference operation after deletion, returns `napi_invalid_arg` instead of crashing. |
| `ReferenceRef` / `ReferenceUnref` / `GetReferenceValue` | adjust the count and read the value |
| `AdjustExternalMemory(env, delta)` | make V8's GC aware of native memory |

## Exceptions

| Function | Notes |
|---|---|
| `Throw` / `ThrowError` / `ThrowTypeError` / `ThrowRangeError` | throw (schedule a pending exception) |
| `IsExceptionPending` / `GetAndClearLastException` | check and take the exception |
| `FatalException` / `FatalError` | trigger `uncaughtException` / terminate the process |
| `GetExtendedErrorInfo` | engine information about the most recent Node-API error |

## Promises and async

| Function | Notes |
|---|---|
| `CreatePromise` / `ResolveDeferred` / `RejectDeferred` / `IsPromise` | promises. A second settle returns `napi_invalid_arg` — the engine unconditionally `delete`s `deferred_ref`, which would be a use-after-free. |
| `NewAsyncWork(env, name, execute, complete)` → `Queue/Cancel/Delete` | libuv thread pool (`execute` runs on a pool thread: **pure Go only**). The `idle/queued/done/deleted` state machine makes "delete after queue", "double delete" and "queue after delete" return errors instead of crashing. |
| `AsyncInit` / `AsyncDestroy` / `MakeCallback` | JS calls visible to `async_hooks`. `AsyncDestroy` only accepts a context produced by `AsyncInit` that has not been destroyed. `MakeCallback` and `OpenCallbackScope` validate the context too, so passing a destroyed one returns `napi_invalid_arg` instead of attributing the work to whatever later object reused the address. |
| `OpenCallbackScope` / `CloseCallbackScope` | call scope outside a callback. A second close returns `napi_callback_scope_mismatch` — the engine's counter cannot catch closing the wrong one when two scopes are open. |

## Threadsafe functions (the only way to call JS from another goroutine)

```go
tsfn, err := napi.NewThreadsafeFunction(env, jsFn, maxQueueSize, threadCount)
tsfn.Call(func(env napi.Env, jsFn napi.Value) { ... }, napi.Blocking|napi.NonBlocking)
tsfn.Acquire() / Release(mode) / Ref(env) / Unref(env)
```

- The `Call` closure runs on the **JS thread** and may create values and call JS
  freely — this is the payload channel.
- `maxQueueSize=0` means unbounded. With a bounded queue and `NonBlocking`, a
  full queue returns `napi_queue_full`.
- `Release` drops the reference count to zero and closes the function. **Drain
  the queue before releasing**: if you release with items still queued, node
  dispatches the backlog with a NULL env and drops it. The binding frees those
  payloads safely, but your closure never runs.
- **`Release(napi.Abort)` invalidates the handle.** The engine destroys the
  object on the next event-loop turn, and every later call touches freed memory
  (measured: a silent exit with `0xC0000028`). The binding therefore rejects all
  operations after an abort — **including the remaining threads' `Release`** —
  with `napi_closing`. In a multi-threaded setting, abort only at the very end,
  or just use plain `Release` and let it wind down naturally.
- A closure must not capture a `napi.Value` across callbacks. To keep a JS value
  inside the closure, use `napi.CreateReference`.

## Buffer / ArrayBuffer / TypedArray / DataView

| Function | Notes |
|---|---|
| `CreateBuffer(env, n)` → `(Value, []byte)` / `CreateBufferCopy(env, []byte)` / `GetBufferInfo` | node:Buffer (the `[]byte` is an aliasing view) |
| `CreateExternalBuffer(env, data, n, onFinalize, finalData)` | zero-copy wrapping (disabled in some builds; returns `napi_no_external_buffers_allowed`) |
| `CreateExternalBufferFromBytes(env, []byte, onFinalize)` | as above, but pinning the slice is guaranteed by construction — **use this for any Go heap memory** |
| `CreateArrayBuffer` → `(Value, []byte)` / `GetArrayBufferInfo` / `DetachArrayBuffer` / `IsDetachedArrayBuffer` | ArrayBuffer |
| `CreateExternalArrayBuffer(env, data, n, onFinalize, finalData)` | zero-copy wrapping of Go memory (same caveats) |
| `CreateExternalArrayBufferFromBytes(env, []byte, onFinalize)` | as above, with `data` and `finalData` consistency guaranteed by construction |
| `CreateTypedArray` / `GetTypedArrayInfo` | TypedArray (`Info.Data` points at the view's first element) |
| `CreateDataView` / `GetDataViewInfo` | DataView |

**Lifetime of aliasing views.** A `[]byte` is valid only while the JS object is
alive and not detached. Never hold it long-term and never touch it from another
goroutine. **There is no safety net here**: the two collectors cannot see each
other's references — Go does not know V8 holds that memory, and V8 does not know
a Go slice points at it. Measured consequences:

- Wrapping a Go slice as an external buffer while passing a raw pointer with
  `finalData=nil`: after one Go GC plus `FreeOSMemory`, V8 reads freed memory —
  access violation (exit code `0xC0000005`). Use
  `CreateExternalArrayBufferFromBytes` / `CreateExternalBufferFromBytes`.
- Reading the `[]byte` returned by `GetArrayBufferInfo` / `GetTypedArrayInfo` /
  `GetDataViewInfo` / `GetBufferInfo` after the JS object was collected: a fatal
  Go runtime error (exit code 2). If you need it across callbacks, hold a
  `napi_ref` and re-read it each time.

**The external-buffer finalizer contract.** Node-API forces `finalize_data` to
be the raw external pointer, so anything the Go side must keep alive (normally
the backing slice and the `FinalizeFunc`) travels through the `finalize_hint`
slot. Passing the backing slice as `finalData` pins it for the buffer's
lifetime — **this is the step people miss**, and missing it produces the
`0xC0000005` above. That is why Go heap memory should go through the
`...FromBytes` variants, which make the step part of construction. Passing both
arguments as nil registers no finalizer at all (a pure loan; the caller
guarantees the lifetime).

## Cleanup hooks

| Function | Notes |
|---|---|
| `AddEnvCleanupHook(env, fn)` | runs at environment shutdown (fire-and-forget) |
| `AddEnvCleanupHookRemovable` / `RemoveEnvCleanupHook` | removable variant (returns a token) |
| `AddAsyncCleanupHook` / `RemoveAsyncCleanupHook` | async variant (may run on another thread). **The callback must call `RemoveAsyncCleanupHook(handle)`**, otherwise teardown hangs forever. Removing twice returns an error instead of crashing. |

## Diagnostics

| Function | Notes |
|---|---|
| `DebugStats() (allocated, freed uint64)` | callback box counters (`outstanding = allocated - freed`); the stress suite asserts on this to detect leaks |

## Test coverage

The 71 assertions in `example/test.mjs` exercise every function in the tables
above. The only exceptions are `FatalError` and `FatalException`: they terminate
the process by design (`napi_fatal_error` prints and then aborts), so they cannot
share a single run with the other assertions.

## The `entry` package

| Function | Notes |
|---|---|
| `entry.Export(name string, cb napi.Callback)` | register a function export |
| `entry.ExportValue(name string, factory func(napi.Env) (napi.Value, error))` | register an arbitrary value (a class, a constant object) |

Both are called from `init()`; registration runs automatically when the module is
loaded.

## The `js` package (`syscall/js`-style)

Failures panic; the callback trampoline recovers and turns them into JavaScript
exceptions.

| API | Notes |
|---|---|
| `js.Wrap(env, napi.Value)` | wrap a native value |
| `js.ValueOf(env, x)` | Go → JS: nil, bool, numeric types, string, `[]any`, `map[string]any`, `[]string`, `func(js.Value, []js.Value) any` |
| `js.FuncOf(env, fn)` | create a JS function |
| `v.Get/Set/Delete/Keys/Index/Len` | properties and elements |
| `v.Call(name, args...)` / `v.Invoke(args...)` / `v.New(args...)` | calls (`Call` uses `v` as `this`) |
| `v.Bool/Float/Int/Int64/String` | type extraction |
| `v.MakeRef()` → `Ref.Value()/Release()` | hold across callbacks |
| `js.Undefined/Null/Global(env)` | singletons |
