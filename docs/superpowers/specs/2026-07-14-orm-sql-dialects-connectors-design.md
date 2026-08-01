# ORM SQL 方言与连接器一致性设计

## 1. 范围

覆盖 T3-03、T3-04、T3-05、T3-06、C-01、C-02、C-03，目标是把静默退化改为明确能力，把连接参数和 SQL 生成统一到安全的结构化路径。

## 2. 锁能力

锁模式改用枚举/typed option，不再让任意字符串直接进入 `LockClause`。

- MySQL 只接受受支持白名单并按目标版本生成语法，未知模式返回 `ErrUnsupportedLockMode`；
- PostgreSQL 保留其受支持模式并拒绝未知模式；
- SQLite 对行级悲观锁返回 `ErrUnsupportedFeature`，提示通过事务模式表达写锁，不能返回空字符串假装成功；
- SQL Server 在 table reference 阶段生成白名单 table hints。更新锁映射到经测试的 `UPDLOCK` 组合，共享锁映射到持有语义明确的组合；不把 hint 错放在 SQL 尾部；
- Oracle 只生成经目标版本验证的锁组合，不支持的组合返回明确错误。

## 3. Oracle 分页、锁与命名

普通无锁分页继续使用目标版本支持的 row limiting。分页与 `FOR UPDATE` 组合不能再生成 Oracle 明确禁止的语法：Builder 对简单、可锁查询使用经测试的合法改写；对 DISTINCT/GROUP/聚合/复杂 view 等无法保证可更新性的组合返回 `ErrUnsupportedFeature`。没有真实 Oracle 证据前不声称所有组合可用。

逻辑标识符与显式 quoted identifier 分离：普通未引用的 ThinkGo 逻辑名按 Oracle 默认命名规则规范化后引用，避免把 `users` 意外固定为区分大小写的小写对象；只有显式 identifier API 才保留调用方大小写。schema、table、column、alias 都使用同一规则。

## 4. MySQL 连接参数

连接器使用驱动的结构化 Config 作为唯一 DSN 构造源，不自行拼接用户名、密码和参数。

安全开关校验先规范化 key，再按布尔语义解析所有合法真值，不只比较文本 `true`。高风险开关启用时一律拒绝；重复键、大小写变体、结构字段与 Params 冲突同样拒绝。安全默认值由 typed Config 设置，用户 Params 不能绕过。

go-sql-driver DSN 无法无歧义表示的用户名（包括审计确认的冒号边界）在连接前返回明确配置错误，不生成可能连接成其他身份的 DSN。密码等由驱动 Config 负责格式化。

## 5. SQLite `:memory:` 生命周期

内存库固定为单一物理连接，并设置：

- `MaxOpenConns(1)`；
- `MaxIdleConns(1)`；
- `ConnMaxLifetime(0)`；
- `ConnMaxIdleTime(0)`。

这四项共同保证唯一连接不会因通用池过期策略被替换而丢库。文件 SQLite 继续使用显式配置的池策略。

## 6. 方言错误传播

Builder 生成方法需要能返回 error；调用链不能为了保持旧签名吞掉 capability error。所有 Query 执行入口在接触驱动前完成锁、分页、identifier 和 bind budget 验证。

错误分类至少包含 unsupported feature、unsupported lock mode、invalid identifier、invalid connector config。错误通过 `errors.Is` 可判断，并包含后端名和能力上下文，但不包含敏感 DSN。

## 7. TDD 与真实驱动边界

- 五方言锁矩阵测试：受支持模式生成准确 SQL，不支持模式明确失败；
- SQL Server 验证 hint 位于 table reference；
- Oracle tagged tests 覆盖无锁分页、锁、分页+锁拒绝/合法改写、大小写和 schema；
- MySQL 参数测试覆盖全部合法布尔真值、大小写、重复键、冲突和冒号用户名；本机真实 MySQL 验证安全默认与正常凭据；
- CGO SQLite 测试让连接超过原 lifetime/idle 边界后继续读到原数据；
- PostgreSQL/SQL Server/Oracle 实服不可用时记录未验证矩阵，不用生成 SQL 测试替代服务端兼容结论。

本阶段必须同步更新查询构造器和连接数据库文档中的方言能力表。
