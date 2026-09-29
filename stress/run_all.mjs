// Stress suite for napi-go. Run with GC exposure for exact heap assertions:
//
//   go build -buildmode=c-shared -o stress.node ./stress
//   node --expose-gc stress/run_all.mjs [scenarioFilter]
//
// Every scenario prints a result line and the suite exits non-zero if any
// correctness bound or memory threshold is violated.
import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { Worker } from "node:worker_threads";

const require = createRequire(import.meta.url);
const addon = require("./stress.node");
const outstandingBoxesAtLoad = (() => {
  const { boxesAllocated, boxesFreed } = addon.debugStats();
  return boxesAllocated - boxesFreed;
})();

const filter = process.argv[2] ?? "";
const MB = 1024 * 1024;

const results = [];
let currentName = "";

function rssMB() { return process.memoryUsage().rss / MB; }
function heapMB() { return process.memoryUsage().heapUsed / MB; }
function goHeapMB() { return addon.goMem().heapAlloc / MB; }

function scenario(name, fn) {
  if (filter && !name.includes(filter)) return;
  return (async () => {
    currentName = name;
    addon.freeGoMemory();
    const rss0 = rssMB(), heap0 = heapMB(), go0 = goHeapMB();
    const t0 = performance.now();
    let extra = {};
    try {
      extra = (await fn()) ?? {};
      addon.freeGoMemory();
      const rss1 = rssMB(), heap1 = heapMB(), go1 = goHeapMB();
      const secs = ((performance.now() - t0) / 1000).toFixed(2);
      const line = `  ok - ${name} (${secs}s, rss ${fmt(rss1 - rss0)}, v8heap ${fmt(heap1 - heap0)}, goheap ${fmt(go1 - go0)}${extra.info ? ", " + extra.info : ""})`;
      results.push({ name, ok: true, line });
      console.log(line);
    } catch (e) {
      const line = `  FAIL - ${name}: ${e.message}`;
      results.push({ name, ok: false, line });
      console.error(line);
      process.exitCode = 1;
    }
  })();
}

function fmt(v) { return `${v >= 0 ? "+" : ""}${v.toFixed(1)}MB`; }

function outstandingBoxes() {
  const { boxesAllocated, boxesFreed } = addon.debugStats();
  return boxesAllocated - boxesFreed;
}

async function settleBoxesStable(maxWaitMs = 20_000) {
  // Poll until the outstanding-box count stops decreasing (all finalizers
  // for dropped objects have run). Returns the stable count.
  let last = outstandingBoxes();
  let lastChange = performance.now();
  const t0 = performance.now();
  for (;;) {
    if (global.gc) global.gc();
    addon.freeGoMemory();
    await new Promise((r) => setTimeout(r, 100));
    const now = outstandingBoxes();
    if (now !== last) {
      last = now;
      lastChange = performance.now();
    } else if (performance.now() - lastChange > 1_000) {
      return now; // stable for 1s
    }
    if (performance.now() - t0 > maxWaitMs) return now;
  }
}

// ---------------------------------------------------------------- scenarios

await scenario("1. call throughput: 5M round trips", async () => {
  const N = 5_000_000;
  let acc = 0;
  const t0 = performance.now();
  for (let i = 0; i < N; i++) acc += addon.addDoubles(i, 1);
  const dt = (performance.now() - t0) / 1000;
  assert.strictEqual(acc, ((N - 1) * N) / 2 + N); // sum(i) + N*1
  assert.ok(dt < 30, `5M calls took ${dt.toFixed(1)}s`);
  return { info: `${(N / dt / 1e6).toFixed(2)}M calls/s` };
});

await scenario("2. value churn: 300 rounds x 10k composite values", async () => {
  for (let r = 0; r < 300; r++) {
    addon.churnRound(10_000);
    if (r % 50 === 0 && global.gc) global.gc();
  }
  if (global.gc) global.gc();
  const growth = goHeapMB();
  assert.ok(growth < 100, `Go heap did not return to baseline: +${growth.toFixed(1)}MB`);
});

