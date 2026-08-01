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
// 连接实现不得修改进程全局 time.Local，只能保存并使用传入的应用时区。
type LocationAwareConnection interface {
	SetLocation(*time.Location)
	Location() *time.Location
}

// RawQueryable exposes native SQL for SQL-only advanced queries.
// It corresponds to ThinkPHP's Db::query() and Db::execute().
type RawQueryable interface {
	Query(sql string, args ...interface{}) ([]map[string]interface{}, error)
	Execute(sql string, args ...interface{}) (int64, error)
}

// ContextualRawQueryable is the cancellable native SQL contract.
type ContextualRawQueryable interface {
	QueryContext(ctx context.Context, sql string, args ...interface{}) ([]map[string]interface{}, error)
	ExecuteContext(ctx context.Context, sql string, args ...interface{}) (int64, error)
}
