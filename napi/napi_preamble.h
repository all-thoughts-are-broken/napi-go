// napi_preamble.h is included by every Go file in this package that uses
// cgo. cgo preambles are per-file, so the shared C declarations must live in
// a regular header. It provides:
//
//   - the Node-API headers at NAPI_VERSION 8
//   - C shims for things awkward to express from Go
//   - extern declarations of the //export wrappers, so that any file can
//     pass them to Node-API as C function pointers
//
// The #cgo directives (include paths, libraries) live in cgo.go, which cgo
// scans per package.

#ifndef NAPI_GO_PREAMBLE_H_
#define NAPI_GO_PREAMBLE_H_

#define NAPI_VERSION 8
#include <node_api.h>
#include <stdlib.h>

// ---- extern prototypes of the //export trampolines -----------------------
// cgo only exposes a //export wrapper as C.<Name> inside the file that
// declares it; these declarations make the wrappers referenceable (by
// address) from every file of the package.

extern napi_value napiGoExecuteCallback(napi_env env, napi_callback_info info);
extern napi_value napiGoGetterCallback(napi_env env, napi_callback_info info);
extern napi_value napiGoSetterCallback(napi_env env, napi_callback_info info);
extern void napiGoFinalizeCallback(napi_env env, void* finalize_data, void* finalize_hint);
extern void napiGoExternalFinalizeCallback(napi_env env, void* finalize_data, void* finalize_hint);
extern void napiGoPropsFinalizeCallback(napi_env env, void* finalize_data, void* finalize_hint);
extern void napiGoInstanceDataFinalizeCallback(napi_env env, void* finalize_data, void* finalize_hint);
extern void napiGoCleanupHook(void* arg);
extern void napiGoAsyncCleanupHook(napi_async_cleanup_hook_handle handle, void* arg);
extern void napiGoAsyncExecute(napi_env env, void* data);
extern void napiGoAsyncComplete(napi_env env, napi_status status, void* data);
extern void napiGoTSFNCallJs(napi_env env, napi_value js_callback, void* context, void* data);

// ---- cgo handle boxing ----------------------------------------------------
// cgo.Handle values (uintptr) are stored in C-allocated boxes so they can be
// attached to napi objects as `void* data` without violating Go pointer
// rules. The box is freed by a Node finalizer when the owning JS object is
// collected.

static void* go_box_new(uintptr_t h) {
  void** box = (void**)malloc(sizeof(void*));
  if (box == NULL) {
    return NULL;
  }
  *box = (void*)h;
  return box;
}

static uintptr_t go_box_load(void* p) {
  return (uintptr_t)(*(void**)p);
}

static void go_box_free(void* p) {
  free(p);
}

// ---- napi_property_descriptor array helpers -------------------------------
// Building C struct arrays from Go is awkward; these shims do the layout.

static napi_property_descriptor* go_props_alloc(size_t n) {
  return (napi_property_descriptor*)calloc(n, sizeof(napi_property_descriptor));
}

static void go_props_set(napi_property_descriptor* props, size_t i,
                         const char* utf8name, napi_value name,
                         napi_callback method, napi_callback getter,
                         napi_callback setter, napi_value value,
                         unsigned int attributes, void* data) {
  props[i].utf8name = utf8name;
  props[i].name = name;
  props[i].method = method;
  props[i].getter = getter;
  props[i].setter = setter;
  props[i].value = value;
  props[i].attributes = (napi_property_attributes)attributes;
  props[i].data = data;
}

static void go_props_free(napi_property_descriptor* props) {
  free(props);
}

// napi_get_uv_event_loop takes a struct uv_loop_s**; the struct is only
// forward-declared, so cgo cannot name it. This shim hides it behind void*.
static napi_status go_get_uv_loop(napi_env env, void** out) {
  return napi_get_uv_event_loop(env, (struct uv_loop_s**)out);
}

#endif  // NAPI_GO_PREAMBLE_H_
