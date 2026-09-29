# API 参考

`napi-go` 的 `napi` 包与 Node-API C API 一一对应（`napi_` 前缀去掉，驼峰命名）。
所有返回 `error` 的地方，`nil` 表示 `napi_ok`；非 nil 时可用
`napi.AsStatus(err)` 提取 `Status`。除特别注明外，所有函数只能在 **JS 线程**
（回调内或 TSFN payload 内）调用。

[English](API.md) · **简体中文**

## 环境

| 函数 | 说明 |
|---|---|
| `GetVersion(env) (uint32, error)` | 引擎支持的最高 Node-API 版本 |
| `GetNodeVersion(env) (NodeVersion, error)` | 运行中的 Node.js 版本 |
| `GetUVEventLoop(env) (unsafe.Pointer, error)` | libuv 事件循环指针（opaque） |
| `SetInstanceData(env, data any, onFinalize) / GetInstanceData(env)` | 每环境一个原生数据槽；二次调用**覆盖**（旧盒子由绑定释放，旧 `onFinalize` 不会被调用——引擎在覆盖时直接 `delete old_data`） |

## 值：创建

| 函数 | JS 结果 |
|---|---|
| `GetUndefined` / `GetNull` / `GetGlobal` / `GetBoolean` | 单例与原始值 |
| `CreateObject` / `CreateArray` / `CreateArrayWithLength` | 对象与数组 |
| `CreateDouble` / `CreateInt32` / `CreateUint32` / `CreateInt64` | number（int64 超过 2^53 会失真，用 BigInt） |
| `CreateStringUtf8` / `CreateStringLatin1` / `CreateStringUtf16` | 三种编码的字符串 |
| `CreateSymbol` | symbol |
| `CreateDate` | Date（毫秒） |
| `CreateBigIntInt64` / `CreateBigIntUint64` / `CreateBigIntWords` | BigInt（words 为小端 64 位肢） |
| `CreateExternal(env, any, onFinalize)` | 包装任意 Go 值的不透明对象 |
| `CreateError` / `CreateTypeError` / `CreateRangeError` | Error 对象（code/msg 为 Value） |

三个 `Create*Error` 的 `code` 参数必须是字符串或 `nil`；`nil` 就是「没有错误码」
的写法，传 `undefined` 会得到 `napi_string_expected`。这一点容易漏，因为该函数返回
`(Value, error)`——忽略错误的话，reject 出去的就是一个裸字符串而不是 `Error`：

```go
undef, _ := napi.GetUndefined(env)        // 错：napi_string_expected
ev, err := napi.CreateError(env, undef, msg)

ev, err := napi.CreateError(env, nil, msg) // 对：不带错误码
```

## 值：读取与判断

| 函数 | 说明 |
|---|---|
| `Typeof` | typeof 分类（null 独立于 object） |
| `GetValueDouble/Int32/Uint32/Int64/Bool` | 提取原始值（类型不符返回错误） |
| `GetValueStringUtf8/Latin1/Utf16` | 字符串提取（内嵌 NUL 保留） |
| `GetValueBigIntInt64/Uint64/Words` | BigInt 提取（Int64/Uint64 带 lossless 标志） |
| `GetDateValue` | Date 毫秒值 |
| `CoerceToBool/Number/Object/String` | JS 强制转换（可能执行用户代码） |
| `IsArray/IsArrayBuffer/IsTypedArray/IsDataView/IsDate/IsError/IsPromise/IsBuffer/IsDetachedArrayBuffer` | 类型谓词 |
| `GetArrayLength` / `StrictEquals` / `InstanceOf` | 长度、严格相等、instanceof |

## 对象与属性

