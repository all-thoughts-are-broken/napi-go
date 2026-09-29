package entry

/*
// entry_preamble.h includes <node_api.h>, so this package needs the same
// vendored header path the napi package uses. Without it the package only
// builds where the headers happen to be on the system include path.
#cgo CFLAGS: -I${SRCDIR}/../include/node

#include "entry_preamble.h"
*/
import "C"

import "github.com/all-thoughts-are-broken/napi-go/napi"

// napi_register_module_v1 is the symbol Node.js looks up when loading the
// addon (the same contract as the NAPI_MODULE macro). //export makes cgo
// place it in the DLL's export table.
//
//export napi_register_module_v1
func napi_register_module_v1(cEnv C.napi_env, cExports C.napi_value) C.napi_value {
	env := napi.Env(cEnv)
	exportsObj := napi.Value(cExports)
	result := initialize(env, exportsObj)
	if result == nil {
		return cExports
	}
	return C.napi_value(result)
}

// node_api_module_get_api_version_v1 is the optional ABI version probe
// Node.js uses to verify compatibility. Returning 8 states that this addon
// is compiled against the stable Node-API 8 surface.
//
//export node_api_module_get_api_version_v1
func node_api_module_get_api_version_v1() C.int32_t {
	return 8
}
