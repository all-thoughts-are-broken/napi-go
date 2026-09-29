# napi-go

[![CI](https://github.com/all-thoughts-are-broken/napi-go/actions/workflows/ci.yml/badge.svg)](https://github.com/all-thoughts-are-broken/napi-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/all-thoughts-are-broken/napi-go.svg)](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/all-thoughts-are-broken/napi-go)](https://goreportcard.com/report/github.com/all-thoughts-are-broken/napi-go)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**用 Go 写 Node.js 原生插件。**

`napi-go` 是 **Node-API（N-API）稳定 C ABI** 的完整绑定。它让你把普通的 Go 代码
编译成 `.node` 插件，Node.js 会像加载任何其它原生模块一样加载它——不需要 C++、
不需要 `node-gyp`、不需要安装 Node 头文件，升级 Node.js 也不需要重新编译。

[English](README.md) · **简体中文**

```go
package main

import (
	"github.com/all-thoughts-are-broken/napi-go/entry"
	"github.com/all-thoughts-are-broken/napi-go/napi"
)

func Add(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	a, _ := napi.GetValueDouble(env, cb.Args[0])
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	sum, _ := napi.CreateDouble(env, a+b)
	return sum
}

func init() { entry.Export("add", Add) }

func main() {}
```

```sh
go build -buildmode=c-shared -o addon.node .
```

```js
const addon = require('./addon.node');
addon.add(1, 2); // 3
```

---

## 为什么用 napi-go

- **一次编译，全版本可载入。** 插件只调用 Node-API 8 稳定 ABI，因此同一个产物可以
  加载进任何支持 Node-API 8 的 Node.js（12.22+ / 14.17+ / 16+ 以及更新的版本），
  不必为每个 Node 版本重新构建。
- **不依赖 Node.js 构建工具链。** 官方 Node-API 头文件内置在 `include/node/`，
  Windows 上用一个很小的导入桩 `windows/node.lib` 在加载时把 `napi_*` 符号绑定到
  正在运行的 `node.exe`；Linux / macOS 上由动态加载器从宿主进程解析。
- **不绑定 MSVC。** Node-API 是纯 C ABI，MinGW-w64 gcc 与 clang 和 `cl.exe` 一样
  可用。原理见 [docs/DESIGN.md](docs/DESIGN.md)。
- **完整 API 面。** `napi` 包导出的 138 个函数与 C API 一一对应，每个都有功能断言。
- **两种风格可选。** 想显式处理错误就用 `napi`；想要 `syscall/js` 那样的写法就用 `js`。
- **误用返回错误，而不是崩溃。** 引用计数、deferred、作用域、async work、线程安全
  函数都由绑定侧记账，二次释放这类错误会变成 `error`，而不是段错误。
- **不只是"不崩"，而是跨版本正确。** 测试不只断言"没抛异常"，还断言值内容、
  finalizer 记账与内存斜率；对抗性探针专门去打"文档没写但会崩"的边界。

## 环境要求

| | |
|---|---|
| Go | 1.24 或更新 |
| C 工具链 | Windows 用 MinGW-w64 gcc（UCRT 构建）；Linux / macOS 用 gcc 或 clang |
| Node.js | 任何支持 Node-API 8 的版本（12.22+ / 14.17+ / 16+），推荐 18+ |

除此之外不需要任何东西——头文件与 Windows 导入库都随模块分发。

## 安装

```sh
go get github.com/all-thoughts-are-broken/napi-go/napi
go get github.com/all-thoughts-are-broken/napi-go/entry
```

需要便捷层的话，再引入 `github.com/all-thoughts-are-broken/napi-go/js`。

然后把自己的插件包构建成动态库：

```sh
go build -buildmode=c-shared -o addon.node .
```

## 包结构

| 包 | 用途 |
|---|---|
| [`napi`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/napi) | 核心绑定：Node-API 8 的全部函数，1:1 对应 C API；失败以 `error` 返回（`napi.AsStatus` 取出 `Status`） |
| [`entry`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/entry) | 模块注册：`entry.Export(name, cb)` 与 `entry.ExportValue(name, factory)`，在 `init()` 里调用 |
| [`js`](https://pkg.go.dev/github.com/all-thoughts-are-broken/napi-go/js) | `syscall/js` 风格便捷层：`js.ValueOf` / `js.FuncOf` / `Value.Get/Call/Set`；失败一律 panic，回调蹦床会 recover 并转成 JS 异常 |

## 文档

- [API 参考](docs/API.zh-CN.md) —— 按主题分组的全部函数。
- [设计文档](docs/DESIGN.zh-CN.md) —— ABI 策略、cgo 组织方式、回调蹦床、
  值生命周期规则与已知限制。
- [压力测试报告](docs/STRESS.zh-CN.md) —— 18 个场景与实测数值。
- [构建与排查](BUILD.zh-CN.md)

## 用法

### 低层 `napi` 风格

每个调用都返回 `error`，`nil` 表示 `napi_ok`。

```go
func Add(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, err := napi.GetCbInfo(env, info)
	if err != nil {
		return nil
	}
	a, err := napi.GetValueDouble(env, cb.Args[0])
	if err != nil {
		return nil // 引擎已设置待处理异常
	}
	b, _ := napi.GetValueDouble(env, cb.Args[1])
	sum, _ := napi.CreateDouble(env, a+b)
	return sum
}
```

### `js` 便捷风格

```go
func Hello(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	name := js.Wrap(env, cb.Args[0]).String()
	return js.ValueOf(env, "hello, "+name).Raw()
}
```

### 从 goroutine 调用 JavaScript

跨线程调用 JS 的唯一合法方式是线程安全函数。`napi-go` 支持完整的 payload 闭包，
闭包在 JS 线程上执行，可以直接创建值、调用 JS：

```go
func Greet(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	jsCallback := cb.Args[1]

	tsfn, _ := napi.NewThreadsafeFunction(env, jsCallback, 0, 1)
	tsfn.Unref(env)
	go func() {
		time.Sleep(time.Second)
		tsfn.Call(func(e napi.Env, fn napi.Value) {
			msg, _ := napi.CreateStringUtf8(e, "from goroutine")
			undef, _ := napi.GetUndefined(e)
			napi.CallFunction(e, undef, fn, msg)
		}, napi.Blocking)
		tsfn.Release(napi.Release)
	}()
	return nil
}
```

> `Release(napi.Abort)` 之后这个句柄就作废了：引擎会在下一轮事件循环里销毁对象，
> 此后任何调用——**包括剩余线程的 `Release`**——都会踩已释放内存。绑定因此把
> abort 之后的全部操作拦成 `napi_closing`。能用普通 `Release` 就尽量别 abort。

### Promise 与 async work（libuv 线程池）

```go
func SlowDouble(env napi.Env, info napi.CallbackInfo) napi.Value {
	cb, _ := napi.GetCbInfo(env, info)
	input, _ := napi.GetValueDouble(env, cb.Args[0])

	promise, deferred, _ := napi.CreatePromise(env)
	work, _ := napi.NewAsyncWork(env, "slowDouble",
		func(e napi.Env) { /* 在线程池线程上：只能是纯 Go */ },
		func(e napi.Env, err error) {
			out, _ := napi.CreateDouble(e, input*2)
			napi.ResolveDeferred(e, deferred, out)
		})
	work.Queue(env)
	return promise
}
```

## ABI 策略

- 绑定**只**调用 Node-API 稳定 C ABI（`NAPI_VERSION=8`），不碰 V8 与 C++ 内部。
- `include/node/` 内置官方 `node_api.h` / `js_native_api.h`（MIT，来自 Node.js 项目）；
  `windows/node.lib` 是导入桩——只含符号表、不含代码——所以产物会绑定到加载它的
  那个 `node.exe`。
- 刻意**不**绑定 Node-API 9/10 的函数（external string、property key、syntax error 等）：
  那会引入链接期符号依赖，破坏「一次编译、到处加载」的承诺。
- 运行时可用 `napi.GetVersion(env)` 探测引擎支持的最高 Node-API 版本。

## 平台支持

| 平台 | 链接方式 | 状态 |
|---|---|---|
| Windows x64 | 链接内置的 `windows/node.lib` 导入桩 | ✅ 已验证 |
| Linux x64 / arm64 | 不链接任何库；`-Wl,--unresolved-symbols=ignore-all`，`dlopen` 时由宿主进程解析符号 | ✅ CI 构建并测试 |
| macOS x64 / arm64 | `-Wl,-undefined,dynamic_lookup` | ✅ CI 构建并测试 |

Linux 与 macOS 的宿主进程本身导出 `napi_*` 符号，动态加载器会自动解析，因此不需要
额外文件。在这两个平台上构建只需要 Go 和 C 编译器。

工具链说明：

| 工具链 | 状态 |
|---|---|
| MinGW-w64 gcc（UCRT） | ✅ 支持，Windows 的参考工具链 |
| clang / lld-link（windows-msvc target） | ⚠️ 可编译链接，但加载后 Go runtime 初始化挂起，排查中，暂不支持 |
| MSVC（`cl.exe`） | 不支持——Go 的 cgo 驱动不直接对接 MSVC |
| gcc / clang（Linux、macOS） | ✅ 支持 |

## 并发与生命周期规则

- JS 线程之外的 goroutine 只允许使用
  `ThreadsafeFunction.Call/Acquire/Release/Ref/Unref`。
- `napi.Value` 只在当前回调内有效。要跨回调保留 JS 值就用 `napi.CreateReference`
  （refcount 为 `0` 是弱引用），并把它绑到宿主对象的 finalizer 上释放。
  **在闭包里捕获回调参数并跨回调使用是未定义行为**，而且强引用被 JS 闭包持有时
  会构成不可回收的环。
- 每个 Go 回调都有 `recover()`：panic 转成 JS 异常而不是砸掉进程；finalizer 里的
  panic 打日志。
- Go 回调对应的 JS 函数对象被 GC 时，注册表句柄会自动释放（`napi_add_finalizer`），
  压测场景会对盒子计数做断言。
- Buffer / ArrayBuffer 返回的 `[]byte` 视图只在 JS 对象存活且未 detach 时有效，
  不要长期持有。
- 同步 JS↔Go 重入深度受 V8 JS 栈限制（数百层以内）；深递归请异步化。
- worker_threads 下所有 Worker 必须 require **同一个 addon 文件路径**——每个副本都会
  在一个进程里实例化第二个 Go runtime，属未定义行为且必然崩溃。

每条规则背后的完整推理与实测数据见 [docs/DESIGN.zh-CN.md](docs/DESIGN.zh-CN.md)。

## 测试

```sh
# 功能断言（覆盖全部 138 个导出函数）
go build -buildmode=c-shared -o example/goaddon.node ./example
node --expose-gc example/test.mjs

# 18 个压力场景
go build -buildmode=c-shared -o stress/stress.node ./stress
node --expose-gc stress/run_all.mjs

# 对抗性探针：文档没写但会崩的边界
go build -buildmode=c-shared -o probe/probe.node ./probe
node --expose-gc probe/run.mjs
node --expose-gc probe/edge.mjs

# 并发单测
go test -race ./napi/...
```

当前状态：功能断言 **71/71**、压测 **18/18**、`-race` 通过、探针套件 **0 bug**
（探针剩余的 note 都是已记录的引擎行为，不是绑定缺陷）。

CI 会在 Linux、macOS、Windows 三平台 × Node.js 18/20/22/24 上跑功能套件，
并额外跑一次压测、探针与 race 检测。

## 版本策略

遵循[语义化版本](https://semver.org/lang/zh-CN/)，发布记录见 [CHANGELOG.md](CHANGELOG.md)。

因为产物面向 Node-API 稳定 ABI，升级 Node.js 不需要重新编译。升级*头文件*是可选的：
用更新的 Node.js 版本替换 `include/node/` 下的四个头文件，并从
`https://nodejs.org/dist/v<版本>/win-x64/node.lib` 更新 `windows/node.lib`。

## 参与贡献

欢迎提交 issue 与 pull request。开 PR 之前请确认：

```sh
gofmt -l .        # 不应有任何输出
go vet ./...      # 必须干净
go test -race ./napi/...
```

并确保功能套件与压测套件仍然通过。

## 许可证

[MIT](LICENSE)。内置的 Node.js 头文件与 `windows/node.lib` 原样分发，遵循 Node.js
项目的 MIT 许可证；详见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
