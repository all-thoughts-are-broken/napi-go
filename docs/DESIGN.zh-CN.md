# 设计文档

[English](DESIGN.md) · **简体中文**

## 总体架构

```
JS (V8)                    C ABI (Node-API 8)                 Go
──────                     ──────────────────                 ──
addon.node  ◄──加载──►  宿主进程 (napi_* 导出)  ◄──cgo──►  napi 包
   │                                                        │
   │  napi_register_module_v1 ◄───── //export ────────────  entry 包
   │  napiGoExecuteCallback  ◄───── //export ─────────────  回调蹦床
   └── 值交换：napi_value ↔ napi.Value(unsafe.Pointer)
```

- **只依赖稳定 C ABI**：`NAPI_VERSION=8`，全部针对加载插件的那个进程解析：
  Linux/macOS 上由动态加载器完成，Windows 上由绑定自己完成（见「Windows 上如何
  找到 Node-API 函数」）。不链接 V8 和 Node.js、不碰 C++。
- **头文件内置**：`include/node/` 是官方 `node_api.h` / `js_native_api.h`（MIT）。
  构建时由 `napi/cgo.go` 的 `#cgo` 指令自动接线，用户无需安装任何 Node 组件。

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
- **自动清理：回调盒子属于它的函数**：`CreateFunction` 在函数对象上注册
  `napi_add_finalizer`；JS GC 回收函数时，finalizer 删除 cgo handle 并释放盒子。
  `DefineProperties` 对每个方法、getter、setter 遵循同一条规则：函数由绑定自己
  创建（方法随后作为持有它的数据属性来定义，访问器通过 `Reflect.defineProperty`
  定义），于是每个函数拥有自己的盒子。

  这些盒子以前挂在**宿主对象**上，那是一个 use-after-free。从对象上读出的方法
  （`const m = obj.m`）或从属性描述符里取出的访问器都是普通函数，可以比对象活得
  久；对象一被回收，盒子就被释放，下一次调用执行的是恰好拿到这块被重用内存的
  那个 Go 回调。实测：取出的方法返回了另一个函数的结果，全程没有任何报错。
  任何对象的死亡都说明不了一个函数是否还能被调用——只有函数自己的死亡可以。

  `DefineClass` 是唯一的例外，而且只因为"已知限制"里描述的引擎行为才成立：原型
  成员由模板构建，绑定无法拥有那些函数，它们的盒子挂在类构造函数上
  （`napiGoPropsFinalizeCallback`）。这之所以安全，是因为引擎永不回收类，构造函数
  比它们都活得久。
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

**令牌随它的环境一起消失——但一刻也不能提前。** 环境关闭时会释放它还拥有的每一个
句柄，不管 addon 有没有归还；而 addon 通常会把引用（构造函数、回调）一直保留到
环境结束。这些令牌以前会在表里留到进程结束：用每个保留 5000 个引用的 Worker
实测，每个引用有 109 字节永远收不回来。现在每个令牌都记录了拥有它的环境，环境
关闭之后，表里属于它的令牌会被清掉。

微妙之处在时机。环境在关闭的最后一步才运行 addon 剩余的 finalizer，而在
finalizer 里删除自己持有的引用——文档推荐的释放方式——必须还能找到它的令牌。
普通的 cleanup hook 运行在这些 finalizer **之前**；在那里清令牌会让这次
`DeleteReference` 返回 `napi_invalid_arg`（实测）。所以绑定用了两个钩子。第一个
在环境的第一个令牌发放时注册，它只负责注册第二个。Node.js 是分批执行 cleanup
hook 的，在某一批执行期间新增的钩子要等到下一批——而下一批排在拆除 Node-API
环境、运行其 finalizer 的那个钩子之后。真正清表的是第二个钩子。

令牌指向的是每个环境一份的记录，而不是 `napi_env` 的地址，因为引擎会把这个地址
复用给下一个环境，新的 Worker 不能被当成刚结束的那个。这份记账的代价是每个句柄
多一次 map 查找：实测一次 handle scope 的打开加关闭从约 195 ns 变为约 225 ns。

这也是为什么 `AsyncWork` 从一开始就是正确的形态——它是 Go 结构体指针，
天然唯一；`ThreadsafeFunction` 持有的也是指向 Go 侧状态的指针而不是令牌：那份
状态本来就必须存在，而且同样唯一。令牌只是把同样的事实带给那几种由引擎分配的
对象。

## worker_threads（多环境）

