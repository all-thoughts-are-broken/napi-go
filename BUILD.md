# Building and troubleshooting

**English** · [简体中文](BUILD.zh-CN.md)

## Windows

Requirements: Go 1.24+, and MinGW-w64 gcc (a UCRT build, as shipped by MSYS2 or
the mingw-w64 installers).

```sh
go build -buildmode=c-shared -o addon.node .
```

No Node.js headers, no node-gyp and no import library: the headers live in
`include/node/` and the cgo flags in `napi/cgo.go` add them automatically.
Nothing is linked against Node.js — the addon finds the `napi_*` functions in
the process that loads it, so the host does not have to be called `node.exe`.

## Linux and macOS

```sh
# Debian/Ubuntu
sudo apt install golang gcc
# macOS: install Go and the Xcode command line tools

go build -buildmode=c-shared -o addon.node .
```

The `napi_*` symbols are provided by the host process when the addon is
`dlopen`ed, so nothing is linked at build time: on Linux the flags allow
unresolved symbols (`-Wl,--unresolved-symbols=ignore-all`), and on macOS they use
`-Wl,-undefined,dynamic_lookup`. Both are set in `napi/cgo.go`.

## Troubleshooting

- **`napi_generic_failure: the process that loaded this addon does not provide
  N of the Node-API functions it binds`** (Windows) — the addon looked for the
  `napi_*` functions in the executable, then in `libnode.dll` and `node.dll`,
  and did not find all of them. Either the host is older than the Node-API 8
  baseline (Node **12.22**), or it exports Node-API from a module with another
  name. The message names the first missing function.
- **`was compiled against a different Node.js version` at load time** — the
  `node.exe` in use is older than the Node-API 8 baseline, i.e. earlier than Node
  **12.22**. Upgrade Node.js.
- **The addon loads on the build machine but not elsewhere ("The specified
  module could not be found")** — it may depend on a runtime DLL of the compiler
  that is not part of Windows. `objdump -p addon.node | grep "DLL Name"` lists
  what it needs; anything beyond `KERNEL32.dll`, `ntdll.dll` and the
  `api-ms-win-crt-*` set belongs to the toolchain (`libmcfgthread-2.dll` with
  MCF-thread builds of gcc, for example). Link it in statically:
  `go build -buildmode=c-shared -ldflags "-extldflags=-static" -o addon.node .`
- **The process crashes on `require`** — check that gcc is a UCRT build
  (`gcc --version` should mention `ucrt`). MSVCRT builds of gcc are untested.
- **clang with the MSVC target** compiles and links (via `lld-link`) but the
  process hangs during load: the Go runtime is suspected to interact badly with
  the TLS/startup path of `lld-link` output. Under investigation; use MinGW gcc
  or gcc/clang on Linux/macOS.
- **The addon loads but the symbols are missing on Linux** — make sure you build
  with `-buildmode=c-shared` and load the resulting `.node` file directly, rather
  than linking it into another shared object.

## Keeping the vendored headers up to date

The module is self-contained on purpose, so upgrading Node.js normally requires
no action. To follow a newer header set, replace the four files under
`include/node/` with the versions from a Node.js release and regenerate the list
of functions the Windows build binds at load time:

```sh
go generate ./napi    # rewrites napi/host_symbols.h; needs a C compiler
```

The list is taken from the headers at `NAPI_VERSION 8`, so newer functions stay
unbound until the version the module targets is raised.

Only do this if you actually need a newer header; the Node-API 8 ABI itself is
stable.
