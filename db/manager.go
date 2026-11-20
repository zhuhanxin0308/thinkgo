package db

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Manager 管理不可静默覆盖的命名连接，并提供幂等关闭生命周期。
type Manager struct {
	defaultName     string
	connections     map[string]*DB
	factories       map[string]*lazyConnectionFactory
	handles         map[ConnectionID]*managedConnection
	lock            sync.RWMutex
	closed          bool
	initErr         error
	factoryClosing  chan struct{}
	factoryWG       sync.WaitGroup
	factoryCloseErr error
	closeOnce       sync.Once
	closeErr        error
}

// ConnectionFactory 延迟创建一个命名数据库连接。
type ConnectionFactory func() (*DB, error)

type lazyConnectionFactory struct {
	build  ConnectionFactory
	active *lazyConnectionAttempt
}

type lazyConnectionAttempt struct {
	done       chan struct{}
	connection *DB
	err        error
}

// wait 等待工厂发布结果。若管理器开始关闭，已发布的结果优先，其余等待者立即退出。
func (attempt *lazyConnectionAttempt) wait(closing <-chan struct{}) (*DB, error) {
	select {
	case <-attempt.done:
	case <-closing:
		select {
		case <-attempt.done:
		default:
			return nil, ErrDatabaseManagerClosed
		}
	}
	return attempt.connection, attempt.err
}

// ValidateConnectionName 校验应用配置使用的命名连接标识。
func ValidateConnectionName(name string) error {
	if !connectorNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidConnectionName, name)
	}
	return nil
}

// NewManager 创建数据库连接管理器；非法默认名称会在首次操作时显式返回。
func NewManager(defaultName string) *Manager {
	manager := &Manager{
		defaultName:    defaultName,
		connections:    make(map[string]*DB),
		factories:      make(map[string]*lazyConnectionFactory),
		handles:        make(map[ConnectionID]*managedConnection),
		factoryClosing: make(chan struct{}),
	}
	if err := ValidateConnectionName(defaultName); err != nil {
		manager.initErr = err
	}
	return manager
}

// Add 注册命名连接，拒绝非法依赖、重复名称和关闭后写入。
func (m *Manager) Add(name string, connection *DB) error {
	return m.addConnection(name, connection, false)
}

// RegisterFactory 注册惰性命名连接。并发首次访问只执行一次工厂并共享结果，失败后可重试。
// 若管理器开始关闭时工厂尚未发布结果，等待该结果的调用者可返回 ErrDatabaseManagerClosed。
func (m *Manager) RegisterFactory(name string, factory ConnectionFactory) error {
	if m == nil {
		return ErrDatabaseUnavailable
	}
	if err := ValidateConnectionName(name); err != nil {
		return err
	}
	if factory == nil {
		return ErrDatabaseUnavailable
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.initErr != nil {
		return m.initErr
	}
	if m.closed {
		return ErrDatabaseManagerClosed
	}
	if _, exists := m.connections[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateConnection, name)
	}
	if _, exists := m.factories[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateConnection, name)
	}
	m.factories[name] = &lazyConnectionFactory{build: factory}
	return nil
}

func (m *Manager) addConnection(name string, connection *DB, fromFactory bool) error {
	if m == nil {
		return ErrDatabaseUnavailable
	}
	if err := ValidateConnectionName(name); err != nil {
		return err
	}
	if connection == nil {
		return ErrDatabaseUnavailable
	}
	var identity ConnectionID
	if err := connection.WithConnection(func(backend Connection) error {
		identity = backend.ConnectionID()
		if identity == "" {
			return fmt.Errorf("%w: connection identity cannot be empty", ErrInvalidDatabaseConfig)
		}
		return nil
	}); err != nil {
		return err
	}
	candidateHandle, err := connection.managedConnectionHandle()
	if err != nil {
		return err
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.initErr != nil {
		return m.initErr
	}
	if m.closed {
		return ErrDatabaseManagerClosed
	}
	if _, exists := m.connections[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateConnection, name)
	}
	if _, exists := m.factories[name]; exists && !fromFactory {
		return fmt.Errorf("%w: %s", ErrDuplicateConnection, name)
	}
	handle, exists := m.handles[identity]
	if !exists {
		handle = candidateHandle
	}
	if err := connection.attachManagedConnection(handle); err != nil {
		return err
	}
	// Manager 接管物理连接的关闭时机，先封闭全部 DB 包装器，再等待共享租约。
	handle.managerOwned.Store(true)
	if !exists {
		m.handles[identity] = handle
	}
	m.connections[name] = connection
	return nil
}

// Default 返回默认连接并显式区分配置错误、关闭和缺失。
func (m *Manager) Default() (*DB, error) {
	if m == nil {
		return nil, ErrDatabaseUnavailable
	}
	return m.Connection(m.defaultName)
}

