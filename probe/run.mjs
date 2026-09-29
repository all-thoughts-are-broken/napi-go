// probe/run.mjs — hostile driver for the napi-go probe addon.
//
// Every probe is run in isolation (try/catch) and reported. Probes that are
// expected to tear the process down live in probe/crash.mjs instead.
import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";
import path from "node:path";
const require = createRequire(import.meta.url);
const p = require("./probe.node");
const HERE = path.dirname(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1"));

let bugs = [];
let notes = [];

// Run one probe in a fresh process so that a hard crash (segfault/abort) is
// observable instead of killing the whole sweep. afterName runs once the child
// has forced its collections, for probes that read counters the finalizers
// advanced.
function spawnProbe(name, args = [], afterName = null, timeout = 60000) {
  const argv = ["--expose-gc", "one.mjs", name, JSON.stringify(args)];
  if (afterName) argv.push("gc", afterName);
  const r = spawnSync(process.execPath, argv, {
    cwd: HERE, encoding: "utf8", timeout,
  });
  const out = r.stdout || "";
  const errText = r.stderr || "";
  const lines = out.split(/\r?\n/);
  const resLine = lines.find((l) => l.startsWith("PROBE_RESULT "));
  const res2Line = lines.find((l) => l.startsWith("PROBE_RESULT2 "));
  const statsLine = lines.find((l) => l.startsWith("PROBE_STATS "));
  const threwLine = lines.find((l) => l.startsWith("PROBE_THREW "));
  const finished = lines.some((l) => l.startsWith("PROBE_END "));
  let value, after, stats;
  if (resLine) { try { value = JSON.parse(resLine.slice(13)); } catch { value = resLine.slice(13); } }
  if (res2Line) { try { after = JSON.parse(res2Line.slice(14)); } catch { after = res2Line.slice(14); } }
  if (statsLine) { try { stats = JSON.parse(statsLine.slice(12)); } catch { /* ignore */ } }
  return {
    status: finished ? (resLine ? "result" : threwLine ? "threw" : "noop") : "crash",
    value,
    after,
    stats,
    threw: threwLine ? threwLine.slice(12) : null,
    exit: r.status,
    signal: r.signal,
    timedOut: !!(r.error && r.error.code === "ETIMEDOUT"),
    stderr: errText.trim(),
  };
}

function rssMB() {
  if (global.gc) { global.gc(); global.gc(); }
  return process.memoryUsage().rss / 1048576;
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function log(tag, name, detail) {
  const line = `${tag.padEnd(7)} ${name.padEnd(34)} ${detail ?? ""}`;
  console.log(line);
  if (tag === "[BUG]") bugs.push(`${name}: ${detail ?? ""}`);
  if (tag === "[note]") notes.push(`${name}: ${detail ?? ""}`);
}

async function group(title) {
  console.log(`\n=== ${title} ===`);
}

async function run(name, fn) {
  try {
    const v = await fn();
    return v;
  } catch (e) {
    log("[throw]", name, `${e.constructor.name}: ${String(e.message).slice(0, 120)}`);
    return undefined;
  }
}

// ---------------------------------------------------------------- helpers

function expectEq(name, actual, expected, extra = "") {
  if (actual === expected) log("[ok]", name, `${JSON.stringify(actual)} ${extra}`);
  else log("[BUG]", name, `expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)} ${extra}`);
}

function report(name, cond, detail) {
  if (cond) log("[ok]", name, detail);
  else log("[BUG]", name, detail);
}

// ================================================================== 1. stats

await group("1. baseline stats");
const s0 = await run("stats", () => p.stats());
log("[info]", "stats", JSON.stringify(s0));
// A pristine process's box ledger right after module load: anything above
// this after a forced GC is a permanent leak.
const bench = spawnProbe("stats");
const BASE_OUTSTANDING = Number(bench.stats?.boxOutstanding ?? 0);
log("[info]", "box baseline (fresh process)", String(BASE_OUTSTANDING));

// ================================================= 2. panic path memory leak

await group("2. panic -> JS exception path (H1: C.CString leak?)");
{
  const MSG = 1000;
  const N = 20000;
  const LEAK_PER_ROUND = N * MSG / 1048576;   // ~19MB: the linear leak signature

  // RSS is only a valid leak signal once the heap has reached its high-water
  // mark. The first rounds after a fresh workload arm Go's arenas / span caches
  // and grow V8's heap — measured +11/+4/+4/+3MB — and then flatten to ~0.3MB.
  // Sampling inside that ramp is what made this probe cry wolf, so the gate now
  // discards WARM rounds and judges the slope only after it has settled.
  const WARM = 4;
  const GATE = 3;                             // MB; well below the ~19MB leak signal

  // Baseline: JS throws the same size message N times.
  const jsThrow = (n) => {
    for (let i = 0; i < n; i++) {
      try { throw new Error("J".repeat(MSG)); } catch { /* ignore */ }
    }
  };
  // Measure retained memory, not garbage: collect both heaps and hand Go's
  // high-water mark back to the OS before each RSS reading. 20k panics with
  // 1KB messages otherwise leave ~10MB of transient Go garbage, which looks
  // exactly like the C-string leak this probe hunts for.
  const settle = async () => {
    if (global.gc) { for (let i = 0; i < 3; i++) global.gc(); }
    p.freeGo();
    await sleep(60);
  };
  // Runs WARM + MEASURE rounds of `work`, returning the per-round RSS deltas.
  const series = async (work) => {
    await settle();
    let prev = rssMB();
    const deltas = [];
    for (let r = 0; r < WARM + 3; r++) {
      work();
      await settle();
      const cur = rssMB();
      deltas.push(cur - prev);
      prev = cur;
    }
    return deltas;
  };

  for (let i = 0; i < 2000; i++) { try { p.panicSized(MSG); } catch {} }
  jsThrow(2000);                                   // warm up both paths

  const goDeltas = await series(() => { for (let i = 0; i < N; i++) { try { p.panicSized(MSG); } catch {} } });
  const jsDeltas = await series(() => jsThrow(N));

  const goRamp = goDeltas.slice(0, WARM);
  const goSteady = goDeltas.slice(WARM);
  const jsSteady = jsDeltas.slice(WARM);
  const goMax = Math.max(...goSteady);
  const jsMax = Math.max(...jsSteady);
  const fmt = (xs) => xs.map((d) => d.toFixed(1)).join("/");

  log("[info]", "rss", `goPanic +${fmt(goDeltas)}MB jsThrow +${fmt(jsDeltas)}MB`);
  // A C-string leak is linear in the number of panics: every round costs its
  // full size and never settles. A bounded ramp that flattens to ~0 is the heap
  // reaching steady state, not a leak.
  report("panic path steady state", goMax < GATE && goMax < jsMax + GATE,
    `steady +${fmt(goSteady)}MB vs jsThrow steady +${fmt(jsSteady)}MB (a leak costs ~${LEAK_PER_ROUND.toFixed(0)}MB/round); ramp +${fmt(goRamp)}MB is the heap warming up`);
}

// ================================================ 3. Wrap / RemoveWrap boxes

await group("3. Wrap / RemoveWrap box accounting (H2: hint box leak?)");
for (const withFin of [false, true]) {
  const r = await run(`wrapRemove(5000, fin=${withFin})`, () => p.wrapRemove(5000, withFin));
  if (!r) continue;
  // One box per wrapped object, with or without a finalizer: the value and
  // the finalizer now share a single box, so RemoveWrap can release both.
  const expectAlloc = 5000;
  expectEq(`wrapRemove fin=${withFin} alloc`, r.allocDelta, expectAlloc);
  expectEq(`wrapRemove fin=${withFin} free`, r.freeDelta, expectAlloc,
    r.freeDelta !== expectAlloc ? `→ outstanding=${r.outstanding} LEAK` : "");
  expectEq(`wrapRemove fin=${withFin} fails`, r.fails, 0);
}

await group("3b. double Wrap");
const wt = await run("wrapTwice", () => p.wrapTwice());
log("[info]", "wrapTwice", JSON.stringify(wt));

await group("3c. double RemoveWrap");
const rwt = await run("removeWrapTwice", () => p.removeWrapTwice());
log("[info]", "removeWrapTwice", JSON.stringify(rwt));

await group("3d. Wrap(with finalize) + RemoveWrap → leaked hint?");
{
  const before = await run("stats", () => p.stats());
  const r = await run("wrapFinRemove(3000)", () => p.wrapFinRemove(3000));
  log("[info]", "wrapFinRemove", JSON.stringify(r));
  if (r) {
    // One box per object: RemoveWrap drops the wrap, and the value and the
    // (never-to-run) finalizer sit in that same box, so nothing is stranded.
    expectEq("Wrap(onFinalize)+RemoveWrap alloc", Number(r.allocDelta), 3000);
    expectEq("Wrap(onFinalize)+RemoveWrap free", Number(r.freeDelta), 3000,
      Number(r.freeDelta) !== 3000 ? `→ outstanding=${r.outstanding}` : "");
  }
  const after = await run("stats", () => p.stats());
  log("[info]", "outstanding", `${before.boxOutstanding} → ${after.boxOutstanding}`);
}

// ================================================= 4. descriptor edge cases

await group("4. DefineProperties / DefineClass with malformed input");
const defineLabels = [
  "no name, has value",
  "value + method both set",
  "name + nameValue both set",
  "empty property list",
  "duplicate names",
  "frozen target",
  "non-object target (number)",
  "getter only",
  "setter only",
  "DefineClass, no properties",
  "DefineClass, duplicate names",
  "DefineClass, static accessor",
  "DefineClass, descriptor with nothing",
  "DefineProperties on undefined",
  "name but no value/method/accessor",
  "method + getter on one descriptor",
  "symbol-keyed property",
  "DefineClass with nil constructor",
];
// Classes are the one object kind V8 never collects: napi_define_class goes
// through a FunctionTemplate, and V8 caches instantiations of cacheable
// templates in the NativeContext forever (TemplateInfo::CacheTemplate
// Instantiation → a FixedArray slot keyed by the template's serial number).
// The box therefore outlives every GC by design of the engine, not of the
// binding — proven by probe/focus3.mjs, where a bare napi_define_class result
// with no binding bookkeeping attached is equally immortal.
const ENGINE_PINNED_CLASS = new Set([9, 10, 11]);
for (let k = 0; k <= 17; k++) {
  const r = spawnProbe("defineBad", [k]);
  const label = `defineBad(${k}) ${defineLabels[k]}`;
  if (r.status === "crash") {
    log("[BUG]", label, `PROCESS CRASH (exit=${r.exit} signal=${r.signal})`);
  } else if (r.status === "threw") {
    log("[info]", label, `threw ${r.threw}`);
  } else {
    const v = r.value || {};
    const post = r.stats ? Number(r.stats.boxOutstanding) : NaN;
    const grew = post > BASE_OUTSTANDING;
    const pinned = ENGINE_PINNED_CLASS.has(k);
    const tag = grew ? (pinned ? "[note]" : "[BUG]") : "[info]";
    const tail = !grew ? "" : pinned
      ? ` +${post - BASE_OUTSTANDING} boxes held for the context's lifetime (engine pins the class)`
      : ` LEAK +${post - BASE_OUTSTANDING}`;
    log(tag, label, `err=${v.err} allocΔ=${v.allocDelta} freeΔ=${v.freeDelta} outstanding(after GC)=${post}${tail}`);
    // Gates for what used to be hard crashes / silent leaks.
    if (k === 0 || k === 12 || k === 17) {
      expectEq(`defineBad(${k}) rejected`, v.err, "napi_invalid_arg");
    }
    if (!pinned) expectEq(`defineBad(${k}) no residue`, post, BASE_OUTSTANDING);
  }
}

// ================================================= 5. handle/callback scopes

await group("5. scope misuse");
const et = await run("escapeTwice", () => p.escapeTwice());
log("[info]", "escapeTwice", JSON.stringify(et));
if (et) expectEq("escape second call", et.escape2, "napi_escape_called_twice");

const SCOPE_LABELS = [
  "handle scope closed twice",
  "handle scopes closed out of order",
  "make_callback + callback scope closed twice",
  "async context destroyed twice",
  "async context destroy(NULL)",
  "async context destroy(foreign pointer)",
  "non-innermost handle scope closed twice",
  "escapable scope closed as a plain scope",
];
for (let k = 0; k <= 7; k++) {
  const r = spawnProbe("scopeMisuse", [k]);
  if (r.status === "crash") {
    log("[BUG]", `scopeMisuse(${k}) ${SCOPE_LABELS[k]}`, `PROCESS CRASH exit=${r.exit} (0x${(r.exit >>> 0).toString(16)})`);
    continue;
  }
  const v = r.value || {};
  log("[info]", `scopeMisuse(${k}) ${SCOPE_LABELS[k]}`, JSON.stringify(v));
  if (k === 2) {
    // Regression gate: the make_callback happy path and orderly scope
    // handling, not just the engine's rejection of a NULL receiver.
    expectEq("makeCallback succeeds", v.call, "ok");
    expectEq("callback scope closed twice", v.close2, "napi_callback_scope_mismatch");
  }
  if (k === 3) {
    // napi_async_destroy is a bare `delete`; the second call used to corrupt
    // the heap (0xC0000374) instead of reporting an error.
    expectEq("async context destroyed twice", v.destroy2, "napi_invalid_arg");
  }
  if (k === 0) {
    expectEq("handle scope closed twice", v.close2, "napi_invalid_arg");
  }
  if (k === 5) {
    // A pointer this package never issued must not reach the engine.
    expectEq("foreign async context rejected", v.destroyBogus, "napi_invalid_arg");
  }
  if (k === 6) {
    // The engine's open_handle_scopes counter still had room here, so the
    // same scope used to be deleted twice (0xC0000374).
    expectEq("non-innermost scope closed twice", v.close2, "napi_invalid_arg");
    expectEq("scope accounting balanced", v.balance, "ok");
  }
  if (k === 7) {
    // A plain close on an escapable scope would reinterpret the engine object.
    expectEq("mismatched scope kind rejected", v.wrongClose, "napi_invalid_arg");
    expectEq("correct kind still closes", v.rightClose, "ok");
  }
}

// ================================================= 6. exception semantics

await group("6. exception semantics");
const cne = await run("clearNoExc", () => p.clearNoExc());
log("[info]", "clearNoExc", JSON.stringify(cne));

for (let k = 0; k <= 4; k++) {
  const r = await run(`throwRoundtrip(${k})`, () => p.throwRoundtrip(k));
  log("[info]", `throwRoundtrip(${k})`, JSON.stringify(r));
  if (r) {
    report(`throwRoundtrip(${k}) pending`, r.pending === true, `pending=${r.pending}`);
    report(`throwRoundtrip(${k}) cleared`, r.stillPending === false, `stillPending=${r.stillPending}`);
  }
}

await group("6b. coercion of hostile values");
for (const [mode, v] of [[0, null], [0, undefined], [1, Symbol("s")], [2, Symbol("s")], [1, 10n], [0, 10n]]) {
  const r = await run(`coerce(${mode}, ${String(v)})`, () => p.coerceProbe(mode, v));
  log("[info]", `coerce(mode=${mode}, ${typeof v === "symbol" ? "symbol" : String(v).slice(0, 12)})`, JSON.stringify(r));
}

// ================================================= 7. view offsets

await group("7. typed array / dataview with byteOffset");
{
  const r = await run("taMake(16,4,4)", () => p.taMake(16, 4, 4));
  log("[info]", "taMake(16,4,4)", JSON.stringify({ abErr: r?.abErr, taErr: r?.taErr, hasTA: !!r?.ta }));
  if (r?.ta) {
    const info = await run("taInfo", () => p.taInfo(r.ta));
    log("[info]", "taInfo(offset view)", JSON.stringify(info));
    const jsOffset = r.ta.byteOffset, jsLen = r.ta.length;
    const jsFirst = r.ta[0], jsLast = r.ta[r.ta.length - 1];
    expectEq("taInfo byteOffset", Number(info.byteOffset), jsOffset);
    expectEq("taInfo length", Number(info.length), jsLen);
    expectEq("taInfo dataLen", Number(info.dataLen), jsLen);
    expectEq("taInfo firstByte", Number(info.firstByte), jsFirst);
    expectEq("taInfo lastByte", Number(info.lastByte), jsLast);
  }
}
{
  const r = await run("dvMake(16,4,6)", () => p.dvMake(16, 4, 6));
  log("[info]", "dvMake(16,4,6)", JSON.stringify({ abErr: r?.abErr, dvErr: r?.dvErr, hasDV: !!r?.dv }));
  if (r?.dv) {
    const info = await run("dvInfo", () => p.dvInfo(r.dv));
    log("[info]", "dvInfo(offset view)", JSON.stringify(info));
    expectEq("dvInfo byteLength", Number(info.byteLength), r.dv.byteLength);
    expectEq("dvInfo byteOffset", Number(info.byteOffset), r.dv.byteOffset);
    expectEq("dvInfo dataLen", Number(info.dataLen), r.dv.byteLength);
  }
}
{
  const r = await run("taMake(8,8,4)", () => p.taMake(8, 8, 4)); // out of bounds
  log("[info]", "taMake out-of-bounds", JSON.stringify({ abErr: r?.abErr, taErr: r?.taErr }));
  const r2 = await run("dvMake(8,4,8)", () => p.dvMake(8, 4, 8));
  log("[info]", "dvMake out-of-bounds", JSON.stringify({ abErr: r2?.abErr, dvErr: r2?.dvErr }));
  const r3 = await run("taMake(0,0,0)", () => p.taMake(0, 0, 0));
  log("[info]", "taMake zero-length", JSON.stringify({ abErr: r3?.abErr, taErr: r3?.taErr }));
}

// ================================================= 8. detach

await group("8. ArrayBuffer detach");
{
  const r = spawnProbe("abLifecycle");
  if (r.status === "crash") {
    log("[BUG]", "abLifecycle", `PROCESS CRASH exit=${r.exit}`);
  } else {
    const v = r.value || {};
    if (v.wroteDetachedStorage === true) {
      log("[BUG]", "abLifecycle write-after-detach", `engine returned a non-empty view of detached storage (len=${v.lenAfter}) — writing there is a use-after-free`);
    } else {
      log("[ok]", "abLifecycle write-after-detach", "detached buffer reports no storage");
    }
    log("[info]", "abLifecycle", JSON.stringify(v));
  }
}

// ================================================= 9. strings

await group("9. string encoding edges");
for (const s of ["", "a\0b", "\uD800", "\uDC00", "\uD83D\uDE00", "中\uD800文"]) {
  const r = await run(`utf16(${JSON.stringify(s)})`, () => p.utf16Probe(s));
  if (!r) continue;
  const units = r.units ? Array.from(r.units) : null;
  log("[info]", `utf16 ${JSON.stringify(s)}`, `n=${r.n} units=${JSON.stringify(units)} back=${JSON.stringify(r.back)} equal=${r.equal}`);
  report(`utf16 roundtrip ${JSON.stringify(s)}`, r.equal === true, `equal=${r.equal}`);
}
for (const mode of [0, 1, 2, 3]) {
  const r = await run(`utf8Edge(${mode})`, () => p.utf8Edge(mode));
  if (r) log("[info]", `utf8Edge(${mode})`, `in=${r.inLen} out=${r.outLen} back=${JSON.stringify(String(r.back).slice(0, 20))}`);
}
for (const s of ["", "abc", "\u00ff\u0041", "\u4e2d"]) {
  const r = await run(`latin1(${JSON.stringify(s)})`, () => p.latin1Probe(s));
  if (!r) continue;
  log("[info]", `latin1 ${JSON.stringify(s)}`, `backLen=${r.backLen} back=${JSON.stringify(r.back)} utf8=${JSON.stringify(r.utf8)}`);
}

// ================================================= 10. BigInt

await group("10. BigInt words");
{
  const cases = [
    ["0n", 0n],
    ["1n", 1n],
    ["-1n", -1n],
    ["2^64", 1n << 64n],
    ["2^64-1", (1n << 64n) - 1n],
    ["-(2^64)", -(1n << 64n)],
    ["2^100", 1n << 100n],
    ["-(2^100)+1", -(1n << 100n) + 1n],
  ];
  for (const [label, v] of cases) {
    const r = await run(`bigintWords(${label})`, () => p.bigintWords(v));
    if (!r) continue;
    const words = r.words ? Array.from(r.words).map(String) : null;
    const rebuilt = words ? words.reduceRight((acc, w) => (acc << 64n) + BigInt(w), 0n) : null;
    // Doc says "sign_bit is negative for negative values"; the engine actually
    // reports 1 for negative (0 for positive), same as napi_create_bigint_words.
    const sign = Number(r.sign) === 1 ? -1n : 1n;
    const value = rebuilt === null ? null : sign * rebuilt;
    log("[info]", `bigintWords(${label})`, `err=${r.err} sign=${r.sign} words=${JSON.stringify(words)} i64=${r.i64} losslessI64=${r.losslessI64} u64=${r.u64} losslessU64=${r.losslessU64}`);
    if (r.err === "ok") {
      report(`bigintWords(${label}) value`, value === v, `rebuilt ${value} vs ${v}`);
    }
    if (label === "1n") expectEq("bigint 1n losslessI64", r.losslessI64, true);
    if (label === "2^64") expectEq("bigint 2^64 losslessI64", r.losslessI64, false);
  }
}
for (const [sign, spec] of [[0, ""], [0, "0"], [0, "1"], [1, "1"], [1, "18446744073709551615"], [0, "18446744073709551615,1"]]) {
  const r = await run(`bigintMake(${sign},${spec})`, () => p.bigintMake(sign, spec));
  if (r) log("[info]", `bigintMake(sign=${sign}, words=${spec})`, `err=${r.err} value=${r.value !== undefined ? String(r.value) : "<nil>"}`);
}
for (const [n, u] of [[-1, false], [0, false], [0, true], [2 ** 63 - 1, false], [2 ** 63, true]]) {
  const r = await run("bigintCreate", () => p.bigintCreate(n, u));
  if (r) log("[info]", `bigintCreate(${n}, uint=${u})`, `err=${r.err} value=${r.value !== undefined ? String(r.value) : "<nil>"}`);
}

// ================================================= 11. callbacks

await group("11. callback info / new.target / invocation");
for (const n of [0, 1, 3, 300]) {
  const args = Array.from({ length: n }, (_, i) => `a${i}`);
  const r = await run(`argProbe(${n})`, () => p.argProbe(...args));
  if (r) expectEq(`argProbe(${n}) argCount`, Number(r.n), n, `this=${r.thisType} dataNil=${r.dataNil}`);
}
{
  const asFn = await run("newTargetProbe()", () => p.newTargetProbe());
  const asCtor = await run("new newTargetProbe()", () => new p.newTargetProbe());
  log("[info]", "newTarget plain", JSON.stringify(asFn));
  log("[info]", "newTarget ctor", JSON.stringify(asCtor));
  if (asFn) expectEq("newTarget plain", asFn.hasNewTarget, false);
  if (asCtor) expectEq("newTarget ctor", asCtor.hasNewTarget, true);
}
{
  const r = await run("callThrow", () => p.callThrow(() => { throw new Error("inner boom"); }));
  log("[info]", "callThrow", JSON.stringify(r));
  if (r) {
    expectEq("callThrow reported pending", r.pending, true);
    expectEq("callThrow engine usable after", r.afterErr, "ok");
  }
}
{
  const r = await run("runScriptThrow", () => p.runScriptThrow("throw new TypeError('script boom')"));
  log("[info]", "runScriptThrow", JSON.stringify(r));
  if (r) expectEq("runScriptThrow pending", r.pending, true);
  const ok = await run("runScriptThrow(ok)", () => p.runScriptThrow("1+1"));
  log("[info]", "runScript ok", JSON.stringify(ok));
}
{
  const r = await run("callFn", () => p.callFn({ tag: "recv" }, function () { return this.tag + ":" + Array.from(arguments).join(","); }, 1, 2));
  log("[info]", "callFn", JSON.stringify(r));
  const bad = await run("callFn(non-function)", () => p.callFn(null, 42));
  log("[info]", "callFn non-function", JSON.stringify(bad));
}
{
  const r = await run("newInstance", () => p.newInstance(function (n) { this.n = n; }, 7));
  log("[info]", "newInstance", JSON.stringify(r));
  const bad = await run("newInstance(arrow)", () => p.newInstance(() => {}, 1));
  log("[info]", "newInstance arrow", JSON.stringify(bad));
  const bad2 = await run("newInstance(string)", () => p.newInstance("x", 1));
  log("[info]", "newInstance string", JSON.stringify(bad2));
}

// ================================================= 12. instance data

await group("12. instance data");
{
  const r = spawnProbe("instanceDataTwice");
  const v = r.value || {};
  log("[info]", "instanceDataTwice", JSON.stringify(v));
  if (Object.keys(v).length) {
    // The engine does accept a second napi_set_instance_data and silently
    // deletes the previous record without running its finalizer, so the
    // binding releases the replaced box itself (see napi/scope_ref.go).
    // Exactly one box — the currently installed payload — may stay
    // outstanding while the environment lives.
    log("[info]", "instance data replacement", `set1=${v.set1} set2=${v.set2} value=${v.value}`);
    expectEq("instance data second call", v.set2, "ok");
    expectEq("instance data holds newest value", v.value, "second");
    const post = Number(r.stats?.boxOutstanding ?? NaN);
    const want = BASE_OUTSTANDING + 1;
    report("instance data replace leak", post <= want,
      `outstanding ${post}, expected ${want} (only the installed payload may stay alive)`);
  }
}

// ================================================= 13. finalizers

await group("13. finalizers");
{
  // Drop every JS reference to the tagged object before collecting: holding
  // the probe's return value would keep it alive and mask the finalizer.
  const brief = await run("finalizerTwice", () => {
    const o = p.finalizerTwice({});
    return { add1: o.add1, add2: o.add2 };
  });
  log("[info]", "finalizerTwice", JSON.stringify(brief));
  await sleep(30);
  for (let i = 0; i < 8; i++) { rssMB(); await sleep(20); }
  let c = await run("probeCounters", () => p.probeCounters());
  log("[info]", "counters after GC", JSON.stringify(c));
  if (c && Number(c.finA) + Number(c.finB) === 0) {
    for (let i = 0; i < 20; i++) { rssMB(); await sleep(10); }
    c = await run("probeCounters", () => p.probeCounters());
    log("[info]", "counters after aggressive GC", JSON.stringify(c));
  }
  if (c) {
    report("both finalizers ran", Number(c.finA) === 1 && Number(c.finB) === 1,
      `finA=${c.finA} finB=${c.finB} (two AddFinalizer calls on one object)`);
  }
}
{
  const before = await run("stats", () => p.stats());
  let objs = [];
  for (let i = 0; i < 3000; i++) objs.push(await run("wrapWithFinalize", () => p.wrapWithFinalize({})));
  objs = null;
  await sleep(50);
  for (let i = 0; i < 5; i++) { rssMB(); await sleep(20); }
  const after = await run("stats", () => p.stats());
  const c = await run("probeCounters", () => p.probeCounters());
  log("[info]", "wrap finalizers", `hits=${c?.wrapFins} outstanding ${before.boxOutstanding} → ${after.boxOutstanding}`);
  report("wrap finalizers released boxes", Number(after.boxOutstanding) <= Number(before.boxOutstanding),
    `outstanding ${before.boxOutstanding} → ${after.boxOutstanding}, finalizer hits=${c?.wrapFins}/${3000}`);
}
{
  // A user finalizer that panics must not cost the box: the release has to
  // happen on the unwind path too (Go's recover() ends the function, so
  // anything written after the panic never runs). Run it in a child — 2000
  // panicking finalizers log 2000 lines to stderr, which would drown the
  // report — and let the child force its own collections so PROBE_STATS
  // reflects the post-finalizer ledger.
  const N = 2000;
  const r = spawnProbe("wrapFinPanic", [N], "wrapFinPanicReport");
  if (r.status === "crash") {
    log("[BUG]", "panicking finalizer", `PROCESS CRASH exit=${r.exit} (0x${(r.exit >>> 0).toString(16)})`);
  } else {
    const post = Number(r.stats?.boxOutstanding ?? NaN);
    const panics = Number(r.after?.panics ?? 0);
    log("[info]", "panicking finalizer", `panics=${panics} allocΔ=${r.value?.allocDelta} outstanding(after GC)=${post} (baseline ${BASE_OUTSTANDING})`);
    report("panicking finalizer released boxes", post <= BASE_OUTSTANDING && panics === N,
      `outstanding(after GC)=${post} vs baseline ${BASE_OUTSTANDING} after ${panics}/${N} panicking finalizers (each leak would pin one box for the process lifetime)`);
  }
}

// ================================================= 14. references

await group("14. references");
{
  const r = await run("refProbe(0)", () => p.refProbe(0));
  log("[info]", "refProbe strong", JSON.stringify(r));
  if (r && r.create === "ok") {
    expectEq("ref count after ref", Number(r.ref1), 2);
    expectEq("unref 1", Number(r.unref1), 1);
    expectEq("unref 2", Number(r.unref2), 0);
    log("[info]", "unref below zero", `${r.unref3} (count=${r.unref3Count})`);
  } else {
    log("[BUG]", "refProbe(0) create", `CreateReference on an object failed: ${r?.create}`);
  }
  // napi_create_reference on a primitive is expected to be rejected; verify
  // through the wrapper so js.Value.MakeRef's behaviour is documented.
  const prim = await run("refProbe(0, number)", () => p.refProbe(0, 42));
  log("[info]", "refProbe(0) on a number", JSON.stringify(prim));
  for (const k of [3, 4]) {
    const rr = spawnProbe("refProbe", [k]);
    const label = `refProbe(${k})${k === 3 ? " double delete" : " use after delete"}`;
    if (rr.status === "crash") {
      log("[BUG]", label, `PROCESS CRASH exit=${rr.exit} (0x${(rr.exit >>> 0).toString(16)})`);
      continue;
    }
    const v = rr.value || {};
    log("[info]", label, JSON.stringify(v));
    expectEq(`${label} guarded`, k === 3 ? v.delete2 : v.useAfterDelete, "napi_invalid_arg");
  }
  await run("refProbe(1) weak", () => p.refProbe(1));
  await run("refProbe(1) weak", () => p.refProbe(1));
  for (let i = 0; i < 6; i++) { rssMB(); await sleep(20); }
  const a = await run("refProbe(2) slot0", () => p.refProbe(2, null, 0));
  const b = await run("refProbe(2) slot1", () => p.refProbe(2, null, 1));
  log("[info]", "weak refs after GC", `slot0=${JSON.stringify(a)} slot1=${JSON.stringify(b)}`);
}

// ================================================= 15. async work

await group("15. async work");
for (let k = 0; k <= 5; k++) {
  const r = spawnProbe("asyncWork", [k]);
  if (r.status === "crash") {
    log("[BUG]", `asyncWork(${k})`, `PROCESS CRASH exit=${r.exit} (0x${(r.exit >>> 0).toString(16)})`);
    continue;
  }
  const v = r.value || {};
  log("[info]", `asyncWork(${k})`, JSON.stringify(v));
  // Gates for the three orderings that used to dereference freed engine
  // memory and take the process down.
  if (k === 2) expectEq("asyncWork(2) delete while queued", v.delete, "napi_invalid_arg");
  if (k === 3) expectEq("asyncWork(3) double delete", v.delete2, "napi_invalid_arg");
  if (k === 5) expectEq("asyncWork(5) queue after delete", v.queueAfterDelete, "napi_invalid_arg");
}
{
  // Canonical lifecycle in a single process: queue N works, each deleting
  // itself from its completion callback, then report the ledger.
  const r = await run("asyncFull(5)", () => p.asyncFull(5));
  log("[info]", "asyncFull", JSON.stringify(r));
  await sleep(300);
  for (let i = 0; i < 5; i++) { rssMB(); await sleep(30); }
  const rep = await run("asyncReport", () => p.asyncReport());
  log("[info]", "asyncReport", JSON.stringify(rep));
  if (rep) {
    expectEq("async exec count", Number(rep.exec), 5);
    expectEq("async done count", Number(rep.done), 5);
    if (Number(rep.allocDelta) !== Number(rep.freeDelta)) {
      log("[BUG]", "async work box leak", `allocΔ=${rep.allocDelta} freeΔ=${rep.freeDelta} deleteErr=${rep.deleteErr}`);
    } else {
      log("[ok]", "async work ledger", `allocΔ=${rep.allocDelta} freeΔ=${rep.freeDelta}`);
    }
  }
}

// ================================================= 16. cleanup hooks

await group("16. cleanup hooks");
{
  const r = await run("removableHook(false)", () => p.removableHook(false));
  log("[info]", "removableHook keep", JSON.stringify(r));
  const r2 = await run("removableHook(true)", () => p.removableHook(true));
  log("[info]", "removableHook remove", JSON.stringify(r2));
  if (r2) expectEq("removable hook boxes freed", Number(r2.freeDelta), Number(r2.allocDelta));

  // Async cleanup hooks. The engine implements napi_remove_async_cleanup_hook
  // as a bare `delete handle`, so the removal paths here used to be able to
  // corrupt the heap outright, and the teardown handshake can hang the process.
  const ASYNC_LABELS = [
    "add + remove once",
    "add + remove + remove again",
    "remove a NULL handle",
    "6x add/remove cycles",
    "fires at teardown, handler closes the handshake",
  ];
  for (const k of [0, 1, 2, 3, 4]) {
    const rr = spawnProbe("asyncHookProbe", [k]);
    const v = rr.value || {};
    const fired = (rr.stderr || "").includes("PROBE_ASYNC_HOOK_FIRED");
    // Case 4's box is released during teardown, i.e. after the probe already
    // sampled the counters, so its only observable is that the handler ran and
    // the process still exited. The other cases must balance their own boxes.
    const flat = Number(v.allocDelta) === Number(v.freeDelta);
    const ok = rr.status === "result" && !rr.timedOut && (k === 4 ? fired : flat);
    log(ok ? "[info]" : "[BUG]", `asyncHookProbe(${k}) ${ASYNC_LABELS[k]}`,
      rr.status === "result" && !rr.timedOut
        ? `${JSON.stringify(v)}${k === 4 ? ` hookFired=${fired} (boxes released during teardown)` : ""}`
        : `status=${rr.status} exit=${rr.exit} timedOut=${rr.timedOut}`);
  }

  // Control for the handshake contract: a handler that never calls
  // RemoveAsyncCleanupHook must hang shutdown, because only the engine
  // handle's destructor releases the environment's pending-request counter.
  const ctl = spawnProbe("asyncHookProbe", [5], null, 5000);
  const ctlFired = (ctl.stderr || "").includes("PROBE_ASYNC_HOOK_FIRED_NO_REMOVE");
  report("async hook handshake is mandatory", ctl.timedOut && ctlFired,
    ctl.timedOut
      ? "handler ran, process still alive after 5s — closing the handshake is mandatory"
      : `process exited by itself (exit=${ctl.exit}) — the contract documented on AsyncCleanupHookFunc no longer holds`);
}

// ================================================= 17. externals

await group("17. external values");
for (let k = 0; k <= 3; k++) {
  const r = spawnProbe("externalProbe", [k]);
  const post = Number(r.stats?.boxOutstanding ?? NaN);
  log("[info]", `externalProbe(${k})`, `${JSON.stringify(r.value ?? r.threw)} outstanding(after GC)=${post}`);
  if (k === 0 && post > BASE_OUTSTANDING) {
    log("[BUG]", "external payload leak", `outstanding ${post} vs baseline ${BASE_OUTSTANDING}`);
  }
}

// ================================================= 18. misc

await group("18. misc");
log("[info]", "bufferCopyEmpty", JSON.stringify(await run("bufferCopyEmpty", () => p.bufferCopyEmpty())));
for (const m of [0, 1]) {
  const r = spawnProbe("promiseProbe", [m]);
  if (r.status === "crash") {
    log("[BUG]", `promiseProbe(${m})`, `PROCESS CRASH exit=${r.exit}`);
    continue;
  }
  const v = r.value ?? {};
  log("[info]", `promiseProbe(${m})`, JSON.stringify(v ?? r.threw));
  if (m === 0) expectEq("promiseProbe(0) double resolve", v.resolveAgain, "napi_invalid_arg");
  if (m === 1) expectEq("promiseProbe(1) reject", v.reject, "ok");
}
for (const [nm, argl] of [["extErrInfo", []], ["versionProbe", []], ["rawMallocProbe", []]]) {
  const r = spawnProbe(nm, argl);
  if (r.status === "crash") {
    log("[BUG]", nm, `PROCESS CRASH exit=${r.exit}`);
    continue;
  }
  const v = r.value ?? {};
  log("[info]", nm, JSON.stringify(v ?? r.threw));
  if (nm === "extErrInfo") {
    // The engine's error record must still describe the failure: reading it
    // after another successful call would silently report napi_ok.
    expectEq("extended error code", v.code, "napi_string_expected");
    expectEq("extended error message", v.msg, "A string was expected");
    expectEq("errOf captured the message", v.errText, "node-api: napi_string_expected: A string was expected");
  }
}
// raw Node-API questions
for (const [label, args] of [
  ["bigint 0n", [0, 0]],
  ["bigint 1n", [0, 1]],
  ["bigint -1n", [0, 2]],
  ["bigint 2^64-1", [0, 3]],
  ["bigint 2^64", [0, 4]],
  ["createReference(number)", [1, 0]],
  ["createReference(string)", [1, 1]],
  ["createReference(null)", [1, 2]],
  ["createReference(object)", [1, 3]],
  ["createReference(function)", [1, 4]],
  ["call with null recv", [2, 0]],
  ["setInstanceData twice", [3, 0]],
]) {
  const r = spawnProbe("rawProbe", args);
  log(r.status === "crash" ? "[BUG]" : "[info]", `rawProbe ${label}`,
    r.status === "crash" ? `PROCESS CRASH exit=${r.exit}` : JSON.stringify(r.value ?? r.threw));
}

// ================================================= 19. js package

await group("19. js convenience layer");
for (let k = 0; k <= 15; k++) {
  const r = await run(`jsProbe(${k})`, () => p.jsProbe(k));
  if (!r) continue;
  const { kind, value, ...rest } = r;
  log("[info]", `jsProbe(${k})`, `panic=${rest.panic ?? "-"} rest=${JSON.stringify(Object.fromEntries(Object.entries(rest).filter(([k2]) => k2 !== "panic")))} value=${value === undefined ? "<nil>" : typeof value}`);
}

// ================================================= summary

console.log("\n================= SUMMARY =================");
for (const b of bugs) console.log("BUG  " + b);
for (const n of notes) console.log("note " + n);
console.log(`bugs=${bugs.length} notes=${notes.length}`);
process.exitCode = bugs.length ? 1 : 0;