| 函数 | 说明 |
|---|---|
| `SetProperty/GetProperty/HasProperty/DeleteProperty/HasOwnProperty` | 按 Value 键（string 或 symbol） |
| `SetNamedProperty/GetNamedProperty/HasNamedProperty` | 按 UTF-8 名（便捷） |
| `SetElement/GetElement/HasElement/DeleteElement` | 按索引 |
| `GetPrototype` / `GetPropertyNames` / `GetAllPropertyNames` | 原型与键枚举（可配 mode/filter/conversion） |
| `DefineProperties(env, object, []PropertyDescriptor)` | 批量定义（方法/getter/setter/data）。每个描述符必须给 `Name` 或 `NameValue`，否则返回 `napi_invalid_arg`（引擎对 NULL name 是解引用崩溃） |
| `ObjectFreeze` / `ObjectSeal` | 冻结与密封 |
| `TypeTagObject` / `CheckObjectTypeTag` | 128 位类型标签（native 类型校验） |
| `Wrap(env, obj, any, onFinalize)` / `Unwrap` / `RemoveWrap` | Go 值挂到 JS 对象（带 finalizer）。值与 finalizer 共用**一个盒子**，所以 `RemoveWrap` 之后不会残留任何盒子；`RemoveWrap` 后 finalizer 不再触发 |
| `AddFinalizer(env, obj, data, onFinalize)` | 仅挂 finalizer |

## 函数与类

| 函数 | 说明 |
|---|---|
| `CreateFunction(env, name, Callback)` | Go 回调 → JS 函数（GC 时自动清理） |
| `GetCbInfo(env, info) (CbInfoResult, error)` | 参数、this、data |
| `GetNewTarget(env, info)` | new.target（非 new 调用为 nil） |
| `CallFunction(env, recv, fn, args...)` | 调用 JS 函数 |
| `NewInstance(env, ctor, args...)` | new 调用 |
| `DefineClass(env, ClassDescriptor)` | 定义类（构造器 + 原型/静态属性）。`Constructor` 为 nil、或描述符全空时返回 `napi_invalid_arg`。⚠️ **引擎会把类对象强引用缓存在 context 里永不回收**，所以请当模块初始化期的一次性操作，别放进热路径——详见 `docs/DESIGN.zh-CN.md` 已知限制 |
| `RunScript(env, script)` | 在当前上下文执行 JS（谨慎使用） |

## 生命周期

`Ref` / `HandleScope` / `EscapableHandleScope` / `Deferred` / `AsyncContext` /
`CallbackScope` 的值都**不是**引擎的裸指针，而是绑定发放的身份令牌
（类型仍是 `unsafe.Pointer`，`nil` 比较照旧）。引擎会把它刚释放的地址原样
再分配出去，所以裸指针无法区分「已归还的旧句柄」和「刚创建的活句柄」——
用令牌之后，已归还的句柄永远能被判定出来，不会误伤新对象。
详见 `docs/DESIGN.zh-CN.md` 的「句柄身份」一节。不要把这些值传给裸 C 调用。

| 函数 | 说明 |
|---|---|
| `OpenHandleScope` / `CloseHandleScope` | 批量控制值的生命周期；重复关闭 / 类型错配返回 `napi_invalid_arg`（引擎只按计数器守卫，拦不住这类误用） |
| `OpenEscapableHandleScope` / `CloseEscapableHandleScope` / `EscapeHandle` | 从作用域逃逸一个值 |
| `CreateReference(env, v, refcount)` / `DeleteReference` | 强（≥1）/弱（0）引用。仅接受 object/function；二次 `DeleteReference` 及删除后的任何引用操作返回 `napi_invalid_arg`（而非崩进程） |
| `ReferenceRef` / `ReferenceUnref` / `GetReferenceValue` | 引用计数调整与取值 |
| `AdjustExternalMemory(env, delta)` | 让 V8 的 GC 感知原生内存 |

## 异常

| 函数 | 说明 |
|---|---|
| `Throw` / `ThrowError` / `ThrowTypeError` / `ThrowRangeError` | 抛出（只设置 pending exception） |
| `IsExceptionPending` / `GetAndClearLastException` | 检查与取出异常 |
| `FatalException` / `FatalError` | 触发 uncaughtException / 直接终止进程 |
| `GetExtendedErrorInfo` | 最近一次 Node-API 错误的引擎信息 |

## Promise 与异步

