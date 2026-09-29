// probe/edge.mjs — handle-lifecycle and cross-collector edge sweep.
//
// Every case runs in its own process (edgecase.mjs) under a hard timeout,
// because the outcomes that matter are not all crashes. The engine can also
// silently *succeed* against memory that one of the two collectors has already
// released — and a wrong value flowing out with a status of `ok` is worse than
// a crash, because nothing reports it.
//
// Sections:
//   0  control   does the loop just need a turn before teardown?
//   A  TSFN      a Go handle the engine may have already `delete`d
//   B  V8→Go     V8 points at memory Go owns
//   C  Go→V8     Go points at memory V8 owns
//   D  handle    a napi_value outliving its handle scope
//   E  aliasing  do the guard sets survive address reuse? does V8 even know
//                about the external memory we create?
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));

let bugs = 0;
const notes = [];
const hazards = [];

function log(tag, name, detail) {
  console.log(`${tag.padEnd(8)} ${name.padEnd(30)} ${detail}`);
  if (tag === "[BUG]") bugs++;
  if (tag === "[hazard]") hazards.push(`${name}: ${detail}`);
  if (tag === "[note]") notes.push(`${name}: ${detail}`);
}

function runCase(name, timeout = 25000, withSettle = true) {
  const spec = withSettle ? name + "@settle" : name;
  const r = spawnSync(process.execPath, ["--expose-gc", "edgecase.mjs", spec], {
    cwd: HERE, encoding: "utf8", timeout,
  });
  const stdout = r.stdout || "";
  const line = stdout.split(/\r?\n/).find((l) => l.startsWith("SCEN_RESULT "));
  let value = null;
  if (line) {
    try { value = JSON.parse(line.slice(12)); } catch { value = null; }
  }
  const reported = !!line;
  const timedOut = !!(r.error && r.error.code === "ETIMEDOUT");
  return {
    value,
    reported,
    timedOut,
    crashed: !reported && !timedOut,
    exit: r.status,
    signal: r.signal,
    settled: withSettle,
    stderr: (r.stderr || "").trim().split(/\r?\n/).filter((l) => l).slice(-2).join(" | "),
  };
}

function detailOf(r) {
  if (r.timedOut) return r.reported ? "HANG at environment teardown" : "HANG inside the case";
  if (r.crashed) return `CRASH exit=${r.exit} signal=${r.signal} ${r.stderr}`;
  return JSON.stringify(r.value);
}

// classify: crashing or hanging is a bug; `extra` inspects the report.
function classify(name, r, extra, note, tag = "[BUG]") {
  if (r.timedOut || r.crashed) {
    log(tag, name, `${detailOf(r)} — ${note}`);
    return;
  }
  const verdict = extra ? extra(r.value || {}) : null;
  if (verdict) log(tag, name, `${verdict} | ${detailOf(r)} — ${note}`);
  else log("[ok]", name, `${detailOf(r)} — ${note}`);
}

// hazard marks a case whose failure mode is real and reproducible but belongs
// to the caller's side of the contract. It still runs on every sweep — a fix
// would show up as [ok] and a regression as the same verdict — but it is not
// counted against the binding. Each one has a documented way out.
const hazard = (name, r, extra, note) => classify(name, r, extra, note, "[hazard]");

// ---------------------------------------------------------- 0. loop control
console.log("=== 0. control: does teardown need an event-loop turn? ===");
for (const n of ["tsfn0", "goMemPinned"]) {
  const bare = runCase(n, 25000, false);
  const settled = runCase(n, 25000, true);
  if (bare.timedOut && settled.timedOut) {
    log("[BUG]", n + ":bare", "hangs even after a loop turn — real teardown bug");
  } else if (bare.timedOut && !settled.timedOut) {
    log("[note]", n + ":bare", "hangs only when the process exits before the loop turns");
  } else {
    log("[ok]", n + ":bare", detailOf(bare));
  }
}

