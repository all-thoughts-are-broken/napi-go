// Package entry registers the exports of a Node.js addon built from Go.
//
// Call Export from an init() function (or package-level variables), then
// build with -buildmode=c-shared and require the resulting file from JS:
//
//	package main
//
//	import (
//	    "github.com/all-thoughts-are-broken/napi-go/entry"
//	    "github.com/all-thoughts-are-broken/napi-go/napi"
//	)
//
//	func Hello(env napi.Env, info napi.CallbackInfo) napi.Value { ... }
//
//	func init() { entry.Export("hello", Hello) }
//	func main() {}
//
//	// JS:
//	// const addon = require("./addon.node"); addon.hello();
//
// The module registers itself through the symbol napi_register_module_v1
// plus the ABI version probe node_api_module_get_api_version_v1, the same
// contract the NAPI_MODULE macro generates for C addons.
package entry

import (
	"fmt"
	"os"
	"sync"

	"github.com/all-thoughts-are-broken/napi-go/napi"
)

// namedExport is one pending registration: either a callback (turned into a
// JS function) or a lazily-built value (a class, a constant object, ...).
type namedExport struct {
	name      string
	cb        napi.Callback
	valueFunc func(napi.Env) (napi.Value, error)
}

var (
	exportsMu sync.Mutex
	exports   []namedExport
)

// Export registers cb under name. Call it from init() or before the module
// is loaded. Names must be unique; duplicates replace earlier ones.
func Export(name string, cb napi.Callback) {
	exportsMu.Lock()
	defer exportsMu.Unlock()
	upsert(namedExport{name: name, cb: cb})
}

// ExportValue registers a value built when the module is loaded — a class
// created with napi.DefineClass, a constant object, anything.
func ExportValue(name string, factory func(napi.Env) (napi.Value, error)) {
	exportsMu.Lock()
	defer exportsMu.Unlock()
	upsert(namedExport{name: name, valueFunc: factory})
}

func upsert(e namedExport) {
	for i := range exports {
		if exports[i].name == e.name {
			exports[i] = e
			return
		}
	}
	exports = append(exports, e)
}

// initialize is invoked by Node.js when the addon is loaded, once per
// environment (including one per Worker).
func initialize(env napi.Env, exportsObj napi.Value) napi.Value {
	exportsMu.Lock()
	pending := make([]namedExport, len(exports))
	copy(pending, exports)
	exportsMu.Unlock()

	for _, e := range pending {
		var (
			val napi.Value
			err error
		)
		switch {
		case e.valueFunc != nil:
			val, err = e.valueFunc(env)
		case e.cb != nil:
			var fn napi.Value
			fn, err = napi.CreateFunction(env, e.name, e.cb)
			val = fn
		}
		if err != nil {
			reportAndThrow(env, "creating export "+e.name, err)
			return exportsObj
		}
		if val == nil {
			continue
		}
		if err := napi.SetNamedProperty(env, exportsObj, e.name, val); err != nil {
			reportAndThrow(env, "setting export "+e.name, err)
			return exportsObj
		}
	}
	return exportsObj
}

func reportAndThrow(env napi.Env, what string, err error) {
	msg := fmt.Sprintf("napi-go entry: %s: %v", what, err)
	fmt.Fprintln(os.Stderr, msg)
	_ = napi.ThrowError(env, "NAPI_GO_ENTRY", msg)
}
