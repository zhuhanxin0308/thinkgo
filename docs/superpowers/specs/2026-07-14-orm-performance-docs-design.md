# ORM 性能验证与文档交付设计

## 1. 原则

审计报告列出的性能项目前只是候选，不因“看起来更快”直接修改生产代码。每项先建立真实工作负载基线和 profile；只有稳定收益且无正确性、race 或跨驱动回归的优化才合入。

## 2. 基准矩阵

至少覆盖：

- `scanRows` 在 1k/10k/100k 行和常见列宽下的 time/op、allocs/op、bytes/op；
- struct metadata、tag 解析和 `structToMap`；
- Getter registry 快照/排序；
- 1k/10k 父模型的 eager relation raw key 建索引与分组；
- `Chunk` OFFSET 与 `ChunkById` keyset 在 10^5/10^6 行的吞吐和尾延迟；
- Mongo cursor 全量物化、skip/regex，以及 Neo session/collect，在真实服务可用时的 10k/100k 数据和并发 1/32/256；
- SQL connector 池参数只在真实并发负载和服务器上限下评估，不统一拍脑袋调参。

SQL 基准至少使用真实 CGO SQLite和本机 MySQL。MySQL 使用独立 benchmark schema/table 和可重复 seed，不污染业务数据；记录数据库版本、关键配置、数据量、并发度和运行命令。

## 3. profile 与合入门槛

先用 CPU、heap/alloc profile 确认候选确实位于热点，再实现优化。每个对比在相同机器、数据库状态和测试参数下多轮交错运行，使用 benchstat 或等价统计比较。

合入需要满足至少一项：

- 关键真实场景的稳定耗时改善达到可测量水平，且置信区间不与零收益混淆；
- allocs/op 或 bytes/op 稳定下降，同时耗时没有实质回退；
- 高并发尾延迟稳定改善，且数据库等待、连接数和错误率没有恶化。

若收益只存在于 microbenchmark、真实场景无收益或结果波动无法区分，则撤销实验代码并在报告中记录“未合入及原因”。

优先候选是 metadata cache、Getter registry 一次快照/排序、relation key 索引和 scan allocation；深 OFFSET、Mongo/Neo 全量物化及连接池调整只在真实 profile 支持时继续。

## 4. 正确性保护

- cache key 必须包含 Go 类型和影响映射的配置，避免跨模型污染；
- metadata cache 只缓存不可变结构，返回值不得被调用方修改；
- slice/pool 复用不能让返回行共享 backing array 或保留超大对象；
- Getter registry 更新与读取必须并发安全，热路径快照不可看到半更新状态；
- 所有优化运行 ORM race、全仓测试和真实数据库正确性对照。

## 5. 文档更新清单

源码修复完成后，逐项更新：

1. `docs/数据库/查询构造器.md`：ThinkPHP 风格 CRUD、`InsertGetId`、详细结果、Raw、安全条件、copy-on-write、锁和方言矩阵；
2. `docs/数据库/模型.md`：Save/Create/Update、主键回填、事件最终数据、关系 raw key；
3. `docs/数据库/连接数据库.md`：lease、关闭所有权、MySQL/SQLite 配置行为和驱动能力；
4. `docs/数据库/事务.md`：context 自动回滚、终态和重入错误；
5. `docs/数据库/MongoDB与Neo4j.md`：ObjectID、projection、严格/DETACH delete、managed transaction、目标库检查；
6. 新增 `docs/数据库/ORM升级迁移指南.md`：旧 API 到新 API 的编译级迁移示例和破坏性变化；
7. 更新 `docs/数据库/ORM源码审计-2026-07-13.md`：为 27 项添加状态、修复提交、测试名和 live-driver 边界；
8. 如 `README.md` 或 `docs/README.md` 有旧示例/索引，同步调整。

审计报告保留原始 finding、证据、严重级别和建议，不把历史内容删除或改写成从未存在；在每项下追加修复状态，使诊断与修复可追溯。

## 6. 文档质量验证

- 代码块中的 Go API 通过示例测试、编译测试或契约测试；
- 全仓搜索旧 `Insert` 返回 ID、旧裸连接、旧 raw where 和静默锁描述，确保无陈旧文档；
- 方言能力表区分“单元测试通过”“真实驱动通过”“尚待 live server”；
- 文档不包含凭据、完整 DSN 或可复用注入 payload；
- 不新增配置文件，基准参数通过测试 flag/环境变量或现有配置注入，不提交本机凭据。

## 7. 最终交付报告

最终报告包含：27 项状态表、API 迁移摘要、各驱动验证矩阵、未具备服务的边界、性能修改前后数据、未合入实验及原因、全部验证命令与结果、已知非 ORM 波动。

完成标准是“源码、测试、文档、审计状态四者一致”，不是仅让测试变绿。
