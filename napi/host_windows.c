// host_windows.c provides the Node-API functions on Windows.
//
// A PE DLL cannot leave symbols unresolved the way an ELF or Mach-O library
// can, so every napi_* function the binding calls has to be defined somewhere
// at link time. The usual answer is to import them from node.exe through its
// import library, and that writes the *file name* of the host into the addon:
// the loader then looks for a module called "node.exe". In a host with any
// other name — electron.exe, a single-executable application, a renamed node —
// that either fails ("The specified module could not be found") or, when a
// node.exe happens to be on PATH, loads a second, uninitialised copy of Node.js
// and calls into it. Measured with a renamed copy of node.exe as the host: the
// first, and with node.exe on PATH a silent exit with 0xC0000028.
//
// So the addon imports nothing from the host. Each function is defined here as
// a jump through a pointer, and napi_go_bind_host fills the pointers in when
// the addon is loaded, from whichever module of the running process exports
// Node-API. The host can be called anything.

#include "napi_preamble.h"

#include <stdlib.h>
#include <windows.h>

// napi_go_absent stands in for a function the host does not export. It
// ignores its arguments and reports failure, so calling a missing function is
// an error rather than a jump through a null pointer. Every Node-API function
// uses the C calling convention, under which a callee that reads no arguments
// can be entered with any.
static napi_status NAPI_CDECL napi_go_absent(void) {
  return napi_generic_failure;
}

// ---- the functions ---------------------------------------------------------

#define NAPI_GO_HOST(name, params, args)                                       \
  static napi_status(NAPI_CDECL* napi_go_host_##name) params =                 \
      (napi_status(NAPI_CDECL*) params)napi_go_absent;                         \
  napi_status NAPI_CDECL name params { return napi_go_host_##name args; }

#define NAPI_GO_HOST_VOID(name, params, args)                                  \
  static void(NAPI_CDECL* napi_go_host_##name) params =                        \
      (void(NAPI_CDECL*) params)napi_go_absent;                                \
  void NAPI_CDECL name params { napi_go_host_##name args; }

// A function declared not to return must not return even when the host lacks
// it; abort is the closest thing to what it promises.
#define NAPI_GO_HOST_NORETURN(name, params, args)                              \
  static void(NAPI_CDECL* napi_go_host_##name) params =                        \
      (void(NAPI_CDECL*) params)napi_go_absent;                                \
  NAPI_NO_RETURN void NAPI_CDECL name params {                                 \
    napi_go_host_##name args;                                                  \
    abort();                                                                   \
  }

#include "host_symbols.h"

#undef NAPI_GO_HOST
#undef NAPI_GO_HOST_VOID
#undef NAPI_GO_HOST_NORETURN

// ---- the table that binds them ----------------------------------------------

#define NAPI_GO_HOST(name, params, args) {#name, (void**)&napi_go_host_##name},
#define NAPI_GO_HOST_VOID(name, params, args) NAPI_GO_HOST(name, params, args)
#define NAPI_GO_HOST_NORETURN(name, params, args)                              \
  NAPI_GO_HOST(name, params, args)

static const struct {
  const char* name;
  void** slot;
} napi_go_host_table[] = {
#include "host_symbols.h"
};

#undef NAPI_GO_HOST
#undef NAPI_GO_HOST_VOID
#undef NAPI_GO_HOST_NORETURN

// napi_go_find_host returns the module of this process that exports Node-API,
// or NULL. The executable comes first: node.exe, electron.exe and
// single-executable applications all export it themselves. A Node.js built as
// a shared library exports it from libnode.dll instead (node.dll for some
// embedders), which is the same fallback node-gyp's load hook uses.
static HMODULE napi_go_find_host(void) {
  static const wchar_t* const candidates[] = {NULL, L"libnode.dll", L"node.dll"};
  size_t i;
  for (i = 0; i < sizeof(candidates) / sizeof(candidates[0]); i++) {
    HMODULE module = GetModuleHandleW(candidates[i]);
    if (module != NULL && GetProcAddress(module, "napi_create_object") != NULL) {
      return module;
    }
  }
  return NULL;
}

// napi_go_bind_host points every function at the host's implementation. It
// returns how many the host does not export — zero for any host with
// Node-API 8 — and stores the name of the first of them in *first_missing.
// Functions that are missing keep answering napi_generic_failure.
int napi_go_bind_host(const char** first_missing) {
  HMODULE host = napi_go_find_host();
  int missing = 0;
  size_t i;
  for (i = 0; i < sizeof(napi_go_host_table) / sizeof(napi_go_host_table[0]);
       i++) {
    FARPROC proc =
        host != NULL ? GetProcAddress(host, napi_go_host_table[i].name) : NULL;
    if (proc != NULL) {
      *napi_go_host_table[i].slot = (void*)proc;
      continue;
    }
    if (missing == 0 && first_missing != NULL) {
      *first_missing = napi_go_host_table[i].name;
    }
    missing++;
  }
  return missing;
}
