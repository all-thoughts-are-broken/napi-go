//go:build !windows && !darwin

package napi

// pinModule has nothing to do here: on ELF platforms the Go linker already
// marks a c-shared library with -z nodelete, which turns dlclose into a no-op.
func pinModule() bool { return true }
