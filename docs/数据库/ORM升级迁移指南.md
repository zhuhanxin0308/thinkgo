# ORM 升级迁移指南

本次 ORM 修复采用一次性破坏性升级，不保留双轨兼容层。目标是让业务侧写法尽量贴近 ThinkPHP 8，同时让 Go 的返回值、连接生命周期和错误边界保持类型安全。升级不要求新增或修改配置文件。

## 写入返回值

### Insert 与 InsertGetId

`Insert` 现在只返回影响行数；需要真实主键时必须显式调用 `InsertGetId`：

```go
// 旧：把 Insert 返回值当主键。
id, err := query.Insert(data)

// 新：只关心插入数量。
affected, err := query.Insert(data)

// 新：需要数据库/驱动返回的真实主键。
id, err := query.InsertGetId(data)
```

MongoDB 返回真实 `InsertedID`，Neo4j 只在写入数据带应用主键时提供 ID；驱动不能证明主键时返回 `ErrInsertIDUnavailable`，不会用影响数 `1` 冒充 ID。

### Save

Query 的 `Save` 与 ThinkPHP 风格一致：没有 WHERE 时新增，有 WHERE 时更新；`Save(data, true)` 强制新增。

```go
database := app.DB()
affected, err := database.Name("users").Save(data)
updated, err := database.Name("users").WhereField("id", "=", 7).Save(data)
inserted, err := database.Name("users").WhereField("id", "=", 7).Save(data, true)
```

Model 的 `Save` 根据结构体主键选择路径：零值主键新增并回填，非零主键按主键更新。

```go
row := &User{ID: 0, Name: "Ada"}
err := userModel.Save(row) // INSERT

row.Name = "Grace"
err = userModel.Save(row) // UPDATE WHERE id = row.ID
```

`Create` 始终是新增语义，`Update` 始终要求非零主键。主键字段不兼容会在访问驱动前返回 `ErrInvalidModel`；极少数运行期 ID 类型漂移发生在写入完成后时返回 `*db.PartialWriteError`，必须使用其 `Result` 对账，不能直接重试。

### UpdateResult 与 DeleteResult

简化 API 继续返回数量；需要跨驱动精确语义时使用 typed result：

```go
result, err := database.Name("users").
	WhereField("id", "=", 7).
	UpdateResult(map[string]interface{}{"name": "Grace"})

if result.MatchedKnown {
	fmt.Println(result.Matched)
}
if result.ModifiedKnown {
	fmt.Println(result.Modified)
}

deleted, err := database.Name("users").
	WhereField("id", "=", 7).
	DeleteResult()
```

普通 `Update` 优先返回可证明的真实修改数；否则退回驱动影响数。`DeleteResult` 保留记录/节点数，Neo4j 显式 detach 时还保留关系删除数。

## 链式查询改为不可变

所有链式方法都返回新 Query，必须接收返回值。旧代码如果忽略返回值，条件不会生效：

```go
// 错误：返回的新 Query 被丢弃。
query.WhereField("status", "=", 1)

// 正确。
query = query.WhereField("status", "=", 1)

// 推荐：直接链式使用。
rows, err := base.WhereField("status", "=", 1).Select()
```

同一个 base Query 可以并发派生分支，终端方法不会反向写入 `Limit`、分页、软删除或恢复状态。

## Searcher 回调签名

Query 不可变后，Searcher 必须返回追加条件后的 Query：

```go
err := userModel.Searcher("keyword", func(query *db.Query, value interface{}, data map[string]interface{}) *db.Query {
	return query.WhereLike("name", "%"+fmt.Sprint(value)+"%")
})
```

旧的无返回值回调无法编译；在回调内只调用 `query.Where...` 而不返回也不会保留条件。

## Connection 实现迁移

自定义驱动需要直接实现最终 `db.Connection`：

```go
Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error)
Insert(context.Context, db.InsertRequest) (db.InsertResult, error)
Update(context.Context, db.UpdateRequest) (db.UpdateResult, error)
Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error)
Count(context.Context, db.CountRequest) (int64, error)
Close() error
ConnectionID() db.ConnectionID
```

旧 `where []string`、`ContextualConnection`、`InsertContextWithPrimaryKey` 已移除。Request 对外只读并返回防御性副本；驱动能力通过 `CapabilityProvider` 声明。

## 底层连接租约

裸 `GetConnection` 已移除。需要官方驱动 API 时只在 `WithConnection` callback 内使用，不得保存到 callback 外，也不得关闭框架托管连接：

