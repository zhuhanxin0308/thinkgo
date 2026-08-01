# ORM 生命周期、所有权与日志安全设计

## 1. 范围

覆盖 T2-01 至 T2-06：context 自动回滚租约泄漏、驱动错误日志泄密、裸连接租约过早释放、共享连接双重关闭、双引号脱敏缺口、游标比较跨驱动不一致。

## 2. 事务终态状态机

Tx 具有 `active`、`committed`、`rolledBack`、`contextRolledBack` 四类可观察状态。Commit、Rollback 和 context watcher 竞争同一个 finalize-once 状态机；只有赢得终结权的一方调用底层终结动作，连接 lease 只释放一次。

`BeginTx(ctx)` 成功后启动轻量 watcher：

- `ctx.Done()` 先发生时，确认/请求底层 rollback，把 wrapper 转为 context 终态，并立即释放 lease；底层已由 `database/sql` 回滚而返回 `sql.ErrTxDone` 仍视为终结成功；
- 显式 Commit/Rollback 先发生时，停止 watcher，记录真实终态并释放 lease；
- 终结后的任何查询或二次终结返回稳定框架错误，错误链可包含底层错误；
- `DB.Close` 不再需要调用方额外 Rollback 才能结束等待。

状态竞争测试必须覆盖 context cancellation 与 Commit/Rollback 同时发生，并运行 race。

## 3. 连接 lease 与关闭所有权

删除“取出裸 Connection 后立即 release”的行为。公开借用连接采用受控 lease：回调式 `WithConnection`，或实现显式 `Close/Release` 的 `ConnectionLease`。优先使用回调式 API减少忘记释放；需要跨调用持有时才使用显式 lease。

Manager 不再通过多个 DB wrapper 分别拥有同一个底层连接。注册时创建唯一 managed handle，别名只引用同一 handle；handle 自身负责 close-once 和引用/租约计数。不得用不稳定的接口地址或反射猜测连接身份。

验收行为：

- lease 活跃时 `DB.Close` 等待，释放后完成；
- 获取 lease 失败不会增加计数；
- 同一连接的多个别名关闭时底层 `Close` 恰好一次；
- 不同连接全部关闭，错误通过 `errors.Join` 汇总；
- 重复 Close 幂等。

## 4. 日志错误安全

调用方仍收到完整原始 error，便于诊断和 `errors.Is/As`；日志不再直接格式化 `%v` 写入任意驱动错误文本。

日志只记录：框架操作名、后端类别、稳定错误分类、白名单驱动错误码/SQLSTATE、耗时和已经安全化的上下文。未知错误文本记录类型而非内容。panic 回滚失败也走同一安全日志入口。

SQL/参数脱敏按方言 token 化，不用简单正则猜测：

- 单引号字符串、双引号字符串/标识符、反引号、SQL Server 方括号、Oracle quoted identifier 分别处理；
- PostgreSQL dollar quote、注释和转义规则纳入扫描器；
- 双引号在 PostgreSQL/标准 SQL 默认是标识符，但在可配置模式下可能是字符串，日志策略选择保守脱敏；
- 绝不把完整 DSN、密码、token、文档字段值或驱动回显值写入日志。

## 5. 游标和排序一致性

`ChunkById` 不再在 Go 中用通用字符串/字节比较来推导数据库顺序。支持的游标键限制为具有稳定全序且能按数据库值无损比较的类型，如整数、时间和明确编码的标识符。

文本/二进制键只有在调用方显式选择可证明一致的 cursor codec/collation 策略时才允许；否则返回 `ErrUnsupportedCursorKey`，建议使用数值主键或自定义游标。查询翻页仍由数据库 `WHERE key > ? ORDER BY key` 决定，Go 端只做进度和重复保护，不重新定义 collation。

## 6. TDD 与验收

- context 自动回滚后不调用二次清理也能关闭 DB；高并发取消/提交无 race、无双释放；
- lease 生命周期和 alias close-once 使用记录连接验证；
- 驱动错误包含敏感 sentinel 时，返回 error 保留 sentinel，日志完全不含 sentinel；
- 各方言字符串、标识符、注释和 dollar quote 的脱敏 golden tests；
- 数值游标成功，未知文本 collation 被明确拒绝，自定义 codec 可通过；
- 本阶段运行 `go test -race ./framework/db/...` 和 `go vet ./framework/db/...`。