| 函数 | 说明 |
|---|---|
| `CreatePromise` / `ResolveDeferred` / `RejectDeferred` / `IsPromise` | Promise。二次 settle 返回 `napi_invalid_arg`（引擎会无条件 `delete deferred_ref`，属 use-after-free） |
| `NewAsyncWork(env, name, execute, complete)` → `Queue/Cancel/Delete` | libuv 线程池（execute 在池线程：**纯 Go only**）。`idle/queued/done/deleted` 状态机：queue 后 delete、二次 delete、delete 后 queue 都返回错误而非崩溃 |
| `AsyncInit` / `AsyncDestroy` / `MakeCallback` | async_hooks 可见的 JS 调用；`AsyncDestroy` 只接受 `AsyncInit` 产出且未销毁的 context。`MakeCallback` / `OpenCallbackScope` 同样校验 context：传已销毁的会返回 `napi_invalid_arg`，不再把工作归因给地址上「后来者」 |
| `OpenCallbackScope` / `CloseCallbackScope` | 非回调上下文的调用作用域；二次关闭返回 `napi_callback_scope_mismatch`（引擎只按计数器守卫，同时开两个作用域时关错的那个拦不住） |

## 线程安全函数（跨 goroutine 调 JS 的唯一途径）

```go
tsfn, err := napi.NewThreadsafeFunction(env, jsFn, maxQueueSize, threadCount)
tsfn.Call(func(env napi.Env, jsFn napi.Value) { ... }, napi.Blocking|napi.NonBlocking)
tsfn.Acquire() / Release(mode) / Ref(env) / Unref(env)
```

- `Call` 的闭包在 **JS 线程**执行，可自由创建值并调用 JS；这就是 payload 通道。
- `maxQueueSize=0` 无界；有界 + `NonBlocking` 时队满返回 `napi_queue_full`。
- `Release` 会让引用计数归零并关闭函数；**注意在队列排空后再 Release**
  （队列未空时关闭，积压 payload 会被 node 以 NULL env 丢弃——绑定会安全释放
  它们，但你的闭包不会执行）。
- **`Release(napi.Abort)` 之后这个句柄就作废了**：引擎会在下一轮事件循环里
  销毁对象，此后任何调用都踩已释放内存（实测 `0xC0000028` 无声退出）。绑定因此
  在 abort 之后把所有操作——**包括剩余线程的 `Release`**——都拦成
  `napi_closing`。多线程场景请在最后一刻才 abort，或者干脆只用 `Release`
  模式让它自然收尾。
- 闭包持有 `napi.Value` 跨回调是非法的；需要保留 JS 值时在闭包里用
  `napi.CreateReference`。
- **闭包里调用 JS、而那段 JS 抛异常时**，`CallFunction` 返回
  `napi_pending_exception`，异常留在环境上。Node 22.22.2 实测：这**不会**污染同一次
  派发的后续调用——`create_object`、`create_string_utf8`、`get_undefined` 仍全部返回
  `napi_ok`，`GetAndClearLastException` 可以清掉该状态。异常最终怎么处理由引擎决定：
  默认打一条 `DEP0168` 然后丢弃；开了
  `--force-node-api-uncaught-exceptions-policy=true` 则变成真正的 uncaught exception。
  要主动决定是让它浮出来还是清掉，别假设抛了就静默消失了。

## Buffer / ArrayBuffer / TypedArray / DataView

| 函数 | 说明 |
|---|---|
| `CreateBuffer(env, n)` → `(Value, []byte)` / `CreateBufferCopy(env, []byte)` / `GetBufferInfo` | node:Buffer（返回的 []byte 是别名视图） |
| `CreateExternalBuffer(env, data, n, onFinalize, finalData)` | 零拷贝包装（部分构建禁用，返回 `napi_no_external_buffers_allowed`） |
| `CreateExternalBufferFromBytes(env, []byte, onFinalize)` | 同上，但钉住切片的动作由构造保证——**Go 堆内存一律用这个** |
| `CreateArrayBuffer` → `(Value, []byte)` / `GetArrayBufferInfo` / `DetachArrayBuffer` / `IsDetachedArrayBuffer` | ArrayBuffer |
| `CreateExternalArrayBuffer(env, data, n, onFinalize, finalData)` | 零拷贝包装 Go 内存（同上） |
| `CreateExternalArrayBufferFromBytes(env, []byte, onFinalize)` | 同上，`data` 与 `finalData` 必须一致这条改由构造保证 |
| `CreateTypedArray` / `GetTypedArrayInfo` | TypedArray（Info.Data 指向视图首元素） |
| `CreateDataView` / `GetDataViewInfo` | DataView |

