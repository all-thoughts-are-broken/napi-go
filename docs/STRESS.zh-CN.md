# 压力测试报告

参考环境：Windows x64 · Node.js 24 · Go 1.27 · MinGW-w64 gcc 14.2.0 (UCRT)。

[English](STRESS.md) · **简体中文**

## 运行方法

```sh
go build -buildmode=c-shared -o stress/stress.node ./stress
node --expose-gc stress/run_all.mjs          # 全部 18 个场景
node --expose-gc stress/run_all.mjs soak     # 单场景过滤
```

`--expose-gc` 让驱动做精确的 GC 后内存断言。任何断言失败 → 退出码非 0。

## 覆盖矩阵（18 场景，最近一轮全部通过）

| # | 场景 | 负载 | 通过标准 | 实测 |
|---|---|---|---|---|
| 1 | 调用吞吐 | 500 万次 addDoubles | 数值正确、Go 堆零增长 | 0.58M calls/s，goheap +0.0MB |
| 2 | 值抖动（Go 侧） | 300 轮 × 1 万组合值 | Go 堆回落 | goheap +0.0MB |
| 3 | 值抖动（经 JS） | 100 万对象逐个建/丢 | 字段正确、内存回落 | rss -0.3MB |
| 4 | Wrap 抖动 | 20 万带 finalizer 对象 | finalizer 全部触发、盒子数回落基线 | 200000/200000 |
| 5 | 函数抖动 | 10 万 Go 后端 JS 函数 | GC 后盒子数回落基线 | 通过 |
| 6 | TSFN 洪泛 | 8 goroutine × 2.5 万 | 恰好送达一次、无丢重、盒子回落 | 200k/200k，1.7s |
| 7 | TSFN 队满 | 队列 8 + 10 万 NonBlocking | napi_queue_full 被安全拒绝；送达数==入队数 | 16139 入队全部送达 |
| 8 | panic 风暴 | 2 万次 Go panic | 全部转 JS 异常、进程存活 | 通过 |
| 9 | 字符串边界 | utf8/utf16、15/16/255/4096 字节边界、emoji、CJK、内嵌 NUL、1MiB×100 | 往返全等 | 通过 |
| 10 | BigInt 往返 | 64~576 位、正负 × 2000 | 全等 | 通过 |
| 11 | Buffer 压力 | 2 万 × 4KB + 单个 64MB | 内容校验和正确 | 通过 |
| 12 | Async work 洪泛 | 5000 个并发任务 | 全部 resolve、索引完整 | 通过 |
| 13 | 引用抖动 | 20 万 create/get/delete | 无错 | 通过 |
| 14 | 弱引用 | 2 万对象 + gc | gc 后 0 存活（信息性） | 0/20000 |
| 15 | 深度重入 | JS↔Go 200 层 × 100 | 计数正确、无栈溢出 | 通过 |
| 16 | worker_threads | 4 worker × (50 万调用 + 5 万对象) | 多环境共享一个 Go runtime | 通过 |
| 17 | Soak 混合负载 | 15 秒混合：调用+抖动+wrap+TSFN | Go 堆四分位漂移 <120MB | -2.7MB |
| 18 | 终局记账 | 注册表回到模块加载基线 | outstanding == baseline | 通过 |

另：`go test -race ./napi/`（32 goroutine × 5000 盒子并发往返）通过；
整轮套件连跑 3 次结果一致。

## TSAN 说明

`go build -race` 的 DLL 可以构建，但 **TSAN 无法在 V8 宿主进程内运行**
（影子内存映射与 V8 保留地址冲突，`ThreadSanitizer failed to allocate`）。
因此并发覆盖拆成两层：

- 纯 Go 并发核心（cgo 句柄盒注册表）：`go test -race` 真实覆盖；
- cgo→Node-API 调用面：按契约单线程（JS 线程），由 18 个压测场景做行为
  正确性覆盖。

## 压测发现并修复的问题

1. **TSFN 派发的 NULL env**：node 在关闭 TSFN 且队列有积压时，以 NULL env
   调用 call_js_cb（仅供释放数据）。原实现直接使用 env → invalid_arg。
   修复：env/data 判空；同时移除了冗余的 handle scope（node 的 DispatchOne
   已自带）。
2. **事件循环保活**：Unref 的 TSFN 不维持事件循环，大规模派发中若主线程
   无其它句柄，node 会提前退出（"unsettled top-level await"）。文档化为
   Ref/Unref 语义。
3. **回调值生命周期（测试端实证）**：闭包捕获回调参数的 `napi.Value` 跨
   回调使用 → handle 已死。演示代码改为强引用 + 宿主 finalizer 释放。
4. **强引用环**：Go 强引用 + JS 闭包捕获（ref→jsLevel→goStep→finalizer）
   互锁导致 finalizer 永不触发。文档化打破环的方式。

前两条是绑定修复，后两条是文档 + 示例修复——都是社区使用必然踩到的坑。

## 环境要求与可复现性

阈值刻意宽松（如 soak Go 堆漂移 <120MB）以避免 CI 抖动；上表数值来自一次
参考运行。弱引用场景是信息性的（GC 时机不作硬保证）。
