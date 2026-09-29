# Stress test report

Reference run: Windows x64 · Node.js 24 · Go 1.27 · MinGW-w64 gcc 14.2.0 (UCRT).

**English** · [简体中文](STRESS.zh-CN.md)

## Running the suite

```sh
go build -buildmode=c-shared -o stress/stress.node ./stress
node --expose-gc stress/run_all.mjs          # all 18 scenarios
node --expose-gc stress/run_all.mjs soak     # filter to one scenario
```

`--expose-gc` lets the driver force collections and make precise post-GC memory
assertions. Any failed assertion makes the process exit non-zero.

## Coverage matrix (18 scenarios, all passing in the latest run)

| # | Scenario | Load | Pass criterion | Measured |
|---|---|---|---|---|
| 1 | Call throughput | 5M `addDoubles` calls | values correct, zero Go heap growth | 0.58M calls/s, goheap +0.0MB |
| 2 | Value churn (Go side) | 300 rounds × 10k composite values | Go heap returns to baseline | goheap +0.0MB |
| 3 | Value churn (through JS) | 1M objects created and dropped one by one | fields correct, memory returns | rss -0.3MB |
| 4 | Wrap churn | 200k objects with finalizers | every finalizer runs, box count back to baseline | 200000/200000 |
| 5 | Function churn | 100k Go-backed JS functions | box count back to baseline after GC | pass |
| 6 | TSFN flood | 8 goroutines × 25k | delivered exactly once, no loss or duplication, boxes return | 200k/200k, 1.7s |
| 7 | TSFN queue full | queue 8 + 100k `NonBlocking` | `napi_queue_full` rejected safely; delivered == enqueued | 16139 enqueued, all delivered |
| 8 | Panic storm | 20k Go panics | all converted to JS exceptions, process survives | pass |
| 9 | String boundaries | utf8/utf16, 15/16/255/4096-byte boundaries, emoji, CJK, embedded NUL, 1MiB × 100 | round-trips identical | pass |
| 10 | BigInt round-trip | 64–576 bit, positive and negative × 2000 | identical | pass |
| 11 | Buffer stress | 20k × 4KB plus one 64MB | checksums correct | pass |
| 12 | Async work flood | 5000 concurrent tasks | all resolve, indices complete | pass |
| 13 | Reference churn | 200k create/get/delete | no errors | pass |
| 14 | Weak references | 20k objects + `gc` | 0 alive after gc (informational) | 0/20000 |
| 15 | Deep reentrancy | JS↔Go 200 levels × 100 | counts correct, no stack overflow | pass |
| 16 | worker_threads | 4 workers × (500k calls + 50k objects) | multiple environments share one Go runtime | pass |
| 17 | Soak (mixed load) | 15s mixed: calls + churn + wrap + TSFN | Go heap quartile drift < 120MB | -2.7MB |
| 18 | Final bookkeeping | registry back to module-load baseline | `outstanding == baseline` | pass |

Also: `go test -race ./napi/` (32 goroutines × 5000 boxes concurrently) passes,
and three consecutive runs of the whole suite produce identical results.

## A note on TSAN

A `go build -race` DLL builds, but **TSAN cannot run inside a V8 host process**
(the shadow-memory mapping collides with V8's reserved address space:
`ThreadSanitizer failed to allocate`). Concurrency coverage is therefore split in
two:

- the pure-Go concurrency core (the cgo handle-box registry) is genuinely covered
  by `go test -race`;
- the cgo→Node-API surface is single-threaded by contract (the JS thread) and is
  covered for behavioural correctness by the 18 stress scenarios.

## Problems the stress suite found and fixed

1. **NULL env on TSFN dispatch**: when node closes a TSFN with a backlog still
   queued, it invokes `call_js_cb` with a NULL env (only to free the data). The
   original implementation used env directly → `invalid_arg`. Fix: check env/data
   for nil, and drop the redundant handle scope (node's `DispatchOne` already
   provides one).
2. **Event-loop keep-alive**: an unreferenced TSFN does not keep the loop alive,
   so during a large dispatch node could exit early if the main thread held no
   other handle ("unsettled top-level await"). The Ref/Unref semantics are now
   documented.
3. **Callback value lifetime (found through the tests)**: capturing a callback's
   `napi.Value` in a closure and using it later yields a dead handle. The demo
   now uses a strong reference released by the host object's finalizer.
4. **Strong-reference cycle**: a Go strong reference plus a JS closure
   (ref → jsLevel → goStep → finalizer) deadlocks the finalizer so it never
   fires. The way to break the cycle is now documented.

The first two are binding fixes; the last two are documentation and example
fixes. All four are traps any user of the binding will hit.

## Environment and reproducibility

The thresholds are deliberately loose (for example the soak Go heap drift
allowance of 120MB) to avoid CI flakiness, and the numbers above come from one
reference run. The weak-reference scenario is informational — GC timing is not a
hard guarantee.
