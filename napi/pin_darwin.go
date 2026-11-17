//go:build darwin

package napi

/*
#include <dlfcn.h>
#include <stddef.h>

// napi_go_pin_module takes a reference on the image this function was linked
// into and never gives it back, so the dlclose that follows the last
// environment cannot unload it. RTLD_NODELETE states the same intent to dyld.
static int napi_go_pin_module(void) {
  Dl_info info;
  if (dladdr((const void*)&napi_go_pin_module, &info) == 0 || info.dli_fname == NULL) {
    return 0;
  }
  return dlopen(info.dli_fname, RTLD_NOW | RTLD_NODELETE) != NULL;
}
*/
import "C"

// pinModule keeps the addon loaded for the rest of the process's life. See
// pin_windows.go for why: Node.js closes the library when the last environment
// that loaded it goes away, and a Go runtime cannot be unloaded.
func pinModule() bool {
	return C.napi_go_pin_module() != 0
}