// ================================================ A. threadsafe function ====
console.log("\n=== A. ThreadSafeFunction lifecycle (engine `delete this` vs Go handle) ===");
// Node-API answers use-after-release with undefined behaviour. The binding
// tracks nothing, so anything the engine happens to tolerate shows up as a
// plain "ok" here — a contract violation reaching engine state that is at best
// state-dependent and at worst freed.
const TSFN_CASES = {
  tsfn0: "baseline: create + one release",
  tsfn1: "release twice",
  tsfn2: "abort twice",
  tsfn3: "call after release",
  tsfn4: "call after abort",
  tsfn5: "acquire after release",
  tsfn6: "unref/ref after release",
  tsfn7: "GetContext after release",
  tsfn8: "release x4 (over-release)",
  tsfn9: "address reuse between two functions",
  tsfn10: "allocator churn, then poke the dead handle",
  tsfn11: "create and never release",
  tsfn12: "two threads, only one released",
  tsfn13: "release -> acquire -> release (back to zero)",
};
// Cases that deliberately leave the engine's thread count above zero. They are
// here to characterise what an unmatched Release costs the *process* (the
// engine never closes the uv_async handle in that state, so it never exits) —
// not to demand the binding repair a contract it cannot see.
const UNMATCHED_RELEASE = {
  tsfn11: "never released",
  tsfn12: "one of two threads left",
};
for (const [name, note] of Object.entries(TSFN_CASES)) {
  const r = runCase(name);
  const ret = (r.value && r.value.ret) || {};

  if (name in UNMATCHED_RELEASE) {
    log("[note]", name, r.timedOut
      ? `unmatched Release (${UNMATCHED_RELEASE[name]}) pins the environment — process never exits`
      : `unmatched Release (${UNMATCHED_RELEASE[name]}) but the process still exited`);
    continue;
  }
  if (r.timedOut || r.crashed) {
    log("[BUG]", name, `${detailOf(r)} — ${note}`);
    continue;
  }
  if (name === "tsfn0") {
    classify(name, r, (v) => (v.ret && v.ret.release1 === "ok" ? null : "baseline release failed"), note);
    continue;
  }
  // Node-API leaves every call after the last Release undefined. The engine
  // happens to accept some of them; that is luck rather than a contract, and
  // the binding passes it straight through.
  const accepted = ["call", "acquire", "unref", "ref", "context"].filter((k) => ret[k] === "ok");
  if (accepted.length) {
    log("[note]", name, `engine accepted ${accepted.join("/")} after the last Release — undefined per Node-API, unchecked by the binding`);
  } else {
    log("[ok]", name, detailOf(r));
  }
}

// ==================================== A2. abort with a thread left over ====
console.log("\n=== A2. abort while a thread is still acquired ===");
{
  // An abort makes the engine destroy the function on its next event-loop
  // turn, whatever the outstanding thread count, so nothing at all may be
  // forwarded afterwards — not the stray call, and not the release that
  // Node-API documents as the way to finish an aborted function.
  const r = runCase("tsfnAbort");
  const abort = (r.value && r.value.abort) || {};
  const follow = (r.value && r.value.follow) || {};
  // The binding's guards and the engine both answer napi_closing, so the
  // message is what says which one stopped the call.
  const refused = (t) => /not live|was aborted/.test(t || "");
  const forwarded = [["call", follow.callText], ["release", follow.releaseText]]
    .filter(([, t]) => !refused(t));
  if (r.timedOut || r.crashed) {
    log("[BUG]", "tsfnAbort", `${detailOf(r)} — abort(threads left) -> call -> release`);
  } else if (forwarded.length) {
    log("[BUG]", "tsfnAbort",
      `forwarded to a destroyed function: ${forwarded.map(([k, t]) => `${k}=${JSON.stringify(t)}`).join(", ")} — the engine goes down with 0xC0000028`);
  } else {
    log("[ok]", "tsfnAbort", `abort=${abort.abort}, both follow-ups refused by the binding`);
  }
  log("[note]", "tsfnAbort", JSON.stringify(r.value));
}

// ============================================ B. V8 pointing at Go memory ==
console.log("\n=== B. cross-collector: V8 pointing at Go memory ===");
classify("goMemNoPinNoGc", runCase("goMemNoPinNoGc"),
  (v) => (v.first !== 0xaa || v.last !== 0xaa ? `already wrong: ${v.first}/${v.last}` : null),
  "control: Go slice, no collection, V8 reads 170/170");
hazard("goMemNoPin", runCase("goMemNoPin"),
  (v) => (v.first !== 0xaa || v.last !== 0xaa ? `recycled: ${v.first}/${v.last}` : null),
  "finalData=nil over Go memory — documented hazard; CreateExternalArrayBufferFromBytes is the safe form");
classify("goMemPinned", runCase("goMemPinned"),
  (v) => (v.first !== 0xaa || v.last !== 0xaa ? `recycled: ${v.first}/${v.last}` : null),
  "same call through CreateExternalArrayBufferFromBytes (the safe wrapper)");

