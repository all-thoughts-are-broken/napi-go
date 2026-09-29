// probe/edgecase.mjs — one hostile lifecycle case in its own process.
//
//   node --expose-gc edgecase.mjs <case>[@settle]
//
// Prints SCEN_RESULT <json>, then SCEN_END, then *falls off the end*. Nothing
// here calls process.exit: whether the environment can still tear itself down
// is one of the things under test, and an explicit exit would hide it.
import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
const p = require("./probe.node");

const SCEN = process.argv[2];
// A trailing @settle gives the event loop one turn after the case runs, so
// that "the call is broken" is never confused with "the probe never let the
// loop notice a released object".
const settle = SCEN.includes("@settle");
const base = SCEN.replace("@settle", "");

const N = 1 << 20; // past malloc's mmap threshold, and large enough that Go's
                   // scavenger hands the pages back to the OS on FreeOSMemory

function churnV8(rounds = 20000) {
  const keep = [];
  for (let i = 0; i < rounds; i++) keep.push({ i, s: "y".repeat(48) });
  return keep.length;
}

// makeAlive returns only a scalar, so neither the ArrayBuffer nor any view
// onto it survives the frame.
function makeAlive() {
  const r = p.grabV8Mem(N);
  const u8 = new Uint8Array(r.buffer);
  const first = u8[0];
  return { first, len: r.len };
}

const out = { scen: base, settle };
try {
  if (/^tsfn\d+$/.test(base)) {
    out.ret = p.tsfnProbe(Number(base.slice(4)));
  } else {
    switch (base) {
      // ---- direction 1: V8 points at Go memory ---------------------------
      case "goMemNoPin": {
        const r = p.goMemNoPin(N);
        out.err = r.err;
        p.freeGo(); // Go GC, then hand the pages back to the OS
        const u8 = new Uint8Array(r.buffer);
        out.first = u8[0];
        out.last = u8[u8.length - 1];
        break;
      }
      case "goMemNoPinNoGc": { // control: read before any collection
        const r = p.goMemNoPin(N);
        out.err = r.err;
        const u8 = new Uint8Array(r.buffer);
        out.first = u8[0];
        out.last = u8[u8.length - 1];
        break;
      }
      case "goMemPinned": { // supported pattern: the slice rides in finalData
        const r = p.goMemPinned(N);
        out.err = r.err;
        p.freeGo();
        const u8 = new Uint8Array(r.buffer);
        out.first = u8[0];
        out.last = u8[u8.length - 1];
        break;
      }

      // ---- direction 2: Go points at V8 memory ---------------------------
      case "v8MemReclaimed": {
        p.dropCrossGc();
        const info = makeAlive();
        out.beforeFirst = info.first;
        out.beforeLen = info.len;
        globalThis.gc();
        globalThis.gc();
        out.kept = churnV8();
        globalThis.gc();
        const r = p.readV8Mem();
        out.afterFirst = r.first;
        out.afterMid = r.mid;
        out.afterLast = r.last;
        break;
      }

      // ---- direction 3: a V8 handle outliving its handle scope -----------
      case "staleHandle": {
        p.dropCrossGc();
        p.saveStale({ tag: "SENTINEL", s: "A".repeat(200) });
        globalThis.gc();
        churnV8(5000);
        globalThis.gc();
        out.ret = p.useStale("MARKER");
        break;
      }
      case "staleHandleNoGc": { // control: no collection at all
        p.dropCrossGc();
        p.saveStale({ tag: "SENTINEL", s: "A".repeat(200) });
        out.ret = p.useStale("MARKER");
        break;
      }

      // ---- direction 4: does V8 even know the memory exists? -------------
      case "externalAccounting": {
        const g = p.goMemPinned(N);
        out.err = g.err;
        out.reported = p.externalMemoryReport(N);
        break;
      }

      // ---- aliasing: does the engine recycle handle addresses? -----------
      case "refAlias":
        out.ret = p.refAliasProbe();
        break;
      case "scopeAlias":
        out.ret = p.scopeAliasProbe();
        break;
      case "deferredAlias":
        out.ret = p.deferredAliasProbe();
        break;
      case "asyncCtxAlias":
        out.ret = p.asyncCtxAliasProbe();
        break;

      // ---- abort with a thread still acquired, then a stray call ---------
      case "tsfnAbort": {
        out.abort = p.tsfnProbe(14);
        // Give the loop its turn: this is when the engine's own state moves
        // past anything the binding can observe (kClosing -> kClosed, async
        // handle closed, cleanup hook removed).
        await new Promise((r) => setTimeout(r, 60));
        out.follow = p.tsfnProbe(15);
        break;
      }

      default:
        out.unknown = true;
    }
  }
} catch (e) {
  out.threw = e.constructor.name + ": " + String(e.message).slice(0, 200);
}
if (settle) {
  await new Promise((r) => setTimeout(r, 80));
}
console.log("SCEN_RESULT " + JSON.stringify(out));
console.log("SCEN_END");
