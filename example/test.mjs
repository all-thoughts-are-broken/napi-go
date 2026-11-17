// End-to-end test for the napi-go example addon.
// Run: node --test test.mjs   (or plain: node test.mjs)
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, copyFileSync, mkdtempSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
const require = createRequire(import.meta.url);
const addon = require("./goaddon.node");
const ADDON_PATH = fileURLToPath(new URL("./goaddon.node", import.meta.url));

// runIsolated runs a CommonJS script in a fresh Node.js process, with the
// addon's path in process.argv[1]. The lifecycle bugs it is used for take the
// whole process down, so they can only be observed from the outside — and the
// process running this file has already loaded the addon, which hides the ones
// that depend on who loaded it.
function runIsolated(source, ...args) {
  return runIsolatedIn(process.execPath, source, ...args);
}

// runIsolatedIn is runIsolated with a chosen Node.js executable.
function runIsolatedIn(execPath, source, ...args) {
  const r = spawnSync(execPath, ["--expose-gc", "-e", source, ADDON_PATH, ...args], {
    encoding: "utf8",
    timeout: 60_000,
  });
  const describe = () =>
    (r.error ? `could not run: ${r.error.message}\n     ` : "") +
    `exit status ${r.status === null ? `signal ${r.signal}` : `0x${(r.status >>> 0).toString(16)}`}` +
    `\n     stdout: ${(r.stdout ?? "").trim()}\n     stderr: ${(r.stderr ?? "").trim()}`;
  return { ...r, describe };
}

let passed = 0;
const pending = [];
function check(name, fn) {
  try {
    const r = fn();
    if (r && typeof r.then === "function") {
      pending.push(
        r.then(
          () => { passed++; console.log("  ok -", name); },
          (e) => { console.error("  FAIL -", name, "\n    ", e.message); process.exitCode = 1; },
        )
      );
    } else {
      passed++;
      console.log("  ok -", name);
    }
  } catch (e) {
    console.error("  FAIL -", name, "\n    ", e.message);
    process.exitCode = 1;
  }
}

console.log("version:", JSON.stringify(addon.versionInfo()));
assert.ok(addon.versionInfo().napi >= 8);
addon.setupCleanup();

// primitives
check("hello", () => assert.strictEqual(addon.hello("世界"), "hello, 世界"));
check("add", () => assert.strictEqual(addon.add(2.5, 4), 6.5));
check("isOdd", () => {
  assert.strictEqual(addon.isOdd(3), true);
  assert.strictEqual(addon.isOdd(4), false);
});
check("concat", () => assert.strictEqual(addon.concat(["a", "bb", "c"]), "abbc"));

// objects
check("readProps", () => {
  const r = addon.readProps({ alpha: 1, beta: 2 });
  assert.deepStrictEqual([...r], ["alpha=1", "beta=2"]);
  assert.strictEqual(r.summary, "sum=3");
});
check("makeUser", () => {
  const u = addon.makeUser(42, "田中");
  assert.strictEqual(u.id, 42);
  assert.strictEqual(u.name, "田中");
});
check("freezeDemo", () => {
  const o = addon.freezeDemo();
  assert.strictEqual(o.v, 7);
  assert.throws(() => { "use strict"; o.v = 9; });
});

// binary data
check("makeBuffer + bufferSum", () => {
  const buf = addon.makeBuffer(10);
  assert.ok(Buffer.isBuffer(buf));
  assert.strictEqual(buf.length, 10);
  assert.strictEqual(buf[3], 3);
  assert.strictEqual(addon.bufferSum(buf), 45n);
});
check("typedArraySum", () => {
  const u8 = new Uint8Array([10, 20, 30]);
  assert.strictEqual(addon.typedArraySum(u8), 60n);
  const offsetView = new Uint8Array(new Uint8Array([9, 9, 1, 2, 3, 9]).buffer, 2, 3);
  assert.strictEqual(addon.typedArraySum(offsetView), 6n);
});
check("makeArrayBufferCopy", () => {
  const view = addon.makeArrayBufferCopy(Buffer.from([5, 6, 7]));
  assert.ok(view instanceof Uint8Array);
  assert.deepStrictEqual([...view], [5, 6, 7]);
});

