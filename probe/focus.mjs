// probe/focus.mjs — one focused experiment: where do DefineClass' boxes go?
//
// For each lifecycle mechanism we (1) build the object through the binding,
// (2) hand it to JS, (3) hold it only through a WeakRef, (4) force a
// collection, and (5) report both the WeakRef (did the engine collect the
// object?) and the box ledger (did our finalizer run?).
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const p = require("./probe.node");

function rssMB() { if (global.gc) { global.gc(); global.gc(); } return process.memoryUsage().rss / 1048576; }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function gcHard() {
  for (let i = 0; i < 8; i++) {
    global.gc();
    const junk = new Array(20000).fill(i);
    if (junk.length < 0) console.log(junk);
  }
  await sleep(80);
  for (let i = 0; i < 8; i++) global.gc();
}

const names = ["DefineProperties", "DefineClass", "CreateFunction", "Wrap", "AddFinalizer", "CreateExternal"];
const baseline = Number(p.stats().boxOutstanding);
console.log(`baseline outstanding = ${baseline}`);

for (let kind = 0; kind < names.length; kind++) {
  let weak;
  const build = () => {
    const r = p.weakTarget(kind);
    weak = new WeakRef(r.target);
    return { err: r.err, allocDelta: r.allocDelta, freeDelta: r.freeDelta };
  };
  const info = build();
  await gcHard();
  const collected = weak.deref() === undefined;
  const outstanding = Number(p.stats().boxOutstanding);
  console.log(
    `${names[kind].padEnd(17)} allocΔ=${String(info.allocDelta).padStart(2)} ` +
    `weakRefCollected=${String(collected).padEnd(5)} ` +
    `outstanding=${outstanding} (Δ${outstanding - baseline}) ` +
    `${collected && outstanding > baseline ? "<== BOX LEAK: object died, finalizer did not run" : ""}` +
    `${!collected ? "<== object still alive after GC" : ""}`
  );
}

// Churn: many dangling objects at once.
for (const [nm, fn] of [["propChurn(300)", () => p.propChurn(300)], ["classChurn(300)", () => p.classChurn(300)]]) {
  const before = Number(p.stats().boxOutstanding);
  const r = fn();
  const mid = Number(p.stats().boxOutstanding);
  await gcHard();
  const after = Number(p.stats().boxOutstanding);
  console.log(`${nm.padEnd(17)} allocΔ=${r.allocDelta} outstanding ${before} -> ${mid} -> ${after} (after GC, Δ${after - before})`);
}

console.log(`final: outstanding=${Number(p.stats().boxOutstanding)} rss=${rssMB().toFixed(1)}MB`);
