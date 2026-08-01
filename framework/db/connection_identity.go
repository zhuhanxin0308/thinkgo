package db

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ConnectionID is a stable process-local identity for one physical connection owner.
type ConnectionID string

var connectionIdentitySequence atomic.Uint64

type connectionIdentityState struct {
	once sync.Once
	id   ConnectionID
}

func (state *connectionIdentityState) ConnectionID() ConnectionID {
	if state == nil {
		return ""
	}
	state.once.Do(func() { state.id = NewConnectionID("connection") })
	return state.id
}

// NewConnectionID creates a non-empty stable identity without exposing pointer addresses.
func NewConnectionID(prefix string) ConnectionID {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "connection"
	}
	return ConnectionID(fmt.Sprintf("%s-%d", prefix, connectionIdentitySequence.Add(1)))
}

type managedConnection struct {
	id           ConnectionID
	connection   Connection
	closeOnce    sync.Once
	closeErr     error
	activeLeases sync.WaitGroup
	leaseMu      sync.Mutex
	leaseCount   int
	leasesDone   chan struct{}
	managerOwned atomic.Bool
}

func newManagedConnection(connection Connection) *managedConnection {
	if isNilDatabaseDependency(connection) {
		return &managedConnection{connection: connection}
	}
	return &managedConnection{id: connection.ConnectionID(), connection: connection}
}

func (connection *managedConnection) Close() error {
	return connection.CloseContext(context.Background())
}

// CloseContext 等待在途租约，并允许调用方通过上下文取消等待。
func (connection *managedConnection) CloseContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidDatabaseContext
	}
	if connection == nil || isNilDatabaseDependency(connection.connection) {
		return ErrDatabaseUnavailable
	}
	if err := connection.waitForLeases(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	connection.closeOnce.Do(func() {
		connection.closeErr = connection.connection.Close()
	})
	return connection.closeErr
}

// addLease 记录一次数据库操作租约；调用方必须配对 releaseLease。
func (connection *managedConnection) addLease() {
	connection.activeLeases.Add(1)
	connection.leaseMu.Lock()
	if connection.leaseCount == 0 {
		connection.leasesDone = make(chan struct{})
	}
	connection.leaseCount++
	connection.leaseMu.Unlock()
}

// releaseLease 释放数据库操作租约，并通知等待关闭的调用方。
func (connection *managedConnection) releaseLease() {
	connection.activeLeases.Done()
	connection.leaseMu.Lock()
	if connection.leaseCount > 0 {
		connection.leaseCount--
	}
	if connection.leaseCount == 0 && connection.leasesDone != nil {
		close(connection.leasesDone)
		connection.leasesDone = nil
	}
	connection.leaseMu.Unlock()
}

func (connection *managedConnection) waitForLeases(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	connection.leaseMu.Lock()
	if connection.leaseCount == 0 {
		connection.leaseMu.Unlock()
		return nil
	}
	done := connection.leasesDone
	connection.leaseMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
