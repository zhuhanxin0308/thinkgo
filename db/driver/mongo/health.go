package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// PingContext 探测主节点可用性，并继承调用方的取消和驱动操作预算。
func (connection *MongoConnection) PingContext(parent context.Context) error {
	ctx, cancel, err := connection.operationContext(parent)
	if err != nil {
		return err
	}
	defer cancel()
	return connection.Client.Ping(ctx, readpref.Primary())
}