// ============================================ C. Go pointing at V8 memory ==
console.log("\n=== C. cross-collector: Go pointing at V8 memory ===");
hazard("v8MemReclaimed", runCase("v8MemReclaimed"),
  (v) => (v.afterFirst !== 0xbb || v.afterMid !== 0xbb || v.afterLast !== 0xbb
    ? `recycled: ${v.afterFirst}/${v.afterMid}/${v.afterLast} (was 187)`
    : null),
  "a Go slice aliasing V8 storage outlives the ArrayBuffer — hold a Ref instead (see byteSlice)");

// ================================================ D. a stale napi_value =====
console.log("\n=== D. a V8 handle outliving its handle scope ===");
// A napi_value is a V8 Local: it points into the handle scope the engine opened
// around the callback. Once the callback returns, that slot is up for reuse.
// Go can hold the pointer anyway, so the failure is not a crash — it is a wrong
// value flowing out with no error at all.
const aliased = (v) => {
  const got = (v && v.ret) || {};
  return got.str === "MARKER"
    ? `saved handle read back as the new callback's argument ("MARKER") instead of the saved object`
    : null;
};
{
  const r = runCase("staleHandleNoGc");
  hazard("staleHandleNoGc", r, aliased, "a saved napi_value reused with no collection at all — wrap it in a Ref");
  log("[note]", "staleHandleNoGc", `read back as ${JSON.stringify(r.value && r.value.ret)}`);
}
{
  const r = runCase("staleHandle");
  hazard("staleHandle", r, aliased, "a saved napi_value reused across a collection + heap churn — wrap it in a Ref");
  log("[note]", "staleHandle", `read back as ${JSON.stringify(r.value && r.value.ret)}`);
}

// ================================== E. aliasing and external accounting =====
console.log("\n=== E. does the engine recycle handles the guards key on? ===");
{
  const r = runCase("refAlias");
  classify("refAlias", r, (v) => {
    const ret = (v && v.ret) || {};
    const recycled = Number(ret.recycled || 0);
    const accepted = Number(ret.staleAccepted || 0);
    if (accepted > 0) return `${accepted}/${recycled} recycled rounds accepted a stale delete — a live reference was destroyed`;
    return null;
  }, "create/delete/create until the allocator recycles the address, then delete through the stale handle");
  log("[note]", "refAlias", JSON.stringify(r.value && r.value.ret));
}
{
  const r = runCase("scopeAlias");
  classify("scopeAlias", r, (v) => {
    const ret = (v && v.ret) || {};
    const accepted = Number(ret.staleAccepted || 0);
    if (accepted > 0) return `${accepted}/${ret.recycled} recycled rounds accepted a stale close — a live scope was closed`;
    return null;
  }, "open/close a scope, open another, close through the stale handle");
  log("[note]", "scopeAlias", JSON.stringify(r.value && r.value.ret));
}
{
  const r = runCase("deferredAlias");
  classify("deferredAlias", r, (v) => {
    const ret = (v && v.ret) || {};
    const accepted = Number(ret.staleAccepted || 0);
    if (accepted > 0) return `${accepted}/${ret.recycled} recycled rounds settled a promise through a stale deferred`;
    return null;
  }, "create/settle a promise, create another, settle through the stale deferred");
  log("[note]", "deferredAlias", JSON.stringify(r.value && r.value.ret));
}
{
  const r = runCase("asyncCtxAlias");
  classify("asyncCtxAlias", r, (v) => {
    const ret = (v && v.ret) || {};
    const accepted = Number(ret.staleAccepted || 0);
    if (accepted > 0) return `${accepted}/${ret.recycled} recycled rounds destroyed a live async context through a stale handle`;
    return null;
  }, "init/destroy an async context, init another, destroy through the stale handle");
  log("[note]", "asyncCtxAlias", JSON.stringify(r.value && r.value.ret));
}

console.log("\n=== F. does V8 know about the external memory we hold? ===");
{
  const r = runCase("externalAccounting");
  classify("externalAccounting", r, (v) => {
    const d = Number(v.delta || 0);
    const alloc = Number(v.allocated || 0);
    if (d < alloc / 2) {
      return `V8's external-memory counter grew by ${d} for a ${alloc}-byte buffer`;
    }
    return null;
  }, "napi_adjust_external_memory delta across CreateExternalArrayBuffer");
}

console.log(`\nEDGE_SUMMARY bugs=${bugs} notes=${notes.length}`);
for (const n of notes) console.log("  note: " + n);
process.exit(bugs === 0 ? 0 : 1);
