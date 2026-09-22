package db

import (
	"context"
	"time"
)

// Connection is the typed, context-aware contract implemented by every driver.
// Requests preserve validated predicate provenance and results preserve driver semantics.
type Connection interface {
	ConnectionID() ConnectionID
	Select(context.Context, SelectRequest) ([]map[string]interface{}, error)
	Insert(context.Context, InsertRequest) (InsertResult, error)
	Update(context.Context, UpdateRequest) (UpdateResult, error)
	Delete(context.Context, DeleteRequest) (DeleteResult, error)
	Count(context.Context, CountRequest) (int64, error)
	Close() error
}

// RowStreamingConnection 提供按行消费查询结果的可选能力，避免强制物化完整结果集。
// 不支持流式读取的自定义连接仍可只实现 Connection，并由上层返回明确的能力错误。
type RowStreamingConnection interface {
	SelectEach(context.Context, SelectRequest, func(map[string]interface{}) bool) error
}

// LocationAwareConnection 是框架数据库连接的应用时区传播契约。
// 连接实现不得读取或修改进程全局 time.Local，只能保存并使用显式时区，空值固定回退 UTC。
type LocationAwareConnection interface {
	SetLocation(*time.Location)
	Location() *time.Location
}

// RawQueryable 仅保留无上下文 Query/Execute 的旧驱动兼容能力。
// 新驱动必须实现 ContextualRawQueryable，ORM 高级操作不会回退到本接口。
type RawQueryable interface {
	Query(sql string, args ...interface{}) ([]map[string]interface{}, error)
	Execute(sql string, args ...interface{}) (int64, error)
}

// ContextualRawQueryable 是支持超时与取消的原生 SQL 契约。
type ContextualRawQueryable interface {
	QueryContext(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error)
	ExecuteContext(ctx context.Context, sql string, args ...interface{}) (int64, error)
}
