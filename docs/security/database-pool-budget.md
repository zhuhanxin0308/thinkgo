# SQL 连接池预算

`max_open_conns` 决定一个 SQL 连接池的最大打开连接数。新增可选 `max_open_conns_limit`，用于在部署调整容量时保留独立的校验上限；超界返回可通过 `errors.Is(err, db.ErrInvalidDatabaseConfig)` 识别的错误，不自动钳制，也不会把预算当作所需连接数。

## 配置和默认值

在 `config/database.json` 的对应 `connections` 条目中设置，例如：

```json
{
  "default": "primary",
  "connections": {
    "primary": {
      "type": "mysql",
      "hostname": "database.internal",
      "database": "app",
      "max_open_conns": 60,
      "max_idle_conns": 10,
      "max_open_conns_limit": 80
    }
  }
}
```

这段示例的实际容量是 60，不是 80；61 至 80 的配置仍可被接受，81 被拒绝。数值只是演示，不是所有业务的安全阈值。凭据由已有环境配置或密钥管理机制提供，不应写进版本控制。

该预算只接受非负整数；缺省或 0 表示不增加额外校验，不改变既有容量、空闲连接、时长或启动策略。低层 `db.Config` 对应字段为 `MaxOpenConnsLimit`，按值复制。它不意味着直接调用 `database/sql.SetMaxOpenConns(0)`；框架既有零值仍回退到自身默认容量。

校验覆盖解析后的实际值：普通 SQL 池省略 `max_open_conns` 时仍使用 25，预算 24 会失败，而不是静默变成 24。`:memory:` SQLite 的既有单连接、不轮换策略保持不变，因此预算 1 可以通过。

内置 MySQL、PostgreSQL、SQL Server、SQLite 和启用 oracle 构建标签的 Oracle 支持该配置。MongoDB、Neo4j 及自定义类型显式设置正预算会被拒绝，不能把尚未接入的原生连接池描述成受此预算保护。它们在预算缺省或 0 时保持原语义。

## 拒绝时机

显式连接数超界在 `db.Config.Validate`／应用配置解析时被拒绝；当连接数由连接器回退到默认值时，在解析实际连接池参数时校验。默认 lazy 策略可能在第一次取得数据库连接时才发现后一类错误；必须处理连接解析错误，不能声称每种配置都会在应用启动时失败。

普通 SQL 打开路径在 `sql.Open` 前完成池参数解析；已有句柄的校验路径在 Ping 前检查预算，失败时关闭句柄；SQLite 文件路径在创建文件前检查。预算不安装后台协程、不主动连满池、不影响正常请求的查询语义，也不改变 required／degraded／lazy／disabled 的故障处理策略。

## 多实例和运维边界

这是每个池的配置护栏，不是全数据库的分布式配额。应按目标数据库分别计算：最大同时运行实例数 × 每实例所有相关连接池的预算，再加后台任务、迁移、管理和故障恢复预留；滚动发布重叠节点也需要计入。没有验证实际实例数、其他应用连接或数据库端配额，不能据此认定总容量安全。

预算与请求容量若都被同一次错误配置提高，框架无法判断操作员意图。应独立管理预算、评审变化，结合数据库侧角色连接限制与监控。持有低层 `*sql.DB` 的代码可直接改写池配置，绕过连接器检查；此能力不是安全沙箱。

时长参数保留已有非负／转换溢出检查，本项不新增统一业务时长上限。限制连接数也可能使查询排队；业务仍应使用上下文截止时间，避免持有全部连接后等待另一连接。参见 [Go 连接管理指南](https://go.dev/doc/database/manage-connections) 和 [database/sql 文档](https://pkg.go.dev/database/sql)。

## 回归范围

测试覆盖严格配置类型、显式与默认容量超界、不支持的驱动、复制隔离、SQL 驱动创建／Ping 前拒绝、无 SQLite 文件副作用、真实 SQLite 文件和内存库的数据读写及实际池容量。运行真实 SQLite 的测试由 `cgo` 标签控制；其余配置和拒绝路径可在无 CGO 模式运行。测试驱动的计数结果不等于远程数据库连接风暴演练。