```go
import mongoDriver "github.com/zhuhanxin0308/thinkgo/framework/db/driver/mongo"

err = database.WithConnection(func(connection db.Connection) error {
	mongoConnection, ok := connection.(*mongoDriver.MongoConnection)
	if !ok {
		return db.ErrUnsupportedFeature
	}
	return useNativeMongo(mongoConnection.Client)
})
```

`DB.Close` 会冻结新租约并等待活动查询/事务；多个 wrapper 共享同一底层连接时按 `ConnectionID` 去重关闭。

MongoDB 与 Neo4j 连接实现分别位于 `db/driver/mongo` 和 `db/driver/neo4j`。`db` 核心不再编译这些可选后端；`connector.RegisterBuiltins()` 继续提供完整框架的显式连接器注册。

## 游标与大表遍历

整数和 `time.Time` 可直接使用 `ChunkById`。字符串、`[]byte`、文本 DECIMAL/NUMERIC 或自定义键必须使用 `ChunkByIdWithCodec`，codec 的比较规则必须与数据库类型和 collation 一致。框架不再用 Go 字典序猜测数据库排序；缺少 codec 返回 `ErrUnsupportedCursorKey`。

真实 SQLite 100 万行末页基准中，深 OFFSET 约为 keyset 的 9.5 倍。框架不会自动把 `Chunk` 改成 keyset，因为缺少唯一、非空、严格递增键契约。

## 悲观锁和方言差异

- MySQL、PostgreSQL 支持排他/共享锁。
- SQL Server 使用主表 `UPDLOCK`/`HOLDLOCK` hint；不自动扩散到 JOIN 表。
- SQLite 对两种锁都返回 `ErrUnsupportedFeature`。
- Oracle 支持排他锁；共享锁以及 lock + row limiting 返回 `ErrUnsupportedFeature`。
- 未知 lock enum 返回 `ErrUnsupportedLockMode`，不再静默忽略或升级锁级别。

## MongoDB

- 固有顶层 `_id` 和显式业务主键支持 ObjectID 十六进制字符串对称往返；普通字符串、非主键及嵌套 ObjectID 不会被猜测转换。
- 显式投影未选择 `_id` 时默认生成 `_id: 0`。
- `Insert` 返回文档数，`InsertGetId` 返回真实 `InsertedID`。
- 默认 Model 的逻辑字段 `id` 现在对称映射到存储键 `_id`，Create 回填后可直接 Save/Where/Find/Delete；如果集合使用真实业务字段 `id`，升级时必须显式调用 `PrimaryKey("id")`，并在 Create/Save 前提供非零业务主键，否则在 Insert 前返回 `ErrInvalidModel`。如果结构体直接标记 `_id` 则显式调用 `PrimaryKey("_id")`。默认映射模式禁止通过 Update 修改逻辑 `id` 或存储 `_id`，违规时在驱动写入前返回 `ErrInvalidQuery`。

## Neo4j

`Delete` 现在默认严格：存在关系时让服务端拒绝。只有显式 API 才连带删除关系：

```go
deleted, err := query.Delete()
result, err := query.DetachDeleteResult()
fmt.Println(result.Deleted, result.RelatedDeleted)
```

Neo4j 操作使用 `ExecuteRead`/`ExecuteWrite` 托管事务；自定义原生回调必须可安全重试。显式配置目标数据库时，连接阶段会执行并消费探测查询。

## 事务、错误和日志

- context 取消会终结事务状态并释放连接租约；后续操作稳定返回 `ErrTransactionDone`。
- 无业务 WHERE 的 Update/Delete 继续返回 `ErrUnsafeFullTableMutation`。
- 驱动原始错误返回给调用方，但日志只记录安全错误类别、脱敏 SQL 和参数数量，不记录绑定值或驱动错误原文。
- 调用方应使用 `errors.Is`，不要依赖错误字符串。

## 升级检查清单

1. 搜索所有把 `Insert` 返回值命名为 `id` 的代码，按用途改成 `affected` 或 `InsertGetId`。
2. 搜索忽略链式返回值的 `Where/Field/Order/Limit/WithContext/Lock` 调用。
3. 更新 Searcher 为返回 `*db.Query` 的签名。
4. 把裸连接访问迁移到 `WithConnection` callback。
5. 为非整数游标注册与数据库排序一致的 `CursorCodec`。
6. 检查 SQLite/Oracle/SQL Server 锁调用是否处理 unsupported error。
7. 把 Neo4j 依赖默认 detach 的删除显式改为 `DetachDelete`。
8. MongoDB 代码使用 `/v2/bson` 类型，并区分 `_id` 与普通字符串字段。
9. 自定义 Connection 迁移到 request/result 接口并声明 capabilities。
10. 运行 `go test ./...` 和 ORM race/真实驱动测试；无需增加配置文件。
