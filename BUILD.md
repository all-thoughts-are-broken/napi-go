# Building and troubleshooting

**English** · [简体中文](BUILD.zh-CN.md)

## Windows

Requirements: Go 1.24+, and MinGW-w64 gcc (a UCRT build, as shipped by MSYS2 or
the mingw-w64 installers).

```sh
go build -buildmode=c-shared -o addon.node .
```

No Node.js headers and no node-gyp: the headers live in `include/node/`, the
`node.exe` import library is `windows/node.lib`, and the cgo flags in
`napi/cgo.go` add them automatically.

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

- **`gcc: error: unrecognized command-line option '-l:node.lib'`** — the linker
  in use does not understand `-l:`. The build defaults to `-lnode` (accepted by
  both GNU ld and lld-link); seeing this error means `CC` was overridden with an
  unusual driver.
- **`was compiled against a different Node.js version` at load time** — the
  `node.exe` in use is older than the Node-API 8 baseline, i.e. earlier than Node
  **12.22**. Upgrade Node.js.
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
`include/node/` with the versions from a Node.js release and refresh the import
stub:

```sh
curl -LO https://nodejs.org/dist/v<version>/win-x64/node.lib
mv node.lib windows/node.lib
```

Only do this if you actually need a newer header; the Node-API 8 ABI itself is
stable.