// class
check("Counter class", () => {
  const Counter = addon.CounterClass;
  const c = new Counter(10);
  assert.strictEqual(c.inc(), 11);
  assert.strictEqual(c.inc(), 12);
  assert.strictEqual(c.value, 12);
  assert.strictEqual(Counter.describe(), "Counter counts things");
  assert.throws(() => Counter(), /new/);
});

// wrap
check("wrapMap", () => {
  const m = addon.wrapMap();
  m.set("lang", "Go");
  assert.strictEqual(m.get("lang"), "Go");
  assert.strictEqual(m.get("missing"), undefined);
  assert.strictEqual(m.size, 1);
});

// external / references / instance data
check("externalRoundtrip", () => assert.strictEqual(addon.externalRoundtrip(), "hello"));
check("keepValue/takeKept", () => {
  const obj = { magic: 123 };
  const slot = addon.keepValue(obj);
  const back = addon.takeKept(slot);
  assert.strictEqual(back.magic, 123);
});
check("instanceDataDemo", () => assert.strictEqual(addon.instanceDataDemo(), "instance"));

// errors & panics
check("throwBoomer", () => {
  assert.throws(() => addon.throwBoomer(), (e) => e.code === "ERR_BOOM" && /explicit/.test(e.message));
});
check("panicFn recovered", () => {
  assert.throws(() => addon.panicFn(), /go panic inside callback/);
});

// misc
check("runScriptDemo", () => assert.strictEqual(addon.runScriptDemo(), 42));
check("bigintOps", () => {
  assert.strictEqual(addon.bigintOps(123456789012345678901234567890n),
                     123456789012345678901234567890n);
  assert.strictEqual(addon.bigintOps(-42n), -42n);
});
check("nowDate", () => {
  const d = addon.nowDate();
  assert.ok(d instanceof Date);
  assert.ok(Math.abs(Date.now() - d.getTime()) < 10_000);
});
check("makeSymbol", () => assert.strictEqual(typeof addon.makeSymbol(), "symbol"));
check("jsDemo", () => {
  const r = addon.jsDemo({
    greeting: "hi",
    times: 3,
    shout(x) { return x.toUpperCase(); },
  });
  assert.strictEqual(r.line, "HI (hi) x3");
  assert.deepStrictEqual([...r.list], ["a", 2, true]);
});
check("keysDemo", () => assert.deepStrictEqual([...addon.keysDemo({ b: 1, a: 2, c: 3 })], ["a", "b", "c"]));

check("callWithAsyncHooks", () => {
  const r = addon.callWithAsyncHooks(function () { return 7 * 6; });
  assert.strictEqual(r, 42);
});

// async
check("asyncDouble (promise via async work)", async () => {
  const r = await addon.asyncDouble(21);
  assert.strictEqual(r, 42); // int64 comes back as a JS number
});

check("tsfnGreet (threadsafe function with payload)", async () => {
  let cbMessage = null;
  const r = await addon.tsfnGreet("bob", (m) => { cbMessage = m; });
  assert.strictEqual(r, "pong: bob");
  assert.strictEqual(cbMessage, "pong: bob");
});

// ---- advanced coverage ------------------------------------------------------

check("Counter setter", () => {
  const c = new addon.CounterClass(1);
  c.value = 100;
  assert.strictEqual(c.value, 100);
  assert.strictEqual(c.inc(), 101);
});

check("newCounterFromGo (NewInstance + InstanceOf)", () => {
  const r = addon.newCounterFromGo(50);
  assert.strictEqual(r.isInstanceOf, true);
  assert.ok(r.instance instanceof addon.CounterClass);
  assert.strictEqual(r.instance.value, 50);
  assert.strictEqual(r.instance.inc(), 51);
});

check("symbolProps (symbol-keyed property)", () => {
  const sym = addon.makeSymbol();
  const o = addon.symbolProps(sym);
  assert.strictEqual(o[sym], "via symbol");
  assert.strictEqual(o.plain, "plain");
  assert.ok(!Object.keys(o).includes(sym), "symbol keys are not enumerable string keys");
});

