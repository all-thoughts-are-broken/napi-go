# 设计文档

[English](DESIGN.md) · **简体中文**

## 总体架构

```
JS (V8)                    C ABI (Node-API 8)                 Go
──────                     ──────────────────                 ──
addon.node  ◄──加载──►  node.exe (napi_* 导出)  ◄──cgo──►  napi 包
   │                                                        │
   │  napi_register_module_v1 ◄───── //export ────────────  entry 包
   │  napiGoExecuteCallback  ◄───── //export ─────────────  回调蹦床
   └── 值交换：napi_value ↔ napi.Value(unsafe.Pointer)
```

- **只依赖稳定 C ABI**：`NAPI_VERSION=8`，全部通过 `node.exe`（Windows）或
  主程序动态符号（Linux/macOS）解析。不链接 V8、不碰 C++。
- **头文件内置**：`include/node/` 是官方 `node_api.h` / `js_native_api.h`（MIT），
  `windows/node.lib` 是导入桩（只含符号表，无代码）。构建时由
  `napi/cgo.go` 的 `#cgo` 指令自动接线，用户无需安装任何 Node 组件。

## cgo 组织方式

cgo 的两条硬约束决定了文件布局：

1. **preamble 是每文件独立的**：共享的 C 声明放进 `napi/napi_preamble.h`，
   每个使用 cgo 的文件 `#include "napi_preamble.h"`。
2. **`//export` 生成的 C 包装只在声明它的文件可见**：其它文件需要取这些
   函数指针时，靠 preamble.h 里的 `extern` 原型声明（链接器解析到同一名）。
   全部蹦床集中在 `napi/registry.go`。

## 回调桥接（蹦床）

每个暴露给 JS 的 Go 函数经过一条蹦床：

```
JS 调用 f(x)
  → node 调用 napiGoExecuteCallback(env, cbinfo)      // C 包装
     → napi_get_cb_info 取出 data 指针                 // = 盒子指针
     → loadBox: cgo.Handle → cbData{cb}                // Go 侧注册表
     → cb(env, cbinfo)                                 // 用户 Go 函数
     → 返回 napi_value（nil = undefined）
```

- **盒子（box）**：C malloc 的一个指针槽，存 `cgo.Handle`。这绕开了 Go 的
  `unsafe.Pointer` 规则（uintptr→pointer 转换只发生在 C 里），让句柄可以合法
  存进 C 结构体的 `void* data`。
- **自动清理**：`CreateFunction` 在函数对象上注册 `napi_add_finalizer`；
  JS GC 回收函数时，finalizer 删除 cgo handle 并释放盒子。属性描述符的
  盒子同理挂在宿主对象上（`napiGoPropsFinalizeCallback`）。
- **带用户的 finalizer 时只用一个盒子**：`Wrap` / `CreateExternal` /
  `AddFinalizer` / `SetInstanceData` 把「Go 值 + 用户 finalizer」一起装进
  `finalizePayload{data, fn}` 这个单一盒子，而不是分别占用 data 槽和 hint 槽。
  原因：`napi_remove_wrap` 之后引擎永不再调用该 finalizer，若 finalizer 单独
  占一个 hint 盒子，那个盒子就永久泄漏（探针实测：5000 次 `Wrap`+`RemoveWrap`
  泄漏 5000 个盒子）。单盒方案让 `Unwrap` / `RemoveWrap` 拿到盒子时就能把
  值交还调用方、把盒子释放掉，账目精确。
  `SetInstanceData` 同理会先释放被替换掉的旧盒子——引擎允许覆盖该槽且不调用
  旧数据的 finalizer（引擎源码是直接 `delete old_data`）。
- **崩溃守卫**：几个"用错就崩"的 API 在 Go 侧维护存活集，把 use-after-free
  转成 `napi_invalid_arg`：`DeleteReference`/引用操作（`liveRefs`）、
  `ResolveDeferred`/`RejectDeferred`（`liveDeferreds`，引擎二次 settle 会
  无条件 `delete deferred_ref`）、`AsyncWork.Delete/Queue/Cancel`（状态机）。
  实测这三类误用原本分别是 `0xC0000374`（堆破坏）、`0xC0000028`、
  `0xC0000005`（访问违例），现在都只返回错误。
  同理，非法描述符（无 name 的 `PropertyDescriptor`、空 `ClassDescriptor`、
  nil 构造器）会在进引擎前被拒——引擎对 NULL name 是解引用崩溃
  （`0xC0000028`）。