// Connection 返回指定名称的连接。
func (m *Manager) Connection(name string) (*DB, error) {
	if m == nil {
		return nil, ErrDatabaseUnavailable
	}
	if err := ValidateConnectionName(name); err != nil {
		return nil, err
	}
	m.lock.RLock()
	if m.initErr != nil {
		defer m.lock.RUnlock()
		return nil, m.initErr
	}
	if m.closed {
		defer m.lock.RUnlock()
		return nil, ErrDatabaseManagerClosed
	}
	connection, ok := m.connections[name]
	if ok && connection != nil {
		m.lock.RUnlock()
		return connection, nil
	}
	registered := m.factories[name]
	if registered == nil || registered.build == nil {
		m.lock.RUnlock()
		return nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, name)
	}
	if active := registered.active; active != nil {
		m.lock.RUnlock()
		return active.wait(m.factoryClosing)
	}
	m.lock.RUnlock()

	// 写锁内复核连接与在途构建，保证并发首访只启动一个工厂。
	m.lock.Lock()
	if m.initErr != nil {
		m.lock.Unlock()
		return nil, m.initErr
	}
	if m.closed {
		m.lock.Unlock()
		return nil, ErrDatabaseManagerClosed
	}
	if connection = m.connections[name]; connection != nil {
		m.lock.Unlock()
		return connection, nil
	}
	registered = m.factories[name]
	if registered == nil || registered.build == nil {
		m.lock.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, name)
	}
	if active := registered.active; active != nil {
		m.lock.Unlock()
		return active.wait(m.factoryClosing)
	}
	attempt := &lazyConnectionAttempt{done: make(chan struct{})}
	registered.active = attempt
	m.factoryWG.Add(1)
	m.lock.Unlock()

	connection, err := callConnectionFactorySafely(registered.build)
	if err == nil && connection == nil {
		err = ErrDatabaseUnavailable
	}
	if err == nil {
		if addErr := m.addConnection(name, connection, true); addErr != nil {
			closeErr := m.closeUnattachedConnection(connection)
			err = errors.Join(addErr, closeErr)
			connection = nil
			if closeErr != nil {
				m.lock.Lock()
				m.factoryCloseErr = errors.Join(m.factoryCloseErr, closeErr)
				m.lock.Unlock()
			}
		}
	}
	if err != nil {
		err = fmt.Errorf("创建数据库连接 %q 失败: %w", name, err)
	}

	m.lock.Lock()
	if registered.active == attempt {
		registered.active = nil
	}
	attempt.connection = connection
	attempt.err = err
	close(attempt.done)
	m.lock.Unlock()
	m.factoryWG.Done()
	return connection, err
}

func (m *Manager) closeUnattachedConnection(connection *DB) error {
	if connection == nil {
		return nil
	}
	candidateHandle, err := connection.managedConnectionHandle()
	if err == nil && candidateHandle != nil && candidateHandle.id != "" {
		m.lock.RLock()
		_, shared := m.handles[candidateHandle.id]
		m.lock.RUnlock()
		if shared {
			// 该物理连接已由 Manager 的共享句柄统一管理租约与关闭，仅封闭未挂载的包装器。
			connection.markClosed()
			return nil
		}
	}
	return connection.Close()
}

func callConnectionFactorySafely(factory ConnectionFactory) (connection *DB, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			connection = nil
			err = fmt.Errorf("数据库连接工厂 panic: %v", recovered)
		}
	}()
	return factory()
}

// Close 唤醒等待惰性连接的调用者，关闭每个唯一 DB，并等待在途工厂实际退出。
// 关闭错误聚合后向所有并发调用返回相同结果。
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.lock.Lock()
		m.closed = true
		if m.factoryClosing != nil {
			close(m.factoryClosing)
		}
		names := make([]string, 0, len(m.connections))
		for name := range m.connections {
			names = append(names, name)
		}
		sort.Strings(names)
		connections := make([]*DB, 0, len(names))
		seen := make(map[*DB]bool, len(names))
		for _, name := range names {
			connection := m.connections[name]
			if connection == nil || seen[connection] {
				continue
			}
			seen[connection] = true
			connections = append(connections, connection)
		}
		handles := make([]*managedConnection, 0, len(m.handles))
		for _, handle := range m.handles {
			if handle != nil {
				handles = append(handles, handle)
			}
		}
		m.lock.Unlock()

		var result error
		for _, connection := range connections {
			connection.markClosed()
		}
		for _, handle := range handles {
			result = errors.Join(result, handle.Close())
		}
		m.factoryWG.Wait()
		m.lock.RLock()
		factoryCloseErr := m.factoryCloseErr
		m.lock.RUnlock()
		m.closeErr = errors.Join(result, factoryCloseErr)
	})
	return m.closeErr
}
