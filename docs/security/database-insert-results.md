# 插入结果、主键读取失败与重试边界

关联 issue #75。`Insert` 返回影响行数，`InsertGetId` 返回驱动报告的主键，`Model.Create` 将主键回填到可写结构体；这些接口分工不变。`InsertGetId` 自身会执行插入，不能在一次 `Insert` 后调用它来查询上一条记录的 ID。

## 已执行 SQL 不等于成功取得主键

在支持 `LastInsertId` 的 SQL 路径中，可能出现 `Exec` 成功、`RowsAffected` 成功，但 `LastInsertId` 失败。此时框架返回 `*db.PartialWriteError`，可通过 `errors.Is(err, db.ErrPartialWrite)` 与 `errors.As` 识别；原始驱动原因保留在 `Cause` 中。

`Result.Affected` 保留已知影响行数；`Result.IDKnown` 为 false，`Result.ID` 为 nil。即使驱动同时返回了一个非零数值和错误，也不会把这个数值当作可信主键。`Result.Data` 保留本次提交给驱动的规范化数据快照，并非数据库触发器、默认值处理后的完整行回读结果。

不能根据 `err != nil` 或模型 ID 仍为零，就断言数据库中没有新增记录。也不能以 `SELECT MAX(id)`、再次插入或未固定连接的会话查询猜测主键。没有 `ErrPartialWrite` 同样不等于绝对未写入：执行失败、连接中断等场景需结合数据库和事务语义判断。

## 调用方必须区分三个边界

1. **非事务调用**：记录可能已经持久化。先根据业务唯一键或幂等标识对账，不直接重试新增。
2. **显式事务**：部分写入错误只说明语句执行与结果处理分离，不说明事务已提交。事务所有者仍须明确提交或回滚，并处理该操作自身的错误。
3. **闭包事务**：将原始错误从 `TransactionContext` 回调返回，会走原有回滚路径。回滚后的返回错误仍可能含 `PartialWriteError`，不能反过来理解为记录已经提交。

通过 `NewModelFor` 绑定的记录收到部分写入错误后，沿用既有 uncertain 状态保护，后续 `Save` 或 `Create` 不会再次发出 INSERT。该保护不覆盖任意新建模型、重新构造的对象或未绑定的普通结构体；业务仍须完成对账或确认回滚，不能通过重建对象绕过结果核对。

## 内部处理与公共响应分开

服务层先使用原始错误处理事务及对账。`errors.As` 取得的 `PartialWriteError.Result` 和 `Cause` 可能包含业务值、SQL 或驱动细节，不应直接写入公共响应或未经脱敏的日志。选择对外响应时可以显式调用 `db.SanitizeError`，但不能用转换后的公共错误判断是否写入、是否提交或是否可以重试。

本修复不新增自动查询、重试、提交或回滚，不更改 `Insert` 的返回值含义；也不将 `Exec`、`RowsAffected` 或 `RETURNING` 扫描失败统一标成“确定写入成功”。这些路径需要各自的结果证据，不能借用本修复的保证。

## 回归验证

```sh
go test -race -shuffle=on -count=20 -run '^TestInsertIDFailure' ./db
CGO_ENABLED=0 go test -shuffle=on -count=20 -run '^TestInsertIDFailure' ./db
```

可移植测试驱动只控制执行结果与元数据错误，覆盖直接连接、Query、模型、显式事务、闭包回滚、输入快照和绑定记录重复保存。启用 CGO 时，SQLite 回归使用真实引擎执行 SQL，仅包装 `LastInsertId` 注入错误，检查自动提交和显式提交后记录为一条、显式或闭包回滚后为零条。它不是对 MySQL 或其他远程数据库故障行为的证明。