- **panic 安全**：每条蹦床 `defer recover()`。JS 线程上的 panic 转成
  `napi_throw_error`（进程不崩）；线程池线程（async work execute）上打日志。
  消息转 C 字符串时成对 `C.CString`/`C.free`——早期版本漏了 free，2 万次
  1KB 的 panic 会泄漏 31MB 的 C 堆。
- **释放必须写成 `defer`**：所有蹦床里的 `freeBox` 都不能内联在用户回调
  之后。Go 的 `recover()` 会直接结束函数，panic 一旦抛出，后面的语句永远
  不执行——实测 2000 个 panic 的 finalizer 会永久钉住 2000 个盒子。
  同时一个函数里只能有**一处**释放：内联释放 + recover 分支再释放 =
  双重释放（C 侧 `free` 已执行 → 堆破坏）。
  蹦床里的类型断言也一律用 `, ok` 形式：断言失败既 panic 又漏掉释放。
- **DebugStats**：`boxHandle`/`freeBox` 维护原子计数，压测以
  outstanding==baseline 断言无泄漏。

## 线程模型

| 上下文 | 允许的操作 |
|---|---|
| JS 线程（回调、TSFN payload、async complete） | 全部 API |
| libuv 线程池（async work execute） | 仅纯 Go；不碰 Value |
| 任意 goroutine | 仅 ThreadsafeFunction.Call/Acquire/Release/Ref/Unref |
| GC finalizer（JS 线程内、GC 阶段） | 仅"basic" API；我们只做纯 Go 清理 |

注册表（cgo handles + 原子计数）本身线程安全，由 `napi/registry_test.go`
在 `-race` 下覆盖。

## 值生命周期规则（来源：Node-API 文档 + 实测）

1. 回调收到的 `napi.Value` 只在该回调的作用域内有效。**闭包捕获跨回调使用
   是未定义行为**（压测 #15 的 NaN 与 #18 的 300 盒子泄漏都是这一条的实证）。
   需要保留：`CreateReference`；并在宿主对象上挂 finalizer 释放
   （见 `makeStepper` 示例）。
2. **强引用 + JS 闭包会构成不可回收环**（ref 钉住 jsLevel → jsLevel 闭包持有
   goStep → goStep 的 finalizer 永远不触发）。必须在 JS 侧打破环（置 null）
   或避免闭包捕获。
3. **同步重入深度受 V8 JS 栈限制**（压测 #15：1000 层即栈溢出）。深递归用
   Promise/TSFN 异步化。
4. **TSFN 队列未排空时 Release**：node 以 NULL env 派发积压 payload（仅为
   释放数据），绑定会安全丢弃并回收盒子，但业务闭包不会执行。正确做法是
   在 drain marker 的 payload 里 Release（见 stress/main.go tsfnFlood）。
5. **TSFN 与事件循环**：Ref 状态的 TSFN 维持事件循环；Unref 后若主线程没有
   其它句柄，node 可能在派发完成前退出。
6. Buffer/ArrayBuffer 的 `[]byte` 视图只在 JS 对象存活期间有效。

## 句柄身份：令牌，而不是引擎指针

引擎交回来的每个句柄（`napi_ref`、`napi_deferred`、`napi_async_context`、
`napi_handle_scope`、`napi_callback_scope`、`napi_threadsafe_function`）都是
裸指针，且引擎对它**不做任何校验**。绑定因此必须自己记账，把「已经归还过的
句柄」拦下来而不是转发——`napi_delete_reference` 之流就是一句 `delete`，
二次调用即 double free。

**关键设计决定：记账的键不能用引擎指针。** C++ 堆的分配器会把刚释放的地址
原样还给下一个同尺寸对象，于是陈旧句柄会「变成」一个活对象的名字：存活集查表
成功，操作被放行，销毁的是调用方没提过的那个东西。实测（64 轮 create/release
循环，直到地址被复用）：