check("readOnlyProp (attributes: no writable)", () => {
  const o = addon.readOnlyProp("frozen value");
  assert.strictEqual(o.ro, "frozen value");
  assert.throws(() => { o.ro = "mutated"; }, TypeError);
});

check("makeAccessor (getter+setter accessor)", () => {
  const o = addon.makeAccessor(10);
  assert.strictEqual(o.value, 10);
  o.value = 7;
  assert.strictEqual(o.value, 14); // setter transforms x2
});

check("escapeDemo (escapable handle scope)", () => {
  assert.strictEqual(addon.escapeDemo(), 9);
});

check("safeCall (catch pending exception)", () => {
  const ok = addon.safeCall(() => 42);
  assert.strictEqual(ok.ok, true);
  assert.strictEqual(ok.value, 42);

  const bad = addon.safeCall(() => { throw new Error("nope"); });
  assert.strictEqual(bad.ok, false);
  assert.strictEqual(bad.message, "nope");
});

check("coerceDemo (JS coercions)", () => {
  const r = addon.coerceDemo("");
  assert.strictEqual(r.asBool, false);
  assert.strictEqual(r.asString, "");
  const r2 = addon.coerceDemo(0);
  assert.strictEqual(r2.asBool, false);
  assert.strictEqual(r2.asString, "0");
});

check("errorValueDemo (Error as a value + IsError)", () => {
  const r = addon.errorValueDemo();
  assert.strictEqual(r.isError, true);
  assert.ok(r.error instanceof Error);
  assert.strictEqual(r.error.code, "ERR_DEMO");
  assert.strictEqual(r.error.message, "error as a value");
});

check("asyncFail (Promise rejection path)", async () => {
  await assert.rejects(addon.asyncFail(), (e) =>
    e.code === "ERR_FAIL" && e.message === "async failure from Go");
});

check("bufferCopy", () => {
  const src = Buffer.from([1, 2, 3]);
  const copy = addon.bufferCopy(src);
  assert.ok(Buffer.isBuffer(copy));
  assert.deepStrictEqual([...copy], [1, 2, 3]);
  copy[0] = 99;
  assert.strictEqual(src[0], 1); // independent copy
});

check("externalArrayBufferDemo (zero-copy from Go)", () => {
  const u8 = addon.externalArrayBufferDemo(8);
  assert.ok(u8 instanceof Uint8Array);
  assert.deepStrictEqual([...u8], [0, 1, 2, 3, 4, 5, 6, 7]);
});

check("makeDataView", () => {
  const dv = addon.makeDataView(8);
  assert.ok(dv instanceof DataView);
  assert.strictEqual(dv.byteLength, 8);
  assert.strictEqual(dv.getUint8(3), 3);
});

check("detachDemo (ArrayBuffer detach)", () => {
  const r = addon.detachDemo();
  assert.strictEqual(r.detached, true);
  assert.strictEqual(r.ab.byteLength, 0);
});

check("utf16Demo", () => {
  for (const s of ["ascii", "你好 🚀 world"]) {
    assert.strictEqual(addon.utf16Demo(s), s);
  }
});

check("adjustMemoryDemo", () => {
  const r = addon.adjustMemoryDemo();
  // The API returns the post-adjustment total: +1MiB then -1MiB must land
  // exactly 1MiB below the first reading.
  assert.strictEqual(Number(r.before) - Number(r.after), 1 << 20);
});

check("getAllKeys (own vs prototype keys)", () => {
  const proto = { inherited: 1 };
  const o = Object.create(proto, { ownProp: { value: 2, enumerable: true } });
  const r = addon.getAllKeys(o);
  assert.ok([...r.all].includes("inherited"));
  assert.ok(![...r.own].includes("inherited"));
  assert.ok([...r.own].includes("ownProp"));
});

check("strictEq", () => {
  assert.strictEqual(addon.strictEq(1, 1), true);
  assert.strictEqual(addon.strictEq(1, "1"), false);
  assert.strictEqual(addon.strictEq("a", "a"), true);
});

