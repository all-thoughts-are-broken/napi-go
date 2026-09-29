# 构建与故障排查

[English](BUILD.md) · **简体中文**

## Windows

前置：Go 1.24+，以及 MinGW-w64 gcc（UCRT 构建，MSYS2 或 mingw-w64 安装包均可）。

```sh
go build -buildmode=c-shared -o addon.node .
```

无需安装 Node 头文件，无需 node-gyp：头文件在 `include/node/`，`node.exe` 的
导入库在 `windows/node.lib`，cgo flags 由 `napi/cgo.go` 自动带上。

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

- **`gcc: error: unrecognized command-line option '-l:node.lib'`**：所用的
  链接器不认识 `-l:`。构建默认使用 `-lnode`（GNU ld 与 lld-link 都接受）；
  看到这个报错说明 `CC` 被换成了非常规驱动。
- **加载时报 `was compiled against a different Node.js version`**：所用的
  `node.exe` 比 Node-API 8 基线更老，即早于 Node **12.22**。升级 Node.js。
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
并刷新导入桩：

```sh
curl -LO https://nodejs.org/dist/v<版本>/win-x64/node.lib
mv node.lib windows/node.lib
```

只在确实需要更新的头文件时才这么做；Node-API 8 的 ABI 本身是稳定的。
