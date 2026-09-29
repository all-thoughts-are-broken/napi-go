// entry_preamble.h gives the cgo files of package entry access to the
// Node-API types. The module registration symbols themselves are produced
// by //export in register.go, which makes cgo list them in the DLL's export
// table (via the generated export_file.def) — exactly where Node.js looks.

#ifndef NAPI_GO_ENTRY_PREAMBLE_H_
#define NAPI_GO_ENTRY_PREAMBLE_H_

#define NAPI_VERSION 8
#include <node_api.h>

#endif  // NAPI_GO_ENTRY_PREAMBLE_H_
