package neo4j

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// PingContext 在配置的目标库运行只读探针，继承调用方取消与操作预算。
func (connection *Neo4jConnection) PingContext(parent context.Context) error {
	ctx, cancel, err := connection.operationContext(parent)
	if err != nil {
		return err
	}
	defer cancel()
	if connection.Driver == nil {
		return ErrDatabaseUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	const probeKey = "thinkgo_probe"
	record, err := connection.operationExecutor().Single(ctx, neo4j.AccessModeRead, "RETURN 1 AS "+probeKey, nil)
	if err != nil {
		return err
	}
	if record != nil {
		if value, ok := record.Get(probeKey); ok && value == int64(1) {
			return nil
		}
	}
	return fmt.Errorf("%w: Neo4j 目标数据库探测结果非法", ErrDatabaseUnavailable)
}
