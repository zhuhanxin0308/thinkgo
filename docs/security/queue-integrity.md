# 队列任务认证与重放边界

普通 `asynq.NewProducer` / `NewWorker` / `NewScheduler` 保留既有协议，不进行来源认证。敏感任务应使用 `NewSignedProducer` 和 `NewVerifiedWorker`，部署独立可信的 `ReplayStore`，并为签名队列配置 Redis 网络隔离、最小 ACL、分离凭据与租户隔离。拥有任务 Redis 写权限者仍能删除、延迟、提前投递或耗尽合法任务的执行预算；签名不提供可用性保证。

## 配置和迁移

```go
// key 从密钥管理取得至少 32 字节的随机密钥，不能放在任务 Redis 中。
signing, err := asynq.NewSigningPolicy(asynq.SigningConfig{
    KeyID: "2026-10", Key: key, Audience: "billing.production",
    ValidFor: 48 * time.Hour,
})
if err != nil { return err }
verification, err := asynq.NewVerificationPolicy(asynq.VerificationConfig{
    Keys: map[string][]byte{"2026-10": key}, Audience: "billing.production",
    MaxValidFor: 48 * time.Hour, ClockSkew: 5 * time.Second,
    Replay: trustedReplayStore,
})
if err != nil { return err }
producer, err := asynq.NewSignedProducer(redisConfig, signing)
if err != nil { return err }
defer producer.Close()
worker, err := asynq.NewVerifiedWorker(redisConfig, workerConfig, verification)
if err != nil { return err }
defer worker.Shutdown()
return worker.Start(handler)
```

复用 Redis 客户端时使用相应的 `FromRedisClient` 构造器，客户端仍由调用方关闭。所有密钥和配置均在构造时深拷贝；策略可并发复用，`ReplayStore` 必须支持多进程并发。

将签名队列与旧队列分离，先部署包含当前和新 key-id 的 verifier，再切换 signer，等旧任务有效期、计划与重试窗口结束后移除旧密钥。切换 `NewVerifiedWorker` 后，无签名的历史任务直接拒绝，不会回退到可信模式。策略本身不支持运行中改写；轮换创建新策略并按生命周期替换生产者/工作进程。

## 协议和执行限制

版本 1 使用 HMAC-SHA256 和固定域 `thinkgo/asynq/task/v1`。签名覆盖版本、key-id、应用/环境 audience、随机 256 位任务 ID、任务类型、payload 的 SHA-256、全部原始传播头、队列、签发/最早执行/到期纳秒时间、重试预算和执行超时。标准 JSON 的固定结构字段顺序与字符串 map 键排序构成该版本的规范编码；验签使用常量时间 MAC 比较。

签名信封保存在保留头 `thinkgo-integrity-v1` 中，业务 payload 保持原字节，所以 `UniqueFor` 仍按 Asynq 原来的队列/类型/payload 去重。调用方不能使用保留头，验签后该头会在调用业务处理器前删除。验签前检查 payload、传播头和信封大小及语法，不对无界原始头做规范化编码。

Worker 验签后将实际 Asynq 任务 ID、队列和最大重试次数与签名绑定。Redis 的当前 retry counter 仅做合法范围检查，不能决定可信执行次数。任务执行上下文另施加签名的 timeout 和绝对 expiry，Redis 修改 timeout 无法延长允许的业务执行时间；同步 Handler 仍须遵守上下文，框架不能中断忽略取消的业务函数。

`ValidFor` 必须覆盖排队、计划执行、全部重试及最终执行。没有自动续签，到期的重试直接拒绝；应用按业务延迟和重试策略设置有效窗口。Asynq 将调度/超时存为整秒，因此签名生产者将带小数的 `ProcessAt` 向上取整到下一秒，按调整后的计划校验有效期；原生 timeout 向上取整，Verifier 仍强制原始纳秒 timeout。`ClockSkew` 是显式允许的时钟偏差，上限 5 分钟；它也允许相同偏差内的提前执行/晚到任务，使用前应同步主机时钟。

一次计划任务可通过 `SignedProducer.Enqueue` 的 `ProcessAt` 提交。现有 `Scheduler.Register` 产生普通无签名任务，会被 verified Worker 拒绝。周期计划必须由可信调度入口在**每次触发时**调用 signed producer，生成新任务 ID、有效期及签名；不能把一次签名及 ID 注册为永久重复模板。

认证/执行窗口不合法及可信重放拒绝会返回 `SkipRetry`，不会触发业务 Handler。可信存储故障、nil lease 和完成记录故障均不能作为成功处理。Claim 使用签名 expiry 上下文，返回 lease 后再次检查取消/到期；迟到的 Claim 不会进入 Handler，而是完成失败租约。

## ReplayStore 的必需契约

本包提供接口，**不包含可供生产部署的持久化 ReplayStore**。存储必须独立于攻击者可以写入的任务 Redis，且由同一应用的所有 Worker 共享。内存 map 或进程私有锁无法跨重启、跨 Worker 阻止重放。部署实现必须满足以下条件：

- 按 audience/ID 持久化原始身份、到期时间和 `MaxAttempts`，禁止后续请求改变这些限制。删除记录只能在包含 verifier 时钟偏差的 `TaskIdentity.ExpiresAt` 之后进行。
- `Claim` 在一笔原子操作中检查成功终态、当前 owner 和总 attempt 数，消耗一次可信 attempt 并取得独占 owner；上限为签名 `MaxRetry + 1`。不要读取 Redis 的可修改 retry counter 来授权。
- 每次 Claim 生成唯一 owner token；`Finish` 比较 token，原子且幂等地改变该 owner 的状态。旧 owner 的迟到完成不能释放新 owner 或撤销成功终态。
- 成功 `Finish(true)` 永久拒绝该 ID 在有效期内的再次执行。`Finish(false)` 允许在剩余原始预算内重试。Finish 超时或结果不确定时保留排他状态，不能假定已回滚。
- 业务副作用与完成记录须有事务或业务幂等策略。Handler panic 时框架保留已领取 lease，不会自动 Finish/释放；进程崩溃也可能留下占用。运维恢复应验证业务结果并使用 owner fencing，不能仅用租约过期自动重复副作用。

签名任务不能获得通用 exactly-once 保证。即使没有攻击者，副作用成功后进程崩溃、完成记录写入失败和重试都需要业务端消除重复；人工恢复与允许重试是明确的可用性选择。拥有签名密钥的生产者有权生成任务，签名不能代替租户授权或业务验证。

## 验证范围

回归覆盖字段篡改、无签名、ID/队列/重试边界、过期/计划及小数秒、保留头、密钥快照与轮换、原 payload/UniqueFor、成功重放、可信总 attempt 预算、共享 claim 并发排他和 owner fencing；并覆盖 Claim/Finish 故障、panic、上下文取消和迟到 Claim。模拟 Redis 用于任务 wire/Worker 适配器行为，测试内存 ledger 是接口契约 oracle，不能当作实际持久化、多地域故障或崩溃恢复验收。

真实 Redis 门禁 `TestLiveRedisQueueIntegrityWireContract` 检查生产者写入的签名、传播头及任务 ID，实际 Worker 执行、跨 Worker 成功重放拒绝，以及 payload／头／ID 篡改拒绝。工作流先核对测试名称存在，再单独以 `integration`、`race`、`-v`、`-count=1` 运行；未配置真实服务时的本地 SKIP 不能算验收通过。该门禁仍使用独立的测试 ledger，不替部署实现持久化 ReplayStore。
