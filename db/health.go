package db

import (
	"context"
	"fmt"
)

// ConnectionPinger 是可选的运行时健康探测能力，不破坏现有 Connection 驱动接口。
type ConnectionPinger interface {
	PingContext(context.Context) error
}

// PingContext 在连接租约内检查真实后端可用性，不能用历史查询结果代替探测。
func (database *DB) PingContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidDatabaseContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return database.WithConnection(func(connection Connection) error {
		pinger, ok := connection.(ConnectionPinger)
		if !ok {
			return fmt.Errorf("%w: 连接未实现运行时健康探测", ErrUnsupportedFeature)
		}
		return pinger.PingContext(ctx)
	})
}

// PingContext 使用原生连接池探测，覆盖掉线、重连与已关闭状态。
func (connection *SQLConnection) PingContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidDatabaseContext
	}
	if connection == nil || connection.DB == nil {
		return ErrDatabaseUnavailable
	}
	return connection.DB.PingContext(ctx)
}

// DefaultIfLoaded 只返回已建立的默认连接，不因观察状态而触发惰性连接工厂。
func (manager *Manager) DefaultIfLoaded() (*DB, bool, error) {
	if manager == nil {
		return nil, false, ErrDatabaseUnavailable
	}
	manager.lock.RLock()
	defer manager.lock.RUnlock()
	if manager.closed {
		return nil, false, ErrDatabaseManagerClosed
	}
	if manager.initErr != nil {
		return nil, false, manager.initErr
	}
	connection := manager.connections[manager.defaultName]
	return connection, connection != nil, nil
}
