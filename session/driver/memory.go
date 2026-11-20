package driver

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	memorySessionEnvelopeVersion = 1
	memorySessionLockShards      = 64
)

type memorySessionEntry struct {
	data    string
	updated time.Time
	seq     uint64
}

// Memory 是支持原子读改写的进程内 Session 驱动，并提供可选容量和过期回收。
type Memory struct {
	mutex      sync.RWMutex
	entries    map[string]memorySessionEntry
	inFlight   map[string]int
	seq        uint64
	clearSeq   uint64
	order      *list.List
	orderIndex map[string]*list.Element
	maxEntries int
	locks      [memorySessionLockShards]sync.Mutex
}

// NewMemory 创建无容量限制的内存 Session 驱动，保留旧 API 的兼容语义。
func NewMemory() *Memory {
	driver, _ := NewMemoryWithMaxEntries(0)
	return driver
}

// NewMemoryWithMaxEntries 创建带 FIFO 容量上限的内存 Session 驱动，0 表示兼容的无限容量。
func NewMemoryWithMaxEntries(maxEntries int) (*Memory, error) {
	if maxEntries < 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidMemoryCapacity, maxEntries)
	}
	driver := &Memory{maxEntries: maxEntries}
	driver.ensureDataLocked()
	return driver, nil
}

// Read 读取 Session，并显式区分缺失记录与空字符串。
func (m *Memory) Read(id string) (string, bool, error) {
	if err := validateSessionID(id); err != nil {
		return "", false, err
	}
	m.mutex.RLock()
	entry, found := m.entries[id]
	m.mutex.RUnlock()
	return entry.data, found, nil
}

// Write 写入 Session；达到容量上限时淘汰最早写入的记录。
func (m *Memory) Write(id string, data string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	lock := m.lockForID(id)
	lock.Lock()
	defer lock.Unlock()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.ensureDataLocked()
	if err := m.ensureCapacityLocked(id); err != nil {
		return err
	}
	m.seq++
	m.entries[id] = memorySessionEntry{data: data, updated: time.Now(), seq: m.seq}
	m.touchOrderLocked(id)
	return nil
}

// Delete 幂等删除 Session。
func (m *Memory) Delete(id string) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	lock := m.lockForID(id)
	lock.Lock()
	defer lock.Unlock()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.removeLocked(id)
	return nil
}

// Clear 清空全部内存 Session。
func (m *Memory) Clear() error {
	unlock := m.lockAllShards()
	defer unlock()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.clearSeq++
	m.entries = make(map[string]memorySessionEntry)
	if m.maxEntries > 0 {
		m.order = list.New()
		m.orderIndex = make(map[string]*list.Element)
	} else {
		m.order = nil
		m.orderIndex = nil
	}
	return nil
}

// Update 在分片锁内完成读取、回调与提交，提交前复核代际与淘汰状态，回调失败时保持原值。
// 回调可能在并发淘汰后基于最新状态重算一次，因此不得执行无法安全重放的外部副作用。
func (m *Memory) Update(id string, update func(string, bool) (string, bool, error)) error {
	if err := validateSessionID(id); err != nil {
		return err
	}
	if update == nil {
		return ErrInvalidSessionUpdate
	}
	lock := m.lockForID(id)
	lock.Lock()
	defer lock.Unlock()
	m.mutex.Lock()
	m.ensureDataLocked()
	entry, found := m.entries[id]
	snapSeq := entry.seq
	snapClearSeq := m.clearSeq
	m.inFlight[id]++
	m.mutex.Unlock()

	inFlightHeld := true
	defer func() {
		if inFlightHeld {
			m.mutex.Lock()
			m.releaseInFlightLocked(id)
			m.mutex.Unlock()
		}
	}()

	for {
		// 全局锁只保护快照与提交，首次及淘汰后的回调都允许读取其他会话。
		next, remove, err := update(entry.data, found)
		if err != nil {
			return err
		}
		m.mutex.Lock()
		m.ensureDataLocked()
		if m.clearSeq != snapClearSeq {
			m.mutex.Unlock()
			return ErrSessionCleared
		}
		current, currentFound := m.entries[id]
		if currentFound != found || current.seq != snapSeq {
			entry, found, snapSeq = current, currentFound, current.seq
			m.mutex.Unlock()
			// 分片锁排除了同 ID 的重建与改写，跨分片只能把存在项淘汰为缺失；
			// 因而合法驱动操作最多重算一次，不会在持续写入下无界重放回调。
			continue
		}
		m.releaseInFlightLocked(id)
		inFlightHeld = false
		defer m.mutex.Unlock()
		if remove {
			m.removeLocked(id)
			return nil
		}
		if err = m.ensureCapacityLocked(id); err != nil {
			return err
		}
		m.seq++
		m.entries[id] = memorySessionEntry{data: next, updated: time.Now(), seq: m.seq}
		m.touchOrderLocked(id)
		return nil
	}
}

