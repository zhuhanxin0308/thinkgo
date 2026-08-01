# ORM Model、关系与 NoSQL 设计

## 1. 范围

覆盖 M-01、M-02、M-03、NQ-02、NQ-03、NQ-04、NQ-05、NQ-06、NQ-07，并承接统一 `InsertGetId` 和详细结果契约。

## 2. Model 保存与主键回填

`Model.Save(v)` 在主键为零值时调用 `Create`，否则调用 `Update`。`Create` 在发出 INSERT 前完成：

- 模型必须是可写指针；
- 主键字段存在、可设置；
- 当前零值和目标驱动可能返回的 ID 类型可转换且不溢出；
- 自动时间戳、软删除默认值和 hook 生成字段可写。

随后使用 `InsertGetId` 获取真实 ID。回填 codec 支持经过验证的整数、字符串以及 MongoDB 主键路径的 ObjectID；不对普通业务字段做隐式 ObjectID 转换。任何运行时仍无法回填的异常都必须有明确 partial-success 防护：能用事务的路径回滚，不能原子回滚的后端返回可识别的 partial-success error 和结构化结果。

## 3. Hook 数据所有权

before/after 事件各自收到深度隔离的数据快照，嵌套 map、slice、byte slice 和 pointer-backed 可变值不能与模型或其他 hook 共享可写内存。

before 事件看到即将写入的数据，可按现有事件契约决定是否修改；after 事件看到最终持久化数据，包含自动时间戳、软删除字段、主键和框架实际写入的值。一个 hook 的变更不能意外污染其他注册器持有的历史 payload。

## 4. eager-load 关系键

扫描数据库行时保留一份只供 ORM 内部使用的 raw relation key sidecar。Getter 可以改变对外字段展示，但 has-one、has-many、belongs-to、many-to-many 的分组和关联都使用数据库原始键。

raw key 不暴露到 JSON/用户模型，也不被 hook 修改。canonicalization 必须保留类型边界并拒绝 nil、NaN/Inf 和不稳定键；批量构建索引，避免每个父行重复反射或字符串化。

## 5. MongoDB ObjectID 与投影

ThinkORM 的行为作为参照：ObjectID 从数据库输出时可以字符串化供模型使用，按主键查询/更新/删除时再对合法 24 位 hex 主键对称转换为 ObjectID。

转换严格限制在已声明主键字段路径；普通字符串字段即使形似 ObjectID 也不转换。无效主键文本返回类型错误或按明确查询契约得到零匹配，不能悄悄改变过滤器类型。

显式字段投影未请求 `_id` 时默认排除 `_id`；`*` 或显式请求 `_id` 时保留。自定义主键映射文档必须说明 `_id` 与业务字段关系。

## 6. Neo4j 删除、事务与连接验证

`Delete` 默认使用严格删除，目标节点仍有关联时让 Neo4j 返回错误；新增名字明确的 `DetachDelete`/等价 API 才执行 `DETACH DELETE`。`DeleteResult` 区分节点和关系副作用，简化 `Delete` 只返回节点数。

读写操作迁移到驱动 managed transaction API（如 `ExecuteRead`/`ExecuteWrite`），让驱动对可重试瞬时错误执行官方支持的重试。回调必须是幂等的数据库操作单元，框架 hook 不得在一次重试中重复触发不可逆外部副作用；事件在事务最终成功后触发一次。

Connect 不只调用集群级 connectivity 检查，还要以配置的目标 database 打开 session 并执行轻量查询，确认数据库存在且当前身份有权访问。

属性键规则在 create/update/filter/order/read 全部一致：默认只接受单段合法属性名，点分路径若未来支持必须引入 typed path，不能只在写路径放行。

## 7. TDD 与真实服务边界

- Model Create 在不可写/不兼容主键时保证 Insert 调用次数为零；成功时回填真实整数、字符串 ID；
- Save 的零值/非零值分支、自动字段和软删除行为；
- hook nested payload 深拷贝和最终字段测试；
- 四类 eager relation 在 getter 改写 key 后仍正确关联；
- Mongo mock I/O 固定 ObjectID 往返与 `_id` 投影，真实 Mongo 可用时验证 insert/find/update/delete 全链路；
- Neo 严格删除与显式 detach 生成不同查询，managed transaction 重试只触发一次外部事件，目标库验证失败可定位；
- Neo 属性键在所有入口使用同一验证器；
- Mongo/Neo 实服不可用时，审计状态保留 live-server 边界。

本阶段完成时同步更新模型、MongoDB 与 Neo4j 文档以及迁移指南。
