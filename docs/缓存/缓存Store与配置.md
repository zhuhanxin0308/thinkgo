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

Store 名称必须匹配 `[A-Za-z0-9][A-Za-z0-9_.-]{0,63}`，配置最多 64 个 store。未知名称返回 `ErrCacheStoreNotFound`，不会静默回退默认 store。

运行期注册：

```go
if err := applicationCache.RegisterStore("secondary", driver.NewMemory()); err != nil {
	return err
}
```

同名 store 已绑定其它驱动时返回 `ErrCacheStoreExists`；再次注册同一可识别驱动实例是幂等的。缓存关闭后不能再注册或取得 store。

## 应用配置驱动

当前应用配置工厂支持：

| `type` | 必要配置 | 说明 |
|---|---|---|
| `memory` | 无；可选 `max_entries` | 进程内动态值缓存；设置容量后按 FIFO 淘汰最早条目 |
| `file` | `path` | 应用根目录内的文件缓存目录，或合法绝对路径 |
| `redis` | Redis 配置字段 | JSON 值、原子计数和分布式锁 |

`memory.max_entries` 达到上限时，普通业务项仍按 FIFO 淘汰；标签缓存的内部元数据不会被淘汰。若没有可安全淘汰的普通项，写入返回 `driver.ErrMemoryCapacityExhausted`，业务应保留原有缓存降级路径。标签场景需要额外预留元数据容量；未配置或设为 `0` 时保持无限容量兼容行为。

未知字段、未知驱动、默认 store 缺失、文件路径越界和 Redis 类型错误会在启动阶段记录为 `StartupError`。初始化失败时应用会安装一个没有隐式驱动的缓存管理器；后续调用返回 `ErrCacheDriverNotConfigured`，不会静默换成文件缓存。

`framework/cache/driver/database.DB` 可以直接构造用于数据库缓存，但当前应用配置工厂不接受 `type: "db"`；需要 DB 驱动时应在应用服务的 `Register` 或 `Boot` 阶段显式创建并注册。
