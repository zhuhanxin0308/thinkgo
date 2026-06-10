package driver

import (
	"sync"
)

// Memory 内存会话驱动（适用于开发/测试环境）
type Memory struct {
	data  map[string]string
	mutex sync.RWMutex
}

// NewMemory 创建内存会话驱动
func NewMemory() *Memory {
	return &Memory{
		data: make(map[string]string),
	}
}

// Read 读取会话数据
func (m *Memory) Read(id string) (string, error) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.data[id], nil
}

// Write 写入会话数据
func (m *Memory) Write(id string, data string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data[id] = data
	return nil
}

// Delete 删除会话数据
func (m *Memory) Delete(id string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	delete(m.data, id)
	return nil
}

// Clear 清空所有会话数据
func (m *Memory) Clear() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.data = make(map[string]string)
	return nil
}
