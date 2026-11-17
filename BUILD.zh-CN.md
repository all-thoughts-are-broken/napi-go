# 构建与故障排查

[English](BUILD.md) · **简体中文**

## Windows

前置：Go 1.24+，以及 MinGW-w64 gcc（UCRT 构建，MSYS2 或 mingw-w64 安装包均可）。

```sh
go build -buildmode=c-shared -o addon.node .
```

无需安装 Node 头文件，无需 node-gyp，也没有导入库：头文件在 `include/node/`，
cgo flags 由 `napi/cgo.go` 自动带上。不链接任何 Node.js 的东西——插件在加载它的
进程里查找 `napi_*` 函数，所以宿主不必叫 `node.exe`。

## Linux 与 macOS

```sh
# Debian/Ubuntu
sudo apt install golang gcc
# macOS：安装 Go 与 Xcode 命令行工具

go build -buildmode=c-shared -o addon.node .
```

`napi_*` 符号由宿主进程在 `dlopen` 时提供，所以构建期不链接任何东西：
Linux 上放行未解析符号（`-Wl,--unresolved-symbols=ignore-all`），
macOS 上用 `-Wl,-undefined,dynamic_lookup`。两者都已写在 `napi/cgo.go` 里。

## 故障排查

- **`napi_generic_failure: the process that loaded this addon does not provide
  N of the Node-API functions it binds`**（Windows）：插件依次在可执行文件、
  `libnode.dll`、`node.dll` 里查找 `napi_*` 函数，没有全部找到。要么宿主比
  Node-API 8 基线更老（Node **12.22**），要么它从别的名字的模块导出 Node-API。
  报错信息里给出了第一个缺失的函数名。
- **加载时报 `was compiled against a different Node.js version`**：所用的
  `node.exe` 比 Node-API 8 基线更老，即早于 Node **12.22**。升级 Node.js。
- **插件在构建机上能加载，换台机器就报「The specified module could not be
  found」**：它可能依赖了编译器自带、但不属于 Windows 的运行时 DLL。
  `objdump -p addon.node | grep "DLL Name"` 会列出它需要的 DLL；除了
  `KERNEL32.dll`、`ntdll.dll` 和 `api-ms-win-crt-*` 那一组之外的都属于工具链
  （例如 MCF 线程模型的 gcc 带来的 `libmcfgthread-2.dll`）。静态链接进去即可：
  `go build -buildmode=c-shared -ldflags "-extldflags=-static" -o addon.node .`
- **`require` 时进程崩溃**：确认 gcc 是 UCRT 构建（`gcc --version` 输出应含
  `ucrt`）。MSVCRT 构建的 gcc 未测试。
- **clang（MSVC target）**：能编译链接（`lld-link`），但加载时挂起——怀疑
  Go runtime 与 `lld-link` 产物的 TLS/启动路径交互有问题。排查中；
  请改用 MinGW gcc，或在 Linux/macOS 上用 gcc/clang。
- **Linux 上插件能加载但符号缺失**：确认是用 `-buildmode=c-shared` 构建的，
  并且直接加载产出的 `.node` 文件，而不是把它链进另一个共享库。

## 更新内置头文件

本模块刻意做成自包含的，所以升级 Node.js 一般无需任何操作。若要跟进更新的
头文件集，用某个 Node.js 发行版的版本替换 `include/node/` 下的四个头文件，
然后重新生成 Windows 构建在加载时绑定的函数列表：

```sh
go generate ./napi    # 重写 napi/host_symbols.h；需要 C 编译器
```

列表取自 `NAPI_VERSION 8` 下的头文件，所以更新的函数在模块提升目标版本之前不会
被绑定。

只在确实需要更新的头文件时才这么做；Node-API 8 的 ABI 本身是稳定的。
