# 内存缓存资源预算

来自外部输入的生产缓存应显式使用 `driver.NewMemoryWithBudget`，或同时配置下列四个正整数，并按业务要求设置正 `expire`。单独 `max_entries` 只限制条目数量，无法限制一个对象图、锁或 fencing 元数据的大小。

```json
{
  "default": "memory",
  "stores": {
    "memory": {
      "type": "memory",
      "max_entries": 10000,
      "max_value_bytes": 1048576,
      "max_bytes": 67108864,
      "max_locks": 256,
      "expire": 300
    }
  }
}
```

这些数字仅是配置示例，应结合业务负载、标签开销和进程内其他数据调整。四个预算必须完整、为正，`max_bytes` 必须不小于 `max_value_bytes`；缺失、零、负数、非整数或类型错误在构造／应用配置解析时拒绝。

```go
memory, err := driver.NewMemoryWithBudget(driver.MemoryBudget{
    MaxEntries: 10000,
    MaxValueBytes: 1 << 20,
    MaxBytes: 64 << 20,
    MaxLocks: 256,
})
if err != nil {
    return err
}
```

## 准入与错误

严格模式的 `MaxEntries` 计算所有数据记录，包括业务值、标签关系和 fencing 记录。`MaxLocks` 单独计算活跃锁，锁的键和 owner 同样占用 `MaxBytes`。条目或总预算不足返回 `driver.ErrMemoryCapacityExhausted`，不淘汰有效业务值或同步元数据。过期数据与锁先回收；覆盖扣除旧值后检查新值，失败保留驱动中原有有效值。Clear／按前缀清理仍保留单调 fencing 记录，Clear 不释放锁，因此清理后预算不一定归零。

单值图超过 `MaxValueBytes`、超过 64 层确定性遍历深度或超过 65536 个遍历节点返回 `driver.ErrMemoryValueTooLarge`。不可安全计费和复制的值返回 `driver.ErrUnsupportedMemoryValue`；非法构造返回 `driver.ErrInvalidMemoryBudget`。这些错误支持 `errors.Is`。计数、原子更新和租约提交都采用相同准入规则；缓存门面失败时可能已推进 fencing 票据，使旧值变成未命中，业务应保留缓存失败后的降级路径。

## 值图与计费规则

严格模式保留支持值的动态 Go 类型，支持 nil、布尔、数字（不含 uintptr）、字符串、数组、切片、指针、接口和结构体。Map 键限布尔、字符串、整数及无符号整数；浮点、复数、接口或引用键被拒绝。Map 按键的确定全序遍历，预检与复制采用同一顺序，避免共享图因随机遍历而在写入和读取时产生不同的深度判定。

共享／循环的 map、指针和相同视图的 slice 按类型、地址以及 slice 的 len／cap 识别，计费和复制各处理一次，保留私有副本内的别名和循环。不同 slice 视图独立复制，只保留 len，不保留输入 cap。所有字符串、map 键、缓存键和锁 owner 独立复制；即使是指向巨大底层字符串的一小段，也不会在缓存内保留整个输入。

函数、channel、unsafe.Pointer、uintptr 和未导出字段中含引用的结构体被拒绝。未导出的纯内联数字／布尔数据可按值复制；`time.Time` 的隐藏 location 引用也属于拒绝范围，可改用 Unix 时间或 RFC3339 字符串。旧构造器保留既有动态值支持。调用方必须在 Set／更新返回之前拥有输入，不能并发修改源对象图。

计费单位是实现定义的保守配额，计算根动态类型的静态大小、接口内动态类型大小、独立字符串的长度、唯一指针目标大小和 slice 的 len × 元素大小；每个唯一引用容器增加 128，每个 map 元素增加键／值静态大小及 128，每个缓存记录增加 256 和键长度，每个锁增加 128、键长度和 owner 长度。乘法和累加先检查剩余配额，不能以溢出绕过限制。常数是该实现的计费协议，不是 Go 堆分配器的精确测量。

## 副本与进程边界

复制之前先完成单值和总量预检，超预算值不会先分配其缓存副本。每个严格驱动串行执行快照操作：Get／Set 使用一个临时快照，原子更新可以同时拥有回调输入快照和提交快照，最多两个已准入单值图。驱动保留值的计费总量不超过 `MaxBytes`；内部快照图的附加计费量最多为 `2 × MaxValueBytes`，遍历辅助数据由深度和节点限制约束。不同驱动实例分别计算，部署应将它们的预算相加。

这不是进程 RSS 或 Go 堆的硬上限。Go 分配器、map bucket、反射临时对象和 GC 回收时间具有额外开销；调用方已经分配的源对象、更新回调自己的分配以及 Get 返回后由调用方持有的副本均不受驱动控制。外部请求仍须有输入大小限制、并发限制和容器／进程内存预算，不能把缓存准入等同于进程不会 OOM。

## 兼容边界

`NewMemory()`、`Memory` 零值与 `NewMemoryWithMaxEntries(0)` 仍保留无界兼容行为；单独配置 `max_entries` 保留既有 FIFO 策略，不启用字节／锁预算。框架应用默认 store 仍为 file。没有配置 TTL 或显式 TTL=0 仍表示永久值，严格预算不替业务决定数据的新鲜期，也不偷偷改写 TTL。默认构造迁移为严格预算、统一 TTL 的兼容决策并未由本接口替代。

回归覆盖对象图别名／循环、substring 与 slice 容量所有权、命名类型、确定性共享深链、隐藏引用拒绝、复制前尺寸判定、覆盖失败保留值、增减账、Clear 的 fencing 保留、锁与数据跨空间到期回收、并发计数与应用门面。预算测试不代表生产负载的 RSS 测量。