同一进程内的多个 Worker 共享同一个 Go runtime：只要它们 require **同一个
addon 文件路径**（Windows LoadLibrary 按路径去重，引用计数共享）。
压测 #16 验证 4 环境 + 主环境共 5 个 env 在一个 runtime 下并发工作。

**addon 在加载时会把自己钉在内存里。** 加载 addon 的环境销毁时，Node.js 会关闭
它的库句柄。如果那个环境是唯一的持有者——addon 只在 Worker 里被 require，或者
Worker 加载的是文件的独立副本——这次关闭就是最后一个引用，库被卸载。而库里的
Go runtime 无法关停：调度器、GC、sysmon 线程还在运行，此时跑在已释放的内存上。
在 Windows、Node.js 22 上实测：

| 布局 | 不钉住 | 钉住 |
| --- | --- | --- |
| 主线程与 worker 用同一路径 | 正常（主线程一直持有引用） | 正常 |
| 只被 Worker require | Worker 退出后 `0xC0000005` | 正常，之后还能再次加载 |
| 主线程持有原文件，Worker 加载副本 | Worker 退出后 `0xC0000005` | 正常 |
| 同一线程 require 两份副本 | 正常 | 正常 |

最后一行说明了根因：一个进程里的两个 Go runtime 可以并存，崩溃来自卸载而不是
第二个 runtime。压测初期版本给每个 worker 发了一份副本，段错误 100% 复现，当时
归因为"多个 Go runtime 互相冲突"；改为共享路径后消失，只是因为主线程从此一直
持有这个库。

`napi/pin_windows.go` 用 `GetModuleHandleEx(GET_MODULE_HANDLE_EX_FLAG_PIN)` 钉住
模块。`napi/pin_darwin.go` 用 `RTLD_NODELETE` 额外 `dlopen` 一次；思路相同，但没有
像 Windows 那样实测过——功能测试里的 worker-only 检查会在 CI 里覆盖它。ELF 平台
无需处理：Go 链接器本来就给 c-shared 库加了 `-z nodelete`。

共享路径仍然是正确的布局。每份副本都是独立的 Go runtime，有自己的堆、线程和
包级状态——一份副本存进 Go 全局变量的值，另一份看不到——而且任何副本都不会被
卸载。

## 注册协议

- `napi_register_module_v1`（//export，进 DLL 导出表）：Node 加载时调用，
  逐个创建 `entry.Export` 的函数并挂到 exports。
- `node_api_module_get_api_version_v1`：返回 8，等价 NAPI_MODULE 宏的版本
  探针，node 据此校验 ABI 兼容性。

## Windows 上如何找到 Node-API 函数

PE DLL 不允许未解析符号，所以 Linux 的 `--unresolved-symbols=ignore-all` 路线
不可行：绑定调用的每个 `napi_*` 函数都必须在链接期有定义。

通常的做法就是本模块以前的做法——链接 `node.lib`，即 `node.exe` 的导入库。这会把
宿主的**文件名**写进插件的导入表，Windows 加载器随后去找一个叫 `node.exe` 的
模块。用一份改了名的 `node.exe` 作宿主实测：

| 宿主 | 结果 |
| --- | --- |
| `node.exe` | 正常加载 |
| 其它名字，`PATH` 上没有 `node.exe` | `ERR_DLOPEN_FAILED`：「The specified module could not be found」 |
| 其它名字，`PATH` 上有 `node.exe` | 无声退出，`0xC0000028` |

最后一行里，满足这个导入的是那份别的文件——第二份 Node.js 映像，从未初始化——
而不是宿主。「其它名字」并不罕见：`electron.exe`、所有单文件可执行程序、任何
嵌入方都是。node-gyp 用一个 delay-load 钩子把 `node.exe` 重定向到当前可执行文件来绕开它，
这需要 MSVC 的 delay-load 支持；GNU `ld` 没有等价物，除非随模块分发与工具链相关
的 delay-import 库。

所以插件不从宿主导入任何符号（`napi/host_windows.c`）：

- 每个 `napi_*` 函数都**在插件里定义**，实现是经由一个函数指针的跳转。函数列表
  来自 `NAPI_VERSION 8` 下的内置头文件（`napi/host_symbols.h`，由
  `go generate ./napi` 生成）。
- 插件加载时，`napi_go_bind_host` 用 `GetProcAddress` 填入这些指针，来源是下列
  模块中第一个导出 Node-API 的：可执行文件本身（`node.exe`、`electron.exe`、
  单文件可执行程序），然后是 `libnode.dll`（以共享库方式构建的 Node.js；
  node-gyp 的钩子也用这个回退）和 `node.dll`。
