package neo4j

import "context"

// PingContext 通过正式驱动验证连接可用性，继承探针取消与操作预算。
func (connection *Neo4jConnection) PingContext(parent context.Context) error {
	ctx, cancel, err := connection.operationContext(parent)
	if err != nil {
		return err
	}
	defer cancel()
	if connection.Driver == nil {
		return ErrDatabaseUnavailable
	}
	return connection.Driver.VerifyConnectivity(ctx)
}
