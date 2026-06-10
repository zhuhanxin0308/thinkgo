package db

import (
	"fmt"
	"sync"
)

// Manager 管理多数据库连接，并保留默认连接语义。
type Manager struct {
	defaultName string
	connections map[string]*DB
	lock        sync.RWMutex
}

// NewManager 创建数据库连接管理器。
func NewManager(defaultName string) *Manager {
	return &Manager{
		defaultName: defaultName,
		connections: make(map[string]*DB),
	}
}

// Add 注册命名连接。
func (m *Manager) Add(name string, connection *DB) {
	if m == nil || name == "" || connection == nil {
		return
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	m.connections[name] = connection
}

// Default 返回默认连接。
func (m *Manager) Default() *DB {
	if m == nil {
		return nil
	}
	m.lock.RLock()
	defer m.lock.RUnlock()
	return m.connections[m.defaultName]
}

// Connection 返回指定名称的连接。
func (m *Manager) Connection(name string) (*DB, error) {
	if m == nil {
		return nil, fmt.Errorf("database manager is nil")
	}
	m.lock.RLock()
	defer m.lock.RUnlock()
	connection, ok := m.connections[name]
	if !ok || connection == nil {
		return nil, fmt.Errorf("database connection not found: %s", name)
	}
	return connection, nil
}

// Close 关闭全部已注册连接，并避免重复关闭同一实例。
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}

	m.lock.RLock()
	defer m.lock.RUnlock()

	closed := make(map[*DB]bool)
	for _, connection := range m.connections {
		if connection == nil || closed[connection] {
			continue
		}
		if err := connection.Close(); err != nil {
			return err
		}
		closed[connection] = true
	}
	return nil
}