- 宿主没有导出的函数保留一个返回 `napi_generic_failure` 的替身，错误里带有
  说明，所以过旧的宿主得到的是一条报错，而不是经空指针跳转。

头文件在 Windows 上把这些函数声明为 `__declspec(dllexport)`，插件一旦定义它们，
它们就会全部进入插件自己的导出表；`napi_preamble.h` 把 `NAPI_EXTERN` 置空来避免
这一点。这个方案的代价是每次调用多一次间接跳转，在 cgo 切换面前看不出来（实测：
前后每次调用的耗时相同）。

以上用一份改了名的 `node.exe` 实测，它现在可以加载并运行插件。Electron 和单文件
可执行程序以同样的方式从可执行文件导出 Node-API，但没有实际试过。

对调用方的影响：你自己的 C 代码如果直接调用 `napi_*` 函数，在 Windows 上不需要
任何链接参数——这些函数由 `napi` 包定义，就在同一个共享库里（`probe` 包的原始
探针就是这样工作的）。但现在不再链接宿主的任何东西，所以如果代码曾经通过内置的
`node.lib` 使用 `node.exe` 的**其它**符号——libuv、V8——就需要自备导入库。

## 已知限制

- **引擎按自己的节奏释放线程安全函数，而且两代引擎对“什么时候”说法不一。**
  所有接收这个句柄的函数都不加检查地解引用它，所以“什么时候”决定了什么是
  use-after-free（`node_api.cc`，`ThreadSafeFunction` 类）：

  | 引擎 | 对象何时被释放 | use-after-free 是什么 |
  | --- | --- | --- |
  | Node.js 18、20、22，以及 24.13 及以前的 24 | 一旦 finalize 完就释放，不管线程计数是多少 | finalize 之后的任何调用，包括线程还“欠着”的那次 release |
  | Node.js 24.14 及以后 | finalize 完**并且**所有线程都 release 之后 | 比线程持有数多出来的那一次调用 |

  finalize 发生在最后一次 release、abort **或环境关闭**之后——最后一种情况下
  addon 根本没有发起任何调用。在 Node.js 22.22.2 / Windows 上实测：

  | 序列 | 结果 |
  | --- | --- |
  | goroutine 调用一个 Worker 已被终止的函数 | `0xC0000005` |
  | 纯 Node-API：`create(2) + abort + release`（同一轮，不转事件循环） | 全部 `napi_ok` |
  | 纯 Node-API：`create(2) + abort` → 转一圈 → `acquire` | `0xC0000028` |
  | 纯 Node-API：`create(2) + abort` → 转一圈 → `release` | `0xC0000028` |
  | 四个线程阻塞在容量为 1 的满队列后面，然后 abort | 一个返回 `napi_closing`，三个永久阻塞 |

  `0xC0000028` 是 `STATUS_ASSERTION_FAILURE`，在 Windows 上是进入**已删除的
  CRITICAL_SECTION** 的典型症状，即引擎的 mutex 已随对象消失。线程数 1/2/3、
  队列有界/无界表现一致。最后一行是引擎只唤醒了一个等待者（`Signal`），而它
  本该唤醒全部。

  两代引擎唯一一致的地方是 finalize 回调：它在 loop 线程上运行，此时对象还
  没被释放。绑定的处理（`napi/async.go` 里的 `tsfnState`）就建立在这一个点上：

  - 每次进入引擎的调用都持有读锁；finalizer 拿写锁，于是它会等已经在引擎里的
    调用返回，此后也不会再有新的调用进去。
  - 在 finalizer 里，绑定把 Go 还持有的线程计数全部归还。新引擎因此可以立刻
    删除对象，而不必等那些永远不会被转发的 release；对旧引擎没有任何影响。
    两边都不泄漏。
  - 此后**一切都返回 `napi_closing`、什么都不转发**。abort 之后同样如此，包括
    Node-API 文档推荐用来收尾的那个「剩余线程 Release」——在旧引擎上，出错的
    正是这次 release。
  - 绑定从不让引擎阻塞。`Call(..., napi.Blocking)` 以非阻塞方式入队，队满时在
    Go 里等待下一次派发。正因如此才有可能等待在途的调用，函数结束时也能唤醒
    每一个被阻塞的调用者。

  剩下的限制：持有 `ThreadsafeFunction` 的代码要到下一次调用返回 `napi_closing`
  时才知道环境已经没了。以上都是在 Node.js 22（旧引擎）上实测的。新引擎的路径
  是从它的源码推出来的；CI 会在自带新引擎的 Node.js 24 上运行同样的检查。

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
