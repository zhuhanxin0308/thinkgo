// Package db 提供数据库连接、链式查询、模型与事务能力。
//
// # Raw SQL 与输入信任边界
//
// [DB.Query]、[DB.Execute] 及其 Context 变体、[Query.WhereRaw]、[Query.HavingRaw]
// 接受由应用审查的 SQL 结构。必须把外部值作为独立参数传入，使用框架的 ? 占位符；
// 不要通过字符串拼接或 fmt.Sprintf 把请求数据加入 SQL，也不要给占位符加引号。
// 占位符不能表示表名、字段名或排序方向，动态结构应从可信允许列表选择。
// 原生入口的格式和绑定数量检查不是授权、关键词防火墙或不可信 SQL 沙箱。
//
// [Query.BuildSelectSQL] 只构建文本和参数，不执行 SQL；返回值使用既有查询参数
// 的容器快照语义。将返回参数用于检查或修改，不会回写查询的这些内部容器。
// 自定义驱动值及未由现有快照器复制的对象仍应由调用方保证不可变。
// SQL、参数和原始错误可能含敏感信息，不应直接回显；[SanitizeError] 用于公共错误转换。
// 参数化不代替对象/租户授权、LIKE 字面转义或事务结果处理。
//
// 完整用法与反例见 docs/security/database-raw-sql.md；[Query.WhereRaw] 提供可执行示例。
package db