check("deleteProp", () => {
  const o = { gone: 1, stays: 2 };
  assert.strictEqual(addon.deleteProp(o), true);
  assert.ok(!("gone" in o));
  assert.ok("stays" in o);
});

check("removableHook", () => {
  assert.strictEqual(addon.removableHook(), true);
});

check("weak references", () => {
  for (let i = 0; i < 1000; i++) addon.keepWeak({ i });
  global.gc?.();
  return; // final check below after GC settles
});
setTimeout(() => {
  const alive = addon.weakAlive();
  console.log(`  (weak refs alive after gc: ${alive}/1000)`);
  addon.resetWeak();
}, 50);

check("goFuncDemo (js.FuncOf: Go function handed to JS)", () => {
  const f = addon.goFuncDemo();
  assert.strictEqual(typeof f, "function");
  assert.strictEqual(f("bob"), "hello bob (from Go)");
  assert.strictEqual(f("世界"), "hello 世界 (from Go)");
});

check("storeCallback/invokeStored (js.MakeRef persistence)", () => {
  let got = null;
  addon.storeCallback((x) => { got = x; return x + "!"; });
  const r = addon.invokeStored();
  assert.strictEqual(got, "from stored ref");
  assert.strictEqual(r, "from stored ref!");
  addon.releaseStored();
  assert.throws(() => addon.invokeStored(), /no stored callback/);
});

// ---- full-surface coverage ---------------------------------------------------

check("typeOfDemo (napi_typeof)", () => {
  const cases = [
    [undefined, "undefined"], [null, "null"], [true, "boolean"], [1, "number"],
    ["s", "string"], [Symbol("x"), "symbol"], [{}, "object"],
    [() => {}, "function"], [1n, "bigint"],
  ];
  for (const [value, want] of cases) {
    assert.strictEqual(addon.typeOfDemo(value).type, want);
  }
});

check("coerceMoreDemo (ToNumber / ToObject)", () => {
  const r = addon.coerceMoreDemo("42");
  assert.strictEqual(r.number, 42);
  assert.strictEqual(r.type, "object"); // the String wrapper
  assert.strictEqual(r.boxed.valueOf(), "42");
});

check("predicatesDemo (napi_is_*)", () => {
  const cases = [
    [[], { array: true, arrayBuffer: false }],
    [new Date(), { date: true, array: false }],
    [Promise.resolve(), { promise: true }],
    [Buffer.from([1]), { buffer: true, typedArray: true }],
    [new ArrayBuffer(4), { arrayBuffer: true, typedArray: false }],
    [new Uint8Array(4), { typedArray: true, dataView: false }],
    [new DataView(new ArrayBuffer(4)), { dataView: true, typedArray: false }],
  ];
  for (const [value, expect] of cases) {
    const r = addon.predicatesDemo(value);
    for (const [k, v] of Object.entries(expect)) {
      assert.strictEqual(r[k], v, `${Object.prototype.toString.call(value)} .${k}`);
    }
  }
});

check("propOpsDemo (Value-keyed properties, elements, prototype)", () => {
  const o = { existing: 1 };
  const r = addon.propOpsDemo(o, "injected");
  assert.strictEqual(r.value, "set-by-go");
  assert.strictEqual(o.injected, "set-by-go"); // the mutation is visible in JS
  assert.strictEqual(r.hasProperty, true);
  assert.strictEqual(r.hasOwnProperty, true);
  assert.strictEqual(r.hasInherited, true); // toString from Object.prototype
  assert.strictEqual(r.hasInheritedOwn, false);
  assert.strictEqual(r.hasNamed, true);
  assert.strictEqual(r.element, 7);
  assert.strictEqual(r.hasElementBefore, true);
  assert.strictEqual(r.deleted, true);
  assert.strictEqual(r.hasElementAfter, false);
  assert.strictEqual(r.protoIsObject, true);
  assert.ok(!("0" in o));
});