| 句柄 | 地址被复用 | 被放行的陈旧操作 |
| --- | --- | --- |
| `napi_ref` | 28 | 28 |
| `napi_handle_scope` | 8 | 8 |
| `napi_deferred` | 1 | 1 |
| `napi_async_context` | 1 | 1 |

`napi_deferred` 那一栏的失败形态最坏：**不是崩溃而是错值**——陈旧句柄为
一个活着的 promise 落了 resolve，本该被 resolve 的那个永远 pending。

方案见 `napi/handle_token.go`：给每个句柄分配一个 Go 对象，把**它的地址**
作为调用方看到的句柄值（类型仍是 `unsafe.Pointer`），引擎指针只存在令牌表里。

- Go 不会让两个**存活**对象共享地址 → 令牌唯一。
- 令牌有两个持有者：调用方的变量（GC 会扫描 `unsafe.Pointer`）与令牌表。
  只要任一方还在，地址就不会被回收 → 陈旧令牌不会被新句柄「接替」。
- 因此「已归还」是可判定的，而不是概率性的。
- 每种句柄一套令牌表（`tokenSet`）：把 `Ref` 令牌交给 `CloseHandleScope`
  会被拒绝，而不是被重新解释。

这也是为什么 `AsyncWork` 从一开始就是正确的形态——它是 Go 结构体指针，
天然唯一；令牌只是把同样的事实带给那几种由引擎分配的对象。

## worker_threads（多环境）

同一进程内的多个 Worker 共享同一个 Go runtime：只要它们 require **同一个
addon 文件路径**（Windows LoadLibrary 按路径去重，引用计数共享）。
压测 #16 验证 4 环境 + 主环境共 5 个 env 在一个 runtime 下并发工作。

**警告：绝不要给每个 worker 发 addon 文件的独立副本**——那会在一个进程里
实例化多个 Go runtime（信号处理器、TLS、线程注册互相冲突），必然崩溃。
压测初期版本犯过这个错，段错误 100% 复现，改为共享路径后消失。

## 注册协议

- `napi_register_module_v1`（//export，进 DLL 导出表）：Node 加载时调用，
  逐个创建 `entry.Export` 的函数并挂到 exports。
- `node_api_module_get_api_version_v1`：返回 8，等价 NAPI_MODULE 宏的版本
  探针，node 据此校验 ABI 兼容性。

## 为什么不用 node.lib 之外的链接方案（Windows）

- PE DLL 不允许未解析符号（Linux 的 `--unresolved-symbols=ignore-all` 路线
  在 Windows 不可行）。
- `-lnode` 搜索 `node.lib`：GNU ld 和 lld-link 都支持，因此 MinGW gcc 与
  clang 均可链接。
- node.lib 只声明符号，不引入实现；产物运行时绑定到宿主 node.exe。

## 已知限制

- **`napi_release_threadsafe_function(..., napi_tsfn_abort)` 之后的事件循环
  转身会销毁对象，文档所述的线程计数语义不成立。** 实测（纯 Node-API，
  `probe/bridge.go` 的原始探针，Node.js 22 / Windows）：

  | 序列 | 结果 |
  | --- | --- |
  | `create(2) + abort + release`（同一轮，不转事件循环） | 全部 `napi_ok` |
  | `create(2) + abort` → 转一圈 → `acquire` | `0xC0000028` |
  | `create(2) + abort` → 转一圈 → `release` | `0xC0000028` |

  线程数 1/2/3、队列有界/无界表现一致；只有「不转事件循环」才正常。
  `0xC0000028` 是 `STATUS_ASSERTION_FAILURE`，在 Windows 上是进入
  **已删除的 CRITICAL_SECTION** 的典型症状，即引擎的 mutex 已随对象消失。
  Node-API 文档说 abort 后对象「等所有线程 release 才销毁」，实现并非如此。

  绑定侧的处置：abort 之后**一切都返回 `napi_closing`、什么都不转发**，
  包括文档推荐用来收尾的那个「剩余线程 Release」。这不会钉住环境——
  引擎在销毁前已经跑过 `ReleaseResources`（`env->Unref()` + 摘 cleanup hook），
  所以进程照常退出。代价是那个引擎对象本身泄漏（无法安全回收）。

