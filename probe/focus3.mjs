// probe/focus3.mjs — is the DefineClass box leak the binding's fault or the
// engine's?
//
// Four classes, each held only by a WeakRef:
//   * a plain JS class (sanity check that WeakRef + gc works at all)
//   * a class from raw napi_define_class, binding not involved
//   * a class from the binding's DefineClass
//   * a function from the binding's CreateFunction
// If the JS class and the raw class both die but the binding's class does not,
// the leak is ours. If the raw class survives too, the engine is holding it.
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const p = require("./probe.node");
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

const cases = [
  ["plain JS class       ", () => { class Foo {} return { err: "ok", target: Foo }; }],
  ["raw define_class     ", () => p.rawCls()],
  ["binding DefineClass  ", () => p.weakTarget(1)],
  ["binding CreateFunction", () => p.weakTarget(2)],
];

for (const [name, build] of cases) {
  const r = build();
  const weak = new WeakRef(r.target);
  r.target = null; // drop the only strong reference to the object
  await gcHard();
  const collected = weak.deref() === undefined;
  console.log(`${name}  err=${String(r.err).padEnd(6)} collected=${collected}`);
}