check("valueReadsDemo (int32/uint32/latin1/bigint)", () => {
  const r = addon.valueReadsDemo(1234567, "café", -9007199254740993n);
  assert.strictEqual(r.int32, 1234567);
  assert.strictEqual(r.uint32, 1234567);
  assert.strictEqual(r.latin1Back, "café");
  assert.strictEqual(r.createdBigInt, -9007199254740993n);
  assert.strictEqual(r.signed, Number(-9007199254740993n)); // lossy as a number
  assert.strictEqual(r.unsigned, BigInt.asUintN(64, -9007199254740993n));
  assert.strictEqual(r.lossless, true);
});

check("handleScopeDemo (open/close handle scope)", () => {
  assert.strictEqual(addon.handleScopeDemo(), 4242);
});

check("callbackScopeDemo (open + makeCallback + close)", () => {
  assert.strictEqual(addon.callbackScopeDemo(() => "scoped-call"), "scoped-call");
});

check("refCountDemo (weak -> strong -> weak)", () => {
  const r = addon.refCountDemo({ tag: "ref" });
  assert.strictEqual(r.strong, 1);
  assert.strictEqual(r.weak, 0);
  assert.strictEqual(r.valueAlive, true);
});

check("wrapRemoveDemo (Unwrap then RemoveWrap)", () => {
  const r = addon.wrapRemoveDemo();
  assert.strictEqual(r.before, "wrapped-value");
  assert.strictEqual(r.removed, "wrapped-value");
  assert.strictEqual(r.detached, true);
});

check("sealDemo (Object.seal)", () => {
  const o = addon.sealDemo();
  assert.strictEqual(o.v, 7);
  assert.strictEqual(Object.isSealed(o), true);
});

check("tagDemo (128-bit object type tag)", () => {
  assert.strictEqual(addon.tagDemo({}).argTagged, false);
  const fresh = addon.tagDemo({});
  assert.strictEqual(fresh.freshTagged, true);
  // Counter instances are branded in their constructor
  assert.strictEqual(addon.tagDemo(new addon.CounterClass(0)).argTagged, true);
});

check("makeCounter (Go-defined class factory)", () => {
  const cls = addon.makeCounter();
  assert.strictEqual(typeof cls, "function");
  assert.strictEqual(new cls(5).inc(), 6);
});

check("externalBufferDemo (zero-copy node:Buffer)", () => {
  const r = addon.externalBufferDemo();
  assert.strictEqual(r.supported, true, `external buffers rejected: ${r.code}`);
  assert.strictEqual(r.buffer.length, r.length);
  assert.strictEqual(r.buffer.toString("utf8"), "zero-copy node:Buffer backed by Go memory");
});

check("errorCtorDemo (TypeError / RangeError with code)", () => {
  const t = addon.errorCtorDemo("type");
  assert.ok(t instanceof TypeError);
  assert.strictEqual(t.code, "ERR_CTOR");
  const r = addon.errorCtorDemo("range");
  assert.ok(r instanceof RangeError);
  assert.strictEqual(r.message, "created in Go: range");
});

check("throwPlainDemo (napi_throw of a non-Error value)", () => {
  let caught;
  try { addon.throwPlainDemo(); } catch (e) { caught = e; }
  assert.strictEqual(caught, "thrown-plain-value");
});

check("extendedErrorInfoDemo (last error info)", () => {
  const r = addon.extendedErrorInfoDemo();
  assert.strictEqual(r.code, "napi_number_expected");
  assert.strictEqual(r.status, "napi_number_expected");
  assert.ok(r.message.length > 0);
});

check("uvLoopDemo (GetUVEventLoop)", () => {
  assert.strictEqual(addon.uvLoopDemo().nonNil, true);
});

check("dateReadDemo (GetDateValue)", () => {
  assert.strictEqual(addon.dateReadDemo(new Date(1234567890)), 1234567890);
});

check("nullGlobalDemo (GetNull / GetGlobal)", () => {
  const r = addon.nullGlobalDemo();
  assert.strictEqual(r.null, null);
  assert.strictEqual(r.global, globalThis);
  assert.strictEqual(globalThis.__goGlobalMarker, "from-go");
});