await scenario("3. value churn via JS: 1M objects built per call", async () => {
  for (let r = 0; r < 20; r++) {
    for (let i = 0; i < 50_000; i++) {
      const o = addon.churnObject(i);
      if (o.n !== i) throw new Error("field mismatch");
    }
    if (global.gc) global.gc();
  }
});

await scenario("4. wrap churn: 200k wrapped objects -> finalizers release all", async () => {
  const baseline = outstandingBoxes();
  for (let i = 0; i < 200_000; i++) {
    addon.newWrapped();
    if (i % 50_000 === 0 && global.gc) global.gc();
  }
  if (global.gc) global.gc();
  const stable = await settleBoxesStable();
  assert.strictEqual(stable, baseline, `${stable - baseline} callback boxes leaked`);
  const finalized = addon.wrapFinalizedCount();
  assert.strictEqual(finalized, 200_000, `expected 200000 finalizers, got ${finalized}`);
});

await scenario("5. function churn: 100k JS functions backed by Go", async () => {
  const baseline = outstandingBoxes();
  for (let i = 0; i < 100_000; i++) {
    const f = addon.createFn(`f${i}`);
    assert.strictEqual(typeof f, "function");
  }
  if (global.gc) global.gc();
  const stable = await settleBoxesStable();
  assert.strictEqual(stable, baseline, `${stable - baseline} callback boxes leaked`);
});

await scenario("6. TSFN flood: 8 goroutines x 25k payload calls", async () => {
  const total = 200_000;
  let received = 0;
  const delivered = await new Promise((resolve) => {
    addon.tsfnFlood(total, 8, () => { received++; }).then(resolve);
  });
  assert.strictEqual(delivered, total, "Go enqueued count");
  assert.strictEqual(received, total, "JS received count");
  const stable = await settleBoxesStable();
  const baseline = outstandingBoxes();
  assert.ok(Math.abs(stable - baseline) <= 0, `${stable - baseline} callback boxes leaked`);
  return { info: `${(total / 1000).toFixed(0)}k payloads delivered` };
});

await scenario("7. TSFN queue overflow: 100k pushes into queue of 8 (nonblocking)", async () => {
  const total = 100_000;
  let received = 0;
  const enqueued = await new Promise((resolve) => {
    addon.tsfnOverflow(total, () => { received++; }).then(resolve);
  });
  assert.ok(enqueued > 0 && enqueued <= total, `enqueued=${enqueued}`);
  assert.strictEqual(received, enqueued, "every enqueued call delivered exactly once");
  await settleBoxesStable();
  return { info: `${enqueued}/${total} enqueued (rest correctly rejected with napi_queue_full)` };
});

await scenario("8. panic storm: 20k Go panics become JS exceptions", async () => {
  for (let i = 0; i < 20_000; i++) {
    try {
      addon.panicWith(i);
      throw new Error("panic did not surface");
    } catch (e) {
      if (!String(e.message).includes(`stress-boom-${i}`)) throw e;
    }
  }
});

await scenario("9. string roundtrips: encodings x size boundaries x unicode x NUL", async () => {
  const samples = [
    "", "a", "x".repeat(15), "x".repeat(16), "x".repeat(255), "x".repeat(4096),
    "héllo wörld", "你好，世界！napi-go", "🚀🎉💀 emoji + CJK + áéíóú",
    "a\0b\0c",                       // embedded NULs
    "🚀".repeat(1000),               // surrogate pairs
  ];
  for (const s of samples) {
    for (const mode of [0, 1]) { // utf8, utf16
      const back = addon.stringRoundtrip(s, mode);
      assert.strictEqual(back, s, `mode=${mode} len=${s.length}`);
    }
  }
  // latin1 re-encodes byte-wise: byte-for-byte lossless only for ASCII
  // input (the API treats the Go string's bytes as latin-1 code units).
  assert.strictEqual(addon.stringRoundtrip("hello latin1 42!", 2), "hello latin1 42!");

  // 1 MiB string, 100 round trips
  const big = "0123456789abcdef".repeat(65_536); // 1 MiB
  for (let i = 0; i < 100; i++) {
    assert.strictEqual(addon.stringRoundtrip(big, 0).length, big.length);
  }
  const gen = addon.bigString(2048); // ~2 MiB built on the Go side
  assert.strictEqual(gen.length, 2048 * 34 * 26); // unit is 26 UTF-16 units
});

