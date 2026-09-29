// Package napi provides complete Go bindings for the Node-API (N-API) stable
// C ABI, allowing Go code to be compiled as a Node.js native addon
// (-buildmode=c-shared) and to exchange values with the JavaScript engine.
//
// The bindings target NAPI_VERSION 8 — the last "stable" ABI baseline — so a
// single binary loads on every Node.js release that ships Node-API 8
// (12.22+, 14.17+, 16+ and everything newer). No Node.js headers need to be
// installed: the official node_api.h / js_native_api.h headers are bundled in
// the include/ directory of this module, and on Windows the bundled
// windows/node.lib import library resolves the napi_* symbols against the
// running node.exe at load time. On Linux and macOS the napi_* symbols are
// resolved from the host process automatically by the dynamic loader.
//
// This file carries the package-wide #cgo directives; the shared C
// declarations live in napi_preamble.h, which every cgo-using file includes
// (cgo preambles are per-file).
package napi

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/../include/node

// Windows: PE DLLs may not contain unresolved symbols, so we link against a
// small import library that forwards the napi_* calls to the host node.exe.
// node.lib is just an import stub — the binary produced from it runs against
// any node.exe whose Node-API version is >= the one the stub was generated
// from, i.e. any Node.js that supports Node-API 8. -lnode finds node.lib
// with both GNU ld (MinGW gcc) and lld-link (clang).
#cgo windows LDFLAGS: -L${SRCDIR}/../windows -lnode

// Linux/macOS: node.exe-equivalents export the napi_* symbols from the main
// executable; the dynamic loader resolves our references at dlopen time.
#cgo linux LDFLAGS: -Wl,--unresolved-symbols=ignore-all
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup

#include "napi_preamble.h"
*/
import "C"