check("asyncCleanupHookDemo (add + remove)", () => {
  const r = addon.asyncCleanupHookDemo();
  assert.strictEqual(r.registered, true);
  assert.strictEqual(r.removed, true);
});

check("arrayBufferInfoDemo (GetArrayBufferInfo)", () => {
  const ab = new Uint8Array([1, 2, 3, 4]).buffer;
  const r = addon.arrayBufferInfoDemo(ab);
  assert.strictEqual(r.byteLength, 4);
  assert.strictEqual(r.sum, 10);
});

check("dataViewInfoDemo (GetDataViewInfo)", () => {
  const buf = new ArrayBuffer(8);
  new Uint8Array(buf).set([1, 2, 3, 4, 5, 6, 7, 8]);
  const r = addon.dataViewInfoDemo(new DataView(buf, 2, 4));
  assert.strictEqual(r.byteOffset, 2);
  assert.strictEqual(r.byteLength, 4);
  assert.strictEqual(r.sum, 3 + 4 + 5 + 6);
  assert.strictEqual(r.hasArrayBuffer, true);
});

// ---- process lifecycle --------------------------------------------------------
// Each of these runs in its own process (see runIsolated).

check("worker-only load (the addon outlives the only environment that loaded it)", () => {
  // Node.js closes an addon's library handle when the environment that loaded
  // it goes away. If no other environment holds the addon, that is the last
  // reference: without a pin the library is unmapped underneath the Go runtime,
  // whose threads are still running.
  const script = `
    const { Worker } = require("node:worker_threads");
    const [original, workerAddon, mainLoadsOriginal] = process.argv.slice(1);
    if (mainLoadsOriginal) require(original);
    const load = () => new Promise((resolve, reject) => {
      const w = new Worker(
        "const { workerData, parentPort } = require('node:worker_threads');" +
        "parentPort.postMessage(require(workerData).add(1, 2));",
        { eval: true, workerData: workerAddon });
      let sum;
      w.on("message", (m) => { sum = m; });
      w.on("error", reject);
      w.on("exit", () => resolve(sum));
    });
    (async () => {
      const first = await load();
      await new Promise((r) => setTimeout(r, 300)); // Go's own threads keep running
      const second = await load();                  // and the addon loads again
      console.log("SURVIVED", first, second);
    })();
  `;
  const survives = (...args) => {
    const r = runIsolated(script, ...args);
    assert.strictEqual(r.status, 0, r.describe());
    assert.match(r.stdout, /SURVIVED 3 3/, r.describe());
  };

  // The addon is required by a Worker and by nothing else.
  survives(ADDON_PATH);

  // The main thread holds the addon; the Worker loads a private copy of the
  // file. That copy is a second library with a second Go runtime, held only by
  // the Worker — the layout that used to be documented as "always crashes".
  // Asserted on Windows, where it was measured; two Go runtimes in one process
  // have not been verified elsewhere.
  if (process.platform === "win32") {
    const dir = mkdtempSync(join(tmpdir(), "napi-go-copy-"));
    try {
      const copy = join(dir, "copy.node");
      copyFileSync(ADDON_PATH, copy);
      survives(copy, "main-loads-original");
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

check("DefineProperties (methods and accessors outlive the object they were defined on)", () => {
  // A method read off an object, or an accessor taken from its property
  // descriptor, is a function in its own right: JavaScript may keep it long
  // after the object is gone. Its Go callback has to live exactly as long.
  const r = runIsolated(`
    const addon = require(process.argv[1]);
    const tick = () => new Promise((r) => setImmediate(r));
    (async () => {
      let map = addon.wrapMap();
      map.set("lang", "Go");
      const get = map.get;
      const size = Object.getOwnPropertyDescriptor(map, "size").get;
      console.log("NAMES", JSON.stringify([get.name, size.name]));
      const host = new WeakRef(map);
      map = null;
      // No deref() inside the loop: reading a WeakRef keeps its target alive
      // until the current job ends, which would defeat the collection.
      for (let i = 0; i < 8; i++) { gc(); await tick(); }
      console.log("HOST_COLLECTED", host.deref() === undefined);
      // Hand the allocator plenty of new callbacks: storage freed along with
      // the host would now belong to one of these.
      const others = [];
      for (let i = 0; i < 256; i++) others.push(addon.goFuncDemo());
      console.log("GET", get("lang"));
      console.log("SIZE", size.call({}));
    })();
  `);
  assert.strictEqual(r.status, 0, r.describe());
  assert.match(r.stdout, /HOST_COLLECTED true/, r.describe());
  assert.match(r.stdout, /GET Go\r?\n/, r.describe());
  assert.match(r.stdout, /SIZE 1\r?\n/, r.describe());
});

check("threadsafe function (a goroutine keeps calling after its environment is gone)", () => {
  // A Worker starts a goroutine that loops over a threadsafe function, and is
  // then terminated. The engine tears the function down with the environment;
  // the goroutine knows nothing about that and calls again. It has to be told
  // (napi_closing), and its release and any later call have to be refused
  // rather than reach the engine's freed object.
  const r = runIsolated(`
    const { Worker } = require("node:worker_threads");
    const addon = require(process.argv[1]);
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    const w = new Worker(
      "const { workerData, parentPort } = require('node:worker_threads');" +
      "require(workerData).tsfnTicker(() => {});" +
      "parentPort.postMessage('started');" +
      "setInterval(() => {}, 1000);",
      { eval: true, workerData: process.argv[1] });
    w.on("message", async () => {
      await sleep(100);
      await w.terminate();
      for (let i = 0; i < 100 && !addon.tsfnTickerReport().done; i++) await sleep(20);
      console.log("REPORT", JSON.stringify(addon.tsfnTickerReport()));
    });
  `);
  assert.strictEqual(r.status, 0, r.describe());
  const report = JSON.parse(/REPORT (.*)/.exec(r.stdout)?.[1] ?? "null");
  assert.deepStrictEqual(report, {
    done: true,
    dispatchedSome: true,
    callErr: "napi_closing",
    releaseErr: "napi_closing",
    lateErr: "napi_closing",
  }, r.describe());
});

check("threadsafe function (abort wakes every blocked caller)", () => {
  // Four goroutines wait behind a full queue of one; the function is aborted.
  // Each of them has to come back with napi_closing.
  const r = runIsolated(`
    const addon = require(process.argv[1]);
    console.log("RESULT", JSON.stringify(addon.tsfnAbortBlocked(() => {}, 4)));
  `);
  assert.strictEqual(r.status, 0, r.describe());
  const result = JSON.parse(/RESULT (.*)/.exec(r.stdout)?.[1] ?? "null");
  assert.deepStrictEqual(result, {
    first: "ok",
    second: "napi_queue_full",
    abort: "ok",
    blocked: ["napi_closing", "napi_closing", "napi_closing", "napi_closing"],
  }, r.describe());
});

check("host executable with another name (nothing is tied to \"node.exe\")", () => {
  // The addon is loaded by a copy of this Node.js binary under a different
  // file name — what Electron, a single-executable application or a renamed
  // node look like to it. A Windows addon that imports its Node-API functions
  // from "node.exe" cannot load there, or worse, loads a second Node.js found
  // on PATH and calls into that.
  const dir = mkdtempSync(join(tmpdir(), "napi-go-host-"));
  try {
    const host = join(dir, process.platform === "win32" ? "renamed-host.exe" : "renamed-host");
    copyFileSync(process.execPath, host);
    chmodSync(host, 0o755);
    const r = runIsolatedIn(host, `
      const addon = require(process.argv[1]);
      console.log("LOADED", addon.add(1, 2), addon.hello("host"));
    `);
    assert.strictEqual(r.status, 0, r.describe());
    assert.match(r.stdout, /LOADED 3 hello, host/, r.describe());
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

// Let every promise settle, then report.
Promise.allSettled(pending).then(() => {
  if (process.exitCode) {
    console.log(`\n${passed} passed with failures`);
  } else {
    console.log(`\nALL ${passed} CHECKS PASSED`);
  }
});
