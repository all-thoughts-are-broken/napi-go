// probe/focus2.mjs — does the DefineClass box leak plateau (real leak) or
// drain away (slow collection)?
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const p = require("./probe.node");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function gcRound() {
  for (let i = 0; i < 3; i++) {
    global.gc();
    const junk = new Array(20000).fill(i);
    if (junk.length < 0) console.log(junk);
  }
  await sleep(50);
}

const base = Number(p.stats().boxOutstanding);
console.log(`baseline=${base}`);
const r = p.classChurn(300);
console.log(`classChurn(300): allocΔ=${r.allocDelta} freeΔ=${r.freeDelta} outstanding=${Number(p.stats().boxOutstanding)}`);

let last = -1;
for (let round = 0; round < 12; round++) {
  await gcRound();
  const o = Number(p.stats().boxOutstanding);
  console.log(`  round ${String(round).padStart(2)}: outstanding=${o} (Δ${o - base})${o === last ? "  <-- plateau" : ""}`);
  last = o;
}

// Control: the same churn through DefineProperties.
const base2 = Number(p.stats().boxOutstanding);
const r2 = p.propChurn(300);
await gcRound();
await gcRound();
const after2 = Number(p.stats().boxOutstanding);
console.log(`propChurn(300): allocΔ=${r2.allocDelta} outstanding ${base2} -> ${after2} (Δ${after2 - base2})`);

// Control: plain functions.
const base3 = Number(p.stats().boxOutstanding);
const r3 = p.propChurn(0); // no-op to keep the shape
await gcRound();
console.log(`(control no-op) propChurn(0) allocΔ=${r3.allocDelta} outstanding Δ${Number(p.stats().boxOutstanding) - base3}`);
