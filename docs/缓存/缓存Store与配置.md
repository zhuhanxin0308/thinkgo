# 缓存 Store 与配置

应用缓存配置位于 `config/cache.json`：

```json
{
    "default": "file",
    "stores": {
        "file": {
            "type": "file",
            "path": "./runtime/cache"
        },
        "memory": {
            "type": "memory",
            "max_entries": 10000
        }
    }
}
```

## 默认 Store 与具名 Store

`default` 必须指向 `stores` 中已经定义的合法名称。应用初始化后，`app.Cache()` 返回默认 store 的视图；也可以取得其它 store：

```go
applicationCache := app.Cache()
memory, err := applicationCache.Store("memory")
if err != nil {
	return err
}
```

例如上面的默认配置为 `file`，`app.Cache()`、`app.Cache().Store("default")` 与 `app.Cache().Store("file")` 都访问同一实际 store。它们共享业务值、正反向标签索引、失效代际与水位、标签变更锁和业务锁；在驱动支持范围内，从一个别名写入的值，可以由另一个别名执行 `Forget`、`ClearPrefix`、`Flush` 或标签 `Flush`。这些持久状态的身份不随别名或注册顺序改变。

Store 名称必须匹配 `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`，配置最多 64 个 store。未知名称返回 `ErrCacheStoreNotFound`，不会静默回退默认 store。

运行期注册：

```go
if err := applicationCache.RegisterStore("secondary", driver.NewMemory()); err != nil {
	return err
}
```

同名 store 已绑定其它驱动时返回 `ErrCacheStoreExists`；再次注册同一可识别驱动实例是幂等的。缓存关闭后不能再注册或取得 store。

在同一缓存管理器内，复用同一个指针形式的 Driver 实例注册多个名称，会让这些别名的 `Remember` 按同一实际 store、同一业务键合并加载。不同管理器各有自己的进程内加载状态；需要跨管理器协调时，使用驱动支持的 [RememberWithLock 与缓存锁](缓存锁.md)。

`ConfigureStore` 仍按注册名称设置该视图的默认 TTL；给别名配置不同的 `Expire` 不会分隔它们的数据、标签或锁。需要独立 store 时，应使用独立后端，或在注册前通过 `NewNamespaceDriver` 配置互不重叠的业务键空间。不同名称本身不提供物理隔离；两个不同驱动对象若报告相同的后端资源身份，注册会返回 `ErrCacheStoreExists`，应复用同一实例来声明别名。

值类型的自定义 Driver 仍可作为单个 store 使用。框架不会通过值相等、深比较或服务地址猜测它与另一个注册值是否共享实例，也不保证这类值在多个名称之间合并 `Remember`。需要自动别名合并时，应复用同一个指针实例；为同一后端另外创建对象或包装器，不等同于复用该实例。

## 应用配置驱动

当前应用配置工厂支持：

| `type` | 必要配置 | 说明 |
|---|---|---|
| `memory` | 可选 `max_entries`；严格模式完整配置 `max_entries`、`max_value_bytes`、`max_bytes`、`max_locks` | 进程内缓存；单独条目限制保留 FIFO，严格预算超界拒绝 |
| `file` | `path` | 应用根目录内的文件缓存目录，或合法绝对路径 |
| `redis` | Redis 配置字段 | JSON 值、原子计数和分布式锁 |

`memory.max_entries` 达到上限时，普通业务项仍按 FIFO 淘汰；标签缓存的内部元数据不会被淘汰。若没有可安全淘汰的普通项，写入返回 `driver.ErrMemoryCapacityExhausted`，业务应保留原有缓存降级路径。标签场景需要额外预留元数据容量；未配置或设为 `0` 时保持无限容量兼容行为。

来自外部输入的生产内存缓存应完整设置四个正预算和业务 TTL；严格模式包含 fencing 与锁的资源，不按 FIFO 淘汰同步状态。值支持范围、计费规则、快照峰值和默认兼容边界见[内存缓存资源预算](../security/memory-cache-budget.md)。

未知字段、未知驱动、默认 store 缺失、文件路径越界和 Redis 类型错误会在启动阶段记录为 `StartupError`。初始化失败时应用会安装一个没有隐式驱动的缓存管理器；后续调用返回 `ErrCacheDriverNotConfigured`，不会静默换成文件缓存。

框架 `cache/driver/database` 包的 `DB` 可以直接构造用于数据库缓存，但当前应用配置工厂不接受 `type: "db"`；需要 DB 驱动时应在应用服务的 `Register` 或 `Boot` 阶段显式创建并注册。

## 升级已有持久缓存

旧版本按视图名称分别保存标签和失效代际。此前通过非 `default` 的具名视图写入或清理过的持久 store，需要在升级时迁移；这包括默认 store 的具名别名，也包括其它具名 store。旧业务值携带的来源代际不包含原视图名称，不能把多个独立序列直接合并后继续比较。

迁移前先停止所有使用受影响 store 的旧进程、后台回填和写入，确认没有仍能提交旧值的写者。随后为全部新实例统一切换到新的专用 namespace、Redis 前缀或文件缓存目录；也可以在停写窗口内，使用后端管理能力离线清理该 store 明确拥有的完整键空间。清理范围包括业务值、正反向标签索引、代际序列、全局及范围失效水位和锁，完成后再启动新版本并重新填充缓存。共享后端中的其它应用数据不属于清理范围。

新版本的 `Cache.Flush` 仍保留代际比较，以防旧失效操作删除并发新值；它不能保证清除属于旧独立序列的所有记录，不能单独作为上述迁移步骤。旧、新版本也不应在受影响的同一旧键空间内混合运行。进程内 Memory store 在相关进程全部重启并创建新驱动实例后自然为空；缓存型 Session 位于迁移范围内时，需要同时安排会话失效和重新登录。

本次统一身份不改变 namespace v2 的物理编码，不会自动读取、合并或重编号旧具名域的控制元数据。此前始终只使用 `default` 视图的控制域保留原有身份；若无法确认旧缓存的使用方式，应按受影响的持久 store 执行迁移。