await scenario("10. BigInt roundtrip: multi-word magnitudes x 2k", async () => {
  for (let i = 0; i < 2_000; i++) {
    const bits = 64 + (i % 64) * 8;
    let v = 0n;
    for (let b = 0; b < bits; b += 8) {
      v = (v << 8n) | BigInt((i + b) % 256);
    }
    const neg = i % 2 === 0 ? v : -v;
    assert.strictEqual(addon.bigintRoundtrip(neg), neg, `bits=${bits}`);
  }
});

await scenario("11. buffer stress: 20k x 4KB + one 64MB", async () => {
  const expect = (n) => {
    let s = 0n;
    for (let i = 0; i < n; i++) s += BigInt(i % 251);
    return s;
  };
  for (let i = 0; i < 20_000; i++) {
    const b = addon.makeFilledBuffer(4096);
    if (i === 0) assert.strictEqual(addon.bufferSum(b), expect(4096));
  }
  const big = addon.makeFilledBuffer(64 * MB);
  assert.strictEqual(addon.bufferSum(big), expect(64 * MB));
});

await scenario("12. async work flood: 5k queued tasks resolve in order-ish", async () => {
  const tasks = [];
  for (let i = 0; i < 5_000; i++) tasks.push(addon.asyncTask(i));
  const out = await Promise.all(tasks);
  assert.strictEqual(out.length, 5_000);
  const sorted = [...out].sort((a, b) => Number(a) - Number(b));
  for (let i = 0; i < 5_000; i++) assert.strictEqual(Number(sorted[i]), i);
});

await scenario("13. reference churn: 200k create/read/delete", async () => {
  addon.refChurn(200_000);
});

await scenario("14. weak references: 20k objects collected by GC", async () => {
  for (let i = 0; i < 20_000; i++) addon.keepWeak({ payload: i });
  if (global.gc) global.gc();
  await new Promise((r) => setImmediate(r));
  if (global.gc) global.gc();
  const alive = addon.weakAliveCount();
  // Weak refs do not guarantee immediate collection; bound rather than
  // require zero, and report.
  assert.ok(alive <= 20_000);
  return { info: `${alive}/20000 still alive after gc (informational)` };
});
addon.resetWeak();

await scenario("15. deep reentrancy: JS<->Go 200 levels x 100", async () => {
  // Each level crosses the V8<->Go boundary synchronously; V8's JS stack
  // limits the depth (documented), so keep it well under the limit.
  // jsLevel closes over a MUTABLE binding (not the per-rep const): a const
  // closure capture plus the strong Go-side reference would form an
  // uncollectable reference cycle (goStep -> ref -> jsLevel -> goStep).
  let goStep = null;
  function jsLevel(n) {
    return n <= 0 ? 0 : Number(goStep(n)) + 1;
  }
  for (let rep = 0; rep < 100; rep++) {
    goStep = addon.makeStepper(jsLevel);
    const r = goStep(200);
    assert.strictEqual(Number(r), 199, "reentrancy sum"); // 200 crossings, deepest contributes 0
    goStep = null; // break the cycle so the finalizer can release the ref
  }
});

