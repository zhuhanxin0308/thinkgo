package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// neo4jOperationExecutor 隔离官方驱动的会话生命周期，保证每次操作都消费结果并关闭会话。
type neo4jOperationExecutor interface {
	Collect(context.Context, neo4j.AccessMode, string, map[string]interface{}) ([]*neo4j.Record, error)
	Single(context.Context, neo4j.AccessMode, string, map[string]interface{}) (*neo4j.Record, error)
	Execute(context.Context, neo4j.AccessMode, string, map[string]interface{}) (DeleteResult, error)
	Close(context.Context) error
}

type neo4jSessionCloser interface {
	Close(context.Context) error
}

// closeNeo4jSession 使用脱离业务取消信号的有界上下文关闭会话，
// 避免超时请求无法把底层连接归还驱动连接池。
func closeNeo4jSession(parent context.Context, session neo4jSessionCloser) error {
	base := context.Background()
	if parent != nil {
		base = context.WithoutCancel(parent)
	}
	ctx, cancel := context.WithTimeout(base, defaultNeo4jOpTimeout)
	defer cancel()
	return session.Close(ctx)
}

type neo4jDriverExecutor struct {
	driver   neo4j.DriverWithContext
	database string
}

func (executor *neo4jDriverExecutor) session(ctx context.Context, mode neo4j.AccessMode) (neo4j.SessionWithContext, error) {
	if executor == nil || executor.driver == nil {
		return nil, ErrDatabaseUnavailable
	}
	session := executor.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: mode, DatabaseName: executor.database})
	if session == nil {
		return nil, fmt.Errorf("%w: Neo4j 驱动返回空会话", ErrDatabaseUnavailable)
	}
	return session, nil
}

func runNeoManaged(ctx context.Context, session neo4j.SessionWithContext, mode neo4j.AccessMode, work neo4j.ManagedTransactionWork) (interface{}, error) {
	if session == nil || work == nil {
		return nil, ErrDatabaseUnavailable
	}
	if mode == neo4j.AccessModeRead {
		return session.ExecuteRead(ctx, work)
	}
	return session.ExecuteWrite(ctx, work)
}

func (executor *neo4jDriverExecutor) Collect(ctx context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (records []*neo4j.Record, resultErr error) {
	session, err := executor.session(ctx, mode)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeNeo4jSession(ctx, session)) }()
	value, err := runNeoManaged(ctx, session, mode, func(transaction neo4j.ManagedTransaction) (interface{}, error) {
		result, err := transaction.Run(ctx, cypher, cloneDatabaseMap(params))
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("%w: Neo4j 驱动返回空结果", ErrInvalidDatabaseRow)
		}
		return result.Collect(ctx)
	})
	if err != nil {
		return nil, err
	}
	records, ok := value.([]*neo4j.Record)
	if !ok {
		return nil, fmt.Errorf("%w: Neo4j managed Collect 结果类型为 %T", ErrInvalidDatabaseRow, value)
	}
	return records, nil
}

func (executor *neo4jDriverExecutor) Single(ctx context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (record *neo4j.Record, resultErr error) {
	session, err := executor.session(ctx, mode)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeNeo4jSession(ctx, session)) }()
	value, err := runNeoManaged(ctx, session, mode, func(transaction neo4j.ManagedTransaction) (interface{}, error) {
		result, err := transaction.Run(ctx, cypher, cloneDatabaseMap(params))
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("%w: Neo4j 驱动返回空结果", ErrInvalidDatabaseRow)
		}
		return result.Single(ctx)
	})
	if err != nil {
		return nil, err
	}
	record, ok := value.(*neo4j.Record)
	if !ok {
		return nil, fmt.Errorf("%w: Neo4j managed Single 结果类型为 %T", ErrInvalidDatabaseRow, value)
	}
	return record, nil
}

func (executor *neo4jDriverExecutor) Execute(ctx context.Context, mode neo4j.AccessMode, cypher string, params map[string]interface{}) (operationResult DeleteResult, resultErr error) {
	session, err := executor.session(ctx, mode)
	if err != nil {
		return DeleteResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, closeNeo4jSession(ctx, session)) }()
	value, err := runNeoManaged(ctx, session, mode, func(transaction neo4j.ManagedTransaction) (interface{}, error) {
		result, err := transaction.Run(ctx, cypher, cloneDatabaseMap(params))
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("%w: Neo4j 驱动返回空结果", ErrInvalidDatabaseRow)
		}
		summary, err := result.Consume(ctx)
		if err != nil {
			return nil, err
		}
		if summary == nil || summary.Counters() == nil {
			return nil, fmt.Errorf("%w: Neo4j 执行结果缺少统计信息", ErrInvalidDatabaseRow)
		}
		nodesDeleted := summary.Counters().NodesDeleted()
		relationshipsDeleted := summary.Counters().RelationshipsDeleted()
		if nodesDeleted < 0 || relationshipsDeleted < 0 {
			return nil, fmt.Errorf("%w: Neo4j 删除数量非法", ErrInvalidAggregateValue)
		}
		deleteResult := DeleteResult{
			Deleted:             int64(nodesDeleted),
			RelatedDeleted:      int64(relationshipsDeleted),
			RelatedDeletedKnown: true,
		}
		return deleteResult, deleteResult.Validate()
	})
	if err != nil {
		return DeleteResult{}, err
	}
	operationResult, ok := value.(DeleteResult)
	if !ok {
		return DeleteResult{}, fmt.Errorf("%w: Neo4j managed Execute 结果类型为 %T", ErrInvalidDatabaseRow, value)
	}
	return operationResult, nil
}

func (executor *neo4jDriverExecutor) Close(ctx context.Context) error {
	if executor == nil || executor.driver == nil {
		return ErrDatabaseUnavailable
	}
	return executor.driver.Close(ctx)
}