**别名视图的生命周期**：`[]byte` 只在 JS 对象存活且未 detach 时有效，禁止长期
持有或从其它 goroutine 访问。这条**没有兜底**——两个收集器互相看不见对方持有的
引用：Go 不知道那块内存在 V8 手里，V8 也不知道有个 Go 切片指向它。实测后果：

- 把 Go 切片包成 external buffer 却让 `finalData=nil` 传裸指针，Go 侧一次
  GC + `FreeOSMemory` 之后 V8 再读 → 访问违例（退出码 `0xC0000005`）。
  用 `CreateExternalArrayBufferFromBytes` / `CreateExternalBufferFromBytes`。
- `GetArrayBufferInfo` / `GetTypedArrayInfo` / `GetDataViewInfo` / `GetBufferInfo`
  返回的 `[]byte` 在 JS 对象被回收后再读 → Go runtime 致命错误（退出码 2）。
  需要跨回调用，就持有 `napi_ref` 并在每次需要时重新读取。

**外部缓冲区的 finalizer 约定**：Node-API 强制把 `finalize_data` 设成外部裸指针，
所以 Go 侧要保留的东西（通常是 Backing 切片和 `FinalizeFunc`）走 `finalize_hint`
槽位。把 `finalData` 传成后备切片即可在其被回收前钉住它——**这正是最容易漏的
一步**，漏了就是上面的 `0xC0000005`，所以 Go 堆内存请用 `...FromBytes` 变体，
它把这一步变成构造的一部分。两个参数都为 nil 时不注册 finalizer
（纯借出内存，由调用方保证生命周期）。

## 清理钩子

| 函数 | 说明 |
|---|---|
| `AddEnvCleanupHook(env, fn)` | 环境关闭时执行（fire-and-forget） |
| `AddEnvCleanupHookRemovable` / `RemoveEnvCleanupHook` | 可移除版本（token） |
| `AddAsyncCleanupHook` / `RemoveAsyncCleanupHook` | 异步版（可能在其它线程执行）；**回调里必须调 `RemoveAsyncCleanupHook(handle)`**，否则 teardown 永久挂起；重复移除返回错误而非崩溃 |

## 调试

| 函数 | 说明 |
|---|---|
| `DebugStats() (allocated, freed uint64)` | 回调盒子计数（outstanding = allocated - freed，压测泄漏断言用） |

## 测试覆盖

`example/test.mjs` 的 71 项断言逐个走通上表列出的全部函数，只有
`FatalError` / `FatalException` 例外：它们按设计终止进程
（`napi_fatal_error` 打印后 abort），无法与其它断言共享一次运行。

## entry 包

| 函数 | 说明 |
|---|---|
| `entry.Export(name string, cb napi.Callback)` | 注册函数导出 |
| `entry.ExportValue(name string, factory func(napi.Env) (napi.Value, error))` | 注册任意值（类、常量对象） |

两者都在 `init()` 里调用；模块加载时自动执行注册。

## js 包（syscall/js 风格）

失败一律 panic（回调蹦床会 recover 并转成 JS 异常）。

| API | 说明 |
|---|---|
| `js.Wrap(env, napi.Value)` | 包装原生值 |
| `js.ValueOf(env, x)` | Go → JS：nil/bool/各类数值/string/[]any/map[string]any/[]string/func(js.Value,[]js.Value) any |
| `js.FuncOf(env, fn)` | 创建 JS 函数 |
| `v.Get/Set/Delete/Keys/Index/Len` | 属性与元素 |
| `v.Call(name, args...)` / `v.Invoke(args...)` / `v.New(args...)` | 调用（Call 的 this 是 v 本身） |
| `v.Bool/Float/Int/Int64/String` | 类型提取 |
| `v.MakeRef()` → `Ref.Value()/Release()` | 跨回调持有 |
| `js.Undefined/Null/Global(env)` | 单例 |