- Node-API 9/10 的 API（external string、property key、syntax error、
  module file name 等）刻意未绑定：会引入新符号依赖，破坏"一次编译、全版本
  可载入"的承诺。
- clang（MSVC target）可编译链接，但加载后 Go runtime 初始化挂起，
  待查（怀疑与 lld-link 产物的 TLS/启动路径有关）。MinGW gcc 为支持工具链。
- Windows 上 worker env 卸载期间的 finalizer 在 worker 线程执行——绑定侧
  只做内存释放，安全；但用户 finalizer 里禁止调用 Node-API。
- **`DefineClass` 的盒子会被引擎钉住，属引擎行为、绑定无法规避**。
  `napi_define_class` 内部用 V8 的 `FunctionTemplate`，而 V8 在
  `ApiNatives::InstantiateFunction` 里把实例化出的类/构造器按
  template 的 serial number **强引用缓存在 NativeContext 的 FixedArray 中**
  （`TemplateInfo::CacheTemplateInstantiation`，快缓存满后退到全局 cache），
  该缓存**在 context 生命周期内永不清理**。因此每个类对象及其绑定的盒子都会
  活到环境退出——实测 `classChurn(300)` 泄漏 669 个盒子，12 轮强制 GC 后
  纹丝不动；而 `DefineProperties` / `CreateFunction` / `Wrap` / `AddFinalizer` /
  `CreateExternal` 的对象都能正常回收（对照组 Δ0）。
  用**原始 C**（不经本绑定）调 `napi_define_class` 复现同样结果，证明与绑定
  无关。结论：**把 `DefineClass` 当模块初始化期的一次性操作**（顶层定义固定
  数量的类），不要放进热路径反复创建。
- **引擎有一族"裸 `delete`"的句柄型 API，绑定侧必须自己维护存活集。**
  `napi_delete_reference`、`napi_remove_async_cleanup_hook`、
  `napi_async_destroy` 以及 `napi_close_handle_scope` 系列在引擎里都是直接
  `delete <指针>`，除了 NULL 检查（以及 scope 系列的计数器）之外**无法判定
  句柄是否仍然有效**。实测的失败模式：

  | 误用 | 引擎行为 | 实测退出码 |
  |---|---|---|
  | `DeleteReference` 二次调用 | 裸 `delete` | `0xC0000374` |
  | `RemoveAsyncCleanupHook` 二次调用 | 裸 `delete` | `0xC0000028` |
  | `AsyncDestroy` 二次调用 | 裸 `delete` | `0xC0000374` |
  | `AsyncDestroy` 传外来指针 | 裸 `delete` | `0xC0000028` |
  | 非最内层 scope 关两次 | 计数器有余量 → `delete` 两次 | `0xC0000374` |
  | 用普通入口关可逃逸 scope | 对象类型重解释 | 未定义 |

  注意 `napi_close_handle_scope` 的计数器**只防下溢**（`open_handle_scopes == 0`
  时才返回 `napi_handle_scope_mismatch`），所以当栈上还有别的 scope 时它拦不住
  重复关闭——而这恰好是最容易写出来的那种误用。
  绑定的对策是每个句柄族配一个存活集（`liveRefs`、`liveDeferreds`、
  `asyncContexts`、`asyncCleanupLive`、`liveHandleScopes`、
  `liveEscapableScopes`），在**交给引擎之前**判定，命中就返回
  `napi_invalid_arg`。代价是每个 handle scope 一次 map 操作（无竞争时 ~20ns），
  相对 cgo 边界本身的开销可忽略，换来的是"误用不再破坏堆"。
- **`AsyncCleanupHookFunc` 必须自行关闭握手。** 引擎把异步 cleanup hook 当
  **异步握手**处理：只有 handle 的析构函数会调用
  `AsyncCleanupHookInfo::done_cb_`（进而 `DecreaseWaitingRequestCounter`），
  而该析构只发生在 `napi_remove_async_cleanup_hook` 里。回调不调用它，
  **进程退出会被永久阻塞**（实测：hook 已执行、进程 5 秒后仍存活，最终被
  SIGTERM 杀掉）。这条绑定兜不了底——绑定只能保证"调用它是安全的、重复调用
  返回错误"，不能替用户调用。
