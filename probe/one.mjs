// probe/one.mjs — run exactly one probe call in its own process so a hard
// crash cannot take the rest of the suite with it.
//
//   node one.mjs <probeName> [args-as-json] [nogc] [afterProbeName]
//
// Prints PROBE_START / PROBE_RESULT / PROBE_STATS / PROBE_RESULT2 / PROBE_END.
// The probe call happens inside its own function frame so that no stale stack
// or context slot keeps the probe's JS objects alive while we force a
// collection — a conservative GC would otherwise report every
// not-yet-finalised box as a leak. `afterProbeName` is read after that
// collection, for probes that report counters the finalizers advanced.
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const p = require("./probe.node");

const name = process.argv[2];
const args = process.argv[3] ? JSON.parse(process.argv[3]) : [];
const gcAfter = process.argv[4] !== "nogc";
const afterName = process.argv[5] || null;

function summarize(v) {
  if (v === undefined) return "<undefined>";
  if (v === null) return "null";
  try { return JSON.stringify(v); } catch { return String(v); }
}

function callProbe() {
  const fn = p[name];
  if (typeof fn !== "function") return { missing: true };
  try {
    const v = fn(...args);
    // Some probes hand back a promise on purpose (promiseProbe(1) rejects
    // one). Node's default unhandled-rejection policy would kill this child
    // before it reports, so swallow the rejection here — the probe result is
    // what the driver measures.
    if (v !== null && typeof v === "object" && v.promise && typeof v.promise.then === "function") {
      v.promise.then(() => {}, () => {});
    }
    return { result: summarize(v) };
  } catch (e) {
    return { threw: e.constructor.name + ": " + String(e.message).slice(0, 200) };
  }
}

console.log("PROBE_START " + name);
const outcome = callProbe();
if (outcome.missing) {
  console.log("PROBE_MISSING " + name);
  process.exit(3);
}
if (outcome.threw) console.log("PROBE_THREW " + outcome.threw);
else console.log("PROBE_RESULT " + outcome.result);

if (gcAfter && global.gc) {
  // Allocate churn to overwrite dead stack slots, then collect repeatedly.
  for (let i = 0; i < 5; i++) {
    global.gc();
    const junk = new Array(10000).fill(i);
    if (junk.length < 0) console.log(junk);
  }
  await new Promise((r) => setTimeout(r, 80));
  for (let i = 0; i < 8; i++) global.gc();
  console.log("PROBE_STATS " + summarize(p.stats()));
}
if (afterName && typeof p[afterName] === "function") {
  try {
    console.log("PROBE_RESULT2 " + summarize(p[afterName]()));
  } catch (e) {
    console.log("PROBE_THREW2 " + e.constructor.name + ": " + String(e.message).slice(0, 200));
  }
}
console.log("PROBE_END " + name);
