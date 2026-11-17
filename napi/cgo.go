// Package napi provides complete Go bindings for the Node-API (N-API) stable
// C ABI, allowing Go code to be compiled as a Node.js native addon
// (-buildmode=c-shared) and to exchange values with the JavaScript engine.
//
// The bindings target NAPI_VERSION 8 — the last "stable" ABI baseline — so a
// single binary loads on every Node.js release that ships Node-API 8
// (12.22+, 14.17+, 16+ and everything newer). No Node.js headers need to be
// installed: the official node_api.h / js_native_api.h headers are bundled in
// the include/ directory of this module, and nothing from Node.js is linked.
// The napi_* functions come from the process that loads the addon: on Linux
// and macOS the dynamic loader resolves them, on Windows the package binds them
// itself when the addon is loaded (host_windows.c), so the host executable
// does not have to be called node.exe.
//
// This file carries the package-wide #cgo directives; the shared C
// declarations live in napi_preamble.h, which every cgo-using file includes
// (cgo preambles are per-file).
package napi

//go:generate go run ./internal/genhost

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/../include/node

// Linux/macOS: the host executable exports the napi_* symbols; the dynamic
// loader resolves our references at dlopen time.
#cgo linux LDFLAGS: -Wl,--unresolved-symbols=ignore-all
#cgo darwin LDFLAGS: -Wl,-undefined,dynamic_lookup

// Windows: nothing to link. A PE DLL may not contain unresolved symbols, so
// host_windows.c defines every napi_* function as a jump through a pointer and
// fills the pointers in from the host process at load time.

#include "napi_preamble.h"
*/
import "C"
