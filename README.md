# napi-go

[![CI](https://github.com/all-thoughts-are-broken/napi-go/actions/workflows/ci.yml/badge.svg)](https://github.com/all-thoughts-are-broken/napi-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/all-thoughts-are-broken/napi-go.svg)](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/all-thoughts-are-broken/napi-go)](https://goreportcard.com/report/github.com/all-thoughts-are-broken/napi-go)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Write Node.js native addons in Go.**

`napi-go` is a complete binding of the **Node-API (N-API) stable C ABI**. It lets
you compile ordinary Go code into a `.node` addon that Node.js loads like any
other native module — no C++, no `node-gyp`, no Node headers to install, and no
recompilation when you upgrade Node.js.

**English** · [简体中文](README.zh-CN.md)

```go
package main

import (
	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

func Add(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	a, _ := napi.GetValueDouble(env, cb.Args[0])
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	sum, _ := napi.CreateDouble(env, a+b)
	return sum
}

func init() { entry.Export("add", Add) }

func main() {}
```

```sh
go build -buildmode=c-shared -o addon.node .
```

```js
const addon = require('./addon.node');
addon.add(1, 2); // 3
```

---

## Why napi-go

- **One binary, every Node.js version.** The addon only calls the Node-API 8
  stable ABI, so a single build loads on every release that ships Node-API 8
  (12.22+, 14.17+, 16+, and everything newer). No ABI rebuild per Node release.
- **No Node.js toolchain.** Official Node-API headers are vendored in
  `include/node/`, and on Windows a small import stub (`windows/node.lib`)
  resolves the `napi_*` symbols against the running `node.exe` at load time. On
  Linux and macOS the dynamic loader resolves them from the host process.
- **Not tied to MSVC.** Node-API is a pure C ABI, so MinGW-w64 gcc and clang
  work just as well as `cl.exe`. See
  [docs/DESIGN.md](docs/DESIGN.md) for the reasoning.
- **Full API surface.** All 138 exported functions of the `napi` package map
  1:1 onto the C API, each covered by a functional assertion.
- **Two layers, your choice.** Use `napi` for explicit, error-returning calls,
  or `js` for a `syscall/js`-style convenience API.
- **Misuse returns errors, not crashes.** Reference counts, deferreds, scopes,
  async work and threadsafe functions are tracked by the binding, so
  double-free-style mistakes surface as `error` values instead of segmentation
  faults.
- **Cross-version correctness, not just "no crash".** The test suites assert on
  values, finalizer bookkeeping and memory slopes, and the adversarial probe
  suite deliberately exercises documented-but-undefined edge cases.

## Requirements

| | |
|---|---|
| Go | 1.24 or newer |
| C toolchain | MinGW-w64 gcc (UCRT) on Windows; gcc or clang on Linux/macOS |
| Node.js | Any release with Node-API 8 (12.22+, 14.17+, 16+). Node.js 18+ recommended. |

Nothing else — the headers and the Windows import library ship with the module.

## Install

```sh
go get github.com/all-thoughts-are-broken/napi-go/napi
go get github.com/all-thoughts-are-broken/napi-go/entry
```

For the convenience layer, add `github.com/all-thoughts-are-broken/napi-go/js`.

Then build your addon package as a shared library:

```sh
go build -buildmode=c-shared -o addon.node .
```

## Packages

| Package | Purpose |
|---|---|
| [`napi`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/napi) | Core bindings: the whole Node-API 8 surface, 1:1 with the C API. Failures are returned as `error` (`napi.AsStatus` unwraps a `Status`). |
| [`entry`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/entry) | Module registration: `entry.Export(name, cb)` and `entry.ExportValue(name, factory)`, called from `init()`. |
| [`js`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/js) | `syscall/js`-style convenience layer: `js.ValueOf`, `js.FuncOf`, `Value.Get/Call/Set`. It panics on failure, and the callback trampoline converts that panic into a JavaScript exception. |

## Documentation

- [API reference](docs/API.md) — every function, grouped by topic.
- [Design notes](docs/DESIGN.md) — ABI strategy, the cgo layout, the callback
  trampoline, lifetime rules, and the known limitations.
- [Stress test report](docs/STRESS.md) — the 18 scenarios and their numbers.
- [Build & troubleshooting](BUILD.md)

## Usage

### The low-level `napi` style

Every call returns an `error`; `nil` means `napi_ok`.

```go
func Add(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, err := napi.GetCbInfo(env, info)
	if err != nil {
		return nil
	}
	a, err := napi.GetValueDouble(env, cb.Args[0])
	if err != nil {
		return nil // a pending JS exception is already set
	}
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	sum, _ := napi.CreateDouble(env, a+b)
	return sum
}
```

### The `js` convenience style

```go
func Hello(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	name := js.Wrap(env, cb.Args[0]).String()
	return js.ValueOf(env, "hello, "+name).Raw()
}
```

### Calling JavaScript from a goroutine

The only legal way to call into JS from another thread is a threadsafe
function. `napi-go` supports a full payload closure, so the callback can create
values and call JS directly:

```go
func Greet(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	jsCallback := cb.Args[1]

	tsfn, _ := napi.NewThreadsafeFunction(env, jsCallback, 0, 1)
	tsfn.Unref(env)
	go func() {
		time.Sleep(time.Second)
		tsfn.Call(func(e napi.Env, fn napi.Value) {
			msg, _ := napi.CreateStringUtf8(e, "from goroutine")
			undef, _ := napi.GetUndefined(e)
			napi.CallFunction(e, undef, fn, msg)
		}, napi.Blocking)
		tsfn.Release(napi.Release)
	}()
	return nil
}
```

> `Release(napi.Abort)` invalidates the handle: the engine destroys the object on
> the next event-loop turn, so every later call — including the remaining
> threads' `Release` — would touch freed memory. The binding rejects all of
> them with `napi_closing`. Prefer plain `Release` when you can.

### Promises with async work (libuv thread pool)

```go
func SlowDouble(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	input, _ := napi.GetValueDouble(env, cb.Args[0])

	promise, deferred, _ := napi.CreatePromise(env)
	work, _ := napi.NewAsyncWork(env, "slowDouble",
		func(e napi.Env) { /* runs on a pool thread: pure Go only */ },
		func(e napi.Env, err error) {
			out, _ := napi.CreateDouble(e, input*2)
			napi.ResolveDeferred(e, deferred, out)
		})
	work.Queue(env)
	return promise
}
```

## ABI strategy

- The binding calls **only** the Node-API stable C ABI (`NAPI_VERSION=8`) and
  never touches V8 or C++ internals.
- `include/node/` vendors the official `node_api.h` / `js_native_api.h` headers
  (MIT, from the Node.js project). `windows/node.lib` is an import stub — it
  contains symbols only, no code — so the DLL binds to whichever `node.exe`
  loads it.
- Node-API 9/10 functions (external strings, property keys, syntax errors, …)
  are deliberately **not** bound: they would add link-time symbol dependencies
  and break the "build once, load anywhere" guarantee.
- At runtime, `napi.GetVersion(env)` reports the highest Node-API version the
  engine supports.

## Platform support

| Platform | Linking | Status |
|---|---|---|
| Windows x64 | Links the bundled `windows/node.lib` import stub | ✅ Verified |
| Linux x64 / arm64 | No library; `-Wl,--unresolved-symbols=ignore-all`, symbols resolved from the host process at `dlopen` time | ✅ Built and tested in CI |
| macOS x64 / arm64 | `-Wl,-undefined,dynamic_lookup` | ✅ Built and tested in CI |

Because Linux and macOS hosts export the `napi_*` symbols from the main
executable, the dynamic loader resolves them automatically — no extra files
needed. Building on those platforms only requires Go and a C compiler.

Toolchain notes:

| Toolchain | Status |
|---|---|
| MinGW-w64 gcc (UCRT) | ✅ Supported — the reference Windows toolchain |
| clang / lld-link (windows-msvc target) | ⚠️ Compiles and links, but Go runtime initialisation hangs at load time; under investigation, not supported yet |
| MSVC (`cl.exe`) | Not supported — the Go cgo driver does not use MSVC |
| gcc / clang (Linux, macOS) | ✅ Supported |

## Concurrency and lifetime rules

- Outside the JS thread, a goroutine may only use
  `ThreadsafeFunction.Call/Acquire/Release/Ref/Unref`.
- A `napi.Value` is valid only for the duration of the current callback. To keep
  a JS value across callbacks use `napi.CreateReference` (refcount `0` for a weak
  reference) and release it from the host object's finalizer. **Capturing a
  callback argument in a closure and using it later is undefined behaviour**,
  and a strong reference held by a JS closure can form an unreclaimable cycle.
- Every Go callback is wrapped in `recover()`: a panic becomes a JavaScript
  exception instead of killing the process. Panics inside finalizers are logged.
- When the JS function object for a Go callback is collected, its registry entry
  is released automatically (`napi_add_finalizer`); the stress scenarios assert
  this on the box counter.
- The `[]byte` views returned for buffers and array buffers are valid only while
  the JS object is alive and not detached. Do not hold them long-term.
- Synchronous JS↔Go reentrancy is bounded by the V8 JS stack (a few hundred
  frames). Deep recursion should be turned into promise- or TSFN-based async.
- Under `worker_threads`, every worker must `require` the **same addon file
  path**. A copy of the file per worker instantiates a second Go runtime inside
  one process, which is undefined and always crashes.

See [docs/DESIGN.md](docs/DESIGN.md) for the full reasoning and the measurements
behind each rule.

## Testing

```sh
# Functional assertions (all 138 exported functions)
go build -buildmode=c-shared -o example/goaddon.node ./example
node --expose-gc example/test.mjs

# 18 stress scenarios
go build -buildmode=c-shared -o stress/stress.node ./stress
node --expose-gc stress/run_all.mjs

# Adversarial probes: documented-but-undefined edge cases
go build -buildmode=c-shared -o probe/probe.node ./probe
node --expose-gc probe/run.mjs
node --expose-gc probe/edge.mjs

# Concurrency unit tests
go test -race ./napi/...
```

Current status: **71/71** functional assertions, **18/18** stress scenarios,
`-race` clean, and **0 bugs** reported by the probe suites (the remaining probe
notes are documented engine behaviours, not binding defects).

CI runs the functional suite on Linux, macOS and Windows across Node.js 18, 20,
22 and 24, and runs the stress and probe suites and the race detector once.

## Versioning

The project follows [Semantic Versioning](https://semver.org/). Release notes
are in [CHANGELOG.md](CHANGELOG.md).

Because the addon targets the Node-API stable ABI, upgrading Node.js never
requires a rebuild. Upgrading the *headers* is optional: replace the four files
under `include/node/` with a newer Node.js release and refresh
`windows/node.lib` from `https://nodejs.org/dist/v<version>/win-x64/node.lib`.

## Contributing

Issues and pull requests are welcome. Before opening a pull request, please make
sure:

```sh
gofmt -l .        # must print nothing
go vet ./...      # must be clean
go test -race ./napi/...
```

and that the functional and stress suites still pass.

## License

[MIT](LICENSE). The vendored Node.js headers and `windows/node.lib` are
redistributed unmodified under the Node.js project's MIT license; see
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
