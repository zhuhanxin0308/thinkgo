# Remember 与计数

## Remember

```go
applicationCache, err := framework.ResolveServiceAs[*cache.Cache](app, framework.ServiceCache)
if err != nil {
	return err
}
value, err := applicationCache.Remember("user:1", 10*time.Minute, func() (interface{}, error) {
	return loadUser(ctx, 1)
})
```

`Remember` 先读取缓存；命中（包括命中 nil）时不调用回调。未命中时，在同一进程、同一缓存管理器状态、同一 store 和同一 key 范围内合并并发加载：只有一个调用执行回调，其它调用等待并复用结果。

回调成功后才写入缓存。回调返回错误或写入失败时不会返回伪造的缓存成功；没有回调返回 `ErrNilRememberCallback`。回调发生 panic 时，等待者会收到可识别错误，执行回调的调用会重新抛出原 panic；合并状态会被清理，不会永久阻塞后续调用。

并发合并只在单进程内生效，不等价于 Redis 或数据库分布式锁。跨进程防击穿需要显式使用[缓存锁](缓存锁.md)。

## RememberWithLock

需要跨管理器或跨进程合并加载时，可以显式使用驱动锁：

```go
value, err := applicationCache.RememberWithLock("user:1", 10*time.Minute, 30*time.Second, func() (interface{}, error) {
	return loadUser(ctx, 1)
})
```

该 API 会在未命中后等待并获取 `Lock`，获得锁后再次检查缓存，再执行回调并写入结果。锁等待有界；无法及时获取返回 `ErrCacheLockBusy`，临界区结束时锁已过期或被替换返回 `ErrCacheLockLost`。Memory、File、Redis 和 DB 都支持该能力；自定义驱动必须同时实现 `DistributedLocker` 和 `LockRenewer`，支持续租的驱动会在回调和写入期间自动续租，仅实现基础锁接口的驱动会在执行回调前返回 `ErrCacheLockUnsupported`。

## 计数

```go
count, err := applicationCache.Inc("login:count", 1)
count, err = applicationCache.Dec("login:count", 1)
```

计数值必须能无损转换成 `int64`：整数类型、精确整数浮点和 JSON 整数可以使用；字符串、布尔、小数、NaN、无穷和超出范围的值会返回 `ErrInvalidCounterValue`。步长不能为负数，结果超出 `int64` 范围返回 `ErrCounterOverflow`。

各驱动会尽可能保留已有 TTL。Redis 使用原子 `INCRBY`/`DECRBY`；Memory 和 File 在驱动锁内完成读取、计算、写入；DB 使用缓存表级条件租约锁覆盖跨进程读改写，并在 upsert 时兼容数据库的影响行数差异。