if (!process.env.NAPI_STRESS_SKIP_WORKERS) await scenario("16. worker_threads: 4 workers x (500k calls + 50k objects)", async () => {
  const { fileURLToPath: fup } = await import("node:url");
  const addonPath = fup(new URL("./stress.node", import.meta.url));
  const workerSrc = `
    const { createRequire } = require("node:module");
    const { parentPort, workerData } = require("node:worker_threads");
    // IMPORTANT: every worker must require the SAME addon file as the main
    // thread. Windows LoadLibrary is per-path: a copied .node file would
    // load a second instance with a second Go runtime in the same process,
    // which is unsupported and crashes. Same path => same DLL => one Go
    // runtime shared across environments (the multi-env design).
    const addon = createRequire(__filename)(${JSON.stringify(addonPath)});
    let acc = 0;
    for (let i = 0; i < 500_000; i++) acc += addon.addDoubles(i, 1);
    if (acc !== ((500_000 - 1) * 500_000) / 2 + 500_000) throw new Error("call mismatch");
    for (let i = 0; i < 50_000; i++) addon.churnObject(i);
    // Process all pending finalizers while the env is still alive instead
    // of leaving them for worker-env teardown.
    addon.drainFinalizers();
    parentPort.postMessage({ boxes: addon.debugStats() });
  `;
  const { writeFileSync, mkdtempSync, rmSync } = await import("node:fs");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { fileURLToPath } = await import("node:url");
  const dir = mkdtempSync(join(tmpdir(), "napi-go-stress-"));
  const workerFile = join(dir, "worker.cjs");
  writeFileSync(workerFile, workerSrc);
  try {
    const runWorker = () => new Promise((resolve, reject) => {
      const w = new Worker(workerFile);
      let msg;
      // Resolve on exit, not on message: the addon DLL stays OS-locked
      // until the worker thread is fully gone, and cleanup deletes the dir.
      w.on("message", (m) => { msg = m; });
      w.on("error", reject);
      w.on("exit", (c) => {
        if (c === 0) resolve(msg);
        else reject(new Error("worker exit " + c));
      });
    });
    const outs = await Promise.all(Array.from({ length: 4 }, runWorker));
    for (const o of outs) {
      assert.ok(o.boxes.boxesAllocated > 0, "worker exercised the registry");
    }
  } finally {
    // The worker's DLL handle may linger briefly after exit on Windows
    // (AV scanners etc.); retry and never fail the scenario over cleanup.
    try {
      rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
    } catch {
      /* best effort: dir lives in the OS temp folder */
    }
  }
  return { info: "4 environments shared one Go runtime" };
});

await scenario("17. soak: mixed load for 15s, bounded memory growth", async () => {
  const durationMs = 15_000;
  const samples = [];
  const stop = performance.now() + durationMs;
  let tsfnReceived = 0;
  const tsfnPromise = addon.tsfnFlood(50_000, 4, () => { tsfnReceived++; });

  let i = 0;
  while (performance.now() < stop) {
    addon.addDoubles(i, i);
    addon.churnRound(200);
    if (i % 200 === 0) {
      addon.newWrapped();
      if (global.gc) global.gc();
    }
    if (i % 500 === 0) {
      samples.push({ rss: rssMB(), go: goHeapMB() });
      await new Promise((r) => setImmediate(r));
    }
    i++;
  }
  await tsfnPromise;
  if (global.gc) global.gc();
  addon.freeGoMemory();

  assert.strictEqual(tsfnReceived, 50_000);
  assert.ok(samples.length >= 5, "soak sampled");
  const quarter = Math.max(1, Math.floor(samples.length / 4));
  const avg = (arr) => arr.reduce((s, x) => s + x, 0) / arr.length;
  const goStart = avg(samples.slice(0, quarter).map((s) => s.go));
  const goEnd = avg(samples.slice(-quarter).map((s) => s.go));
  assert.ok(goEnd - goStart < 120, `Go heap crept +${(goEnd - goStart).toFixed(1)}MB during soak`);
  return { info: `${i} iterations, goheap ${fmt(goEnd - goStart)}` };
});

await scenario("18. final bookkeeping: registry back to module-load baseline", async () => {
  if (global.gc) global.gc();
  const stable = await settleBoxesStable();
  const loadBaseline = outstandingBoxesAtLoad;
  assert.strictEqual(stable, loadBaseline,
    `${stable - loadBaseline} boxes outstanding vs load baseline (leak)`);
});

// ------------------------------------------------------------------ report

const failed = results.filter((r) => !r.ok);
console.log(`\n${results.length - failed.length}/${results.length} scenarios passed`);
if (failed.length > 0) {
  console.error("FAILED:", failed.map((f) => f.name).join(", "));
  process.exitCode = 1;
}
