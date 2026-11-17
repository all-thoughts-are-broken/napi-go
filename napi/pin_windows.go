//go:build windows

package napi

/*
#include <windows.h>

// napi_go_pin_module pins the module this function was linked into, so that
// no number of FreeLibrary calls can unmap it. The address of the function
// itself is what identifies the module.
static int napi_go_pin_module(void) {
  HMODULE self = NULL;
  return GetModuleHandleExW(
      GET_MODULE_HANDLE_EX_FLAG_PIN | GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS,
      (LPCWSTR)(void*)&napi_go_pin_module, &self) != 0;
}
*/
import "C"

// pinModule keeps the addon mapped for the rest of the process's life.
//
// Node.js closes an addon's library handle when the environment that loaded it
// is destroyed, and a Worker is such an environment. When no other environment
// holds the addon — it was only ever required inside the Worker, or every
// Worker loaded its own copy of the file — that close is the last reference and
// Windows unmaps the DLL. A Go runtime cannot be unloaded: its scheduler,
// collector and monitor threads are still running and now execute unmapped
// memory. Measured: an addon required only by a Worker takes the process down
// with 0xC0000005 shortly after the Worker exits.
func pinModule() bool {
	return C.napi_go_pin_module() != 0
}
