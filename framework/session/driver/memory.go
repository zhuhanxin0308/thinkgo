package driver

import "sync"

// Memory 是零值可用、支持原子读改写的内存 Session 驱动。
type Memory struct {
	mutex sync.RWMutex
	data  map[string]string
}

// NewMemory 创建内存 Session 驱动。
func NewMemory() *Memory {
	return &Memory{data: make(map[string]string)}
}

// Read 读取 Session，并显式区分缺失记录与空字符串。
func (m *Memory) Read(id string) (string, bool, error) {
	if err := validateSessionID(id); err != nil {
		return "", false, err
	}
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	value, found := m.data[id]
	return value, found, nil
}

// Write 写入 Session；零值实例会在首次写入时初始化。
func (m *Memory) Write(id string, data string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.ensureDataLocked()
	m.data[id] = data
	return nil
}

// Delete 幂等删除 Session。
func (m *Memory) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.data, id)
	return nil
}

// Clear 清空全部内存 Session。
func (m *Memory) Clear() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[string]string)
	return nil
}

// Update 在单一写锁内完成读取、回调与提交，回调失败时保持原值。
func (m *Memory) Update(id string, update func(string, bool) (string, bool, error)) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	if update == nil {
		return ErrInvalidSessionUpdate
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.ensureDataLocked()
	current, found := m.data[id]
	next, remove, err := update(current, found)
	if err != nil {
		return err
	}
	if remove {
		delete(m.data, id)
		return nil
	}
	m.data[id] = next
	return nil
}

func (m *Memory) ensureDataLocked() {
	if m.data == nil {
		m.data = make(map[string]string)
	}
}