// GC 删除已过期、已撤销、损坏或超过最大存活时间的记录。
func (m *Memory) GC(maxLifetime time.Duration) (int, error) {
	if m == nil {
		return 0, ErrInvalidMemoryCapacity
	}
	if maxLifetime <= 0 {
		return 0, nil
	}
	now := time.Now()
	deadline := now.Add(-maxLifetime)
	unlock := m.lockAllShards()
	defer unlock()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	removed := 0
	for id, entry := range m.entries {
		if entry.updated.Before(deadline) || memoryEnvelopeExpired(entry.data, now) {
			m.removeLocked(id)
			removed++
		}
	}
	return removed, nil
}

func (m *Memory) ensureDataLocked() {
	if m.entries == nil {
		m.entries = make(map[string]memorySessionEntry)
	}
	if m.inFlight == nil {
		m.inFlight = make(map[string]int)
	}
	if m.maxEntries > 0 && m.order == nil {
		m.order = list.New()
		m.orderIndex = make(map[string]*list.Element)
		for id := range m.entries {
			m.touchOrderLocked(id)
		}
	}
}

func (m *Memory) releaseInFlightLocked(id string) {
	if m.inFlight == nil {
		return
	}
	if remaining := m.inFlight[id] - 1; remaining > 0 {
		m.inFlight[id] = remaining
	} else {
		delete(m.inFlight, id)
	}
}

// lockForID 使用固定分片锁，让不同 Session ID 的原子更新尽量并行，同时避免无界保存用户输入的锁对象。
func (m *Memory) lockForID(id string) *sync.Mutex {
	var hash uint32 = 2166136261
	for index := 0; index < len(id); index++ {
		hash ^= uint32(id[index])
		hash *= 16777619
	}
	return &m.locks[hash%memorySessionLockShards]
}

// lockAllShards 在全量清理时阻止单个 Session 更新穿过快照和提交边界。
func (m *Memory) lockAllShards() func() {
	for index := range m.locks {
		m.locks[index].Lock()
	}
	return func() {
		for index := len(m.locks) - 1; index >= 0; index-- {
			m.locks[index].Unlock()
		}
	}
}

func (m *Memory) ensureCapacityLocked(id string) error {
	if m.maxEntries <= 0 || m.orderIndex[id] != nil {
		return nil
	}
	for len(m.entries) >= m.maxEntries {
		oldest := m.selectEvictionTargetLocked()
		if oldest == nil {
			return ErrMemoryCapacityExhausted
		}
		oldestID, ok := oldest.Value.(string)
		if !ok {
			m.order.Remove(oldest)
			continue
		}
		m.removeLocked(oldestID)
	}
	return nil
}

func (m *Memory) selectEvictionTargetLocked() *list.Element {
	if m.order == nil {
		return nil
	}
	for element := m.order.Front(); element != nil; element = element.Next() {
		candidateID, ok := element.Value.(string)
		if !ok || m.inFlight[candidateID] == 0 {
			return element
		}
	}
	return m.order.Front()
}

func (m *Memory) touchOrderLocked(id string) {
	if m.maxEntries <= 0 {
		return
	}
	if m.order == nil {
		m.order = list.New()
		m.orderIndex = make(map[string]*list.Element)
	}
	if _, exists := m.orderIndex[id]; exists {
		// 容量淘汰采用 FIFO 语义，更新记录不应改变首次写入顺序。
		return
	}
	m.orderIndex[id] = m.order.PushBack(id)
}

func (m *Memory) removeLocked(id string) {
	if element, exists := m.orderIndex[id]; exists {
		m.order.Remove(element)
		delete(m.orderIndex, id)
	}
	delete(m.entries, id)
}

type memorySessionEnvelope struct {
	Version  int   `json:"version"`
	ExpireAt int64 `json:"expire_at"`
	Revoked  bool  `json:"revoked"`
}

func memoryEnvelopeExpired(content string, now time.Time) bool {
	var envelope memorySessionEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err != nil || envelope.Version != memorySessionEnvelopeVersion {
		return true
	}
	return envelope.ExpireAt > 0 && now.Unix() >= envelope.ExpireAt
}

var _ interface {
	Read(string) (string, bool, error)
	Write(string, string) error
	Delete(string) error
	Clear() error
	Update(string, func(string, bool) (string, bool, error)) error
	GC(time.Duration) (int, error)
} = (*Memory)(nil)
