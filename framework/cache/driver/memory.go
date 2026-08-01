package driver

import (
	"container/list"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

const memorySweepInterval = time.Minute

const (
	cacheTagMetadataPrefix        = "__thinkgo_tag__:"
	cacheTagReverseMetadataPrefix = "__thinkgo_tag_reverse__:"
)

// Memory 是支持 nil 命中、严格计数和独立锁空间的进程内缓存驱动。
type Memory struct {
	items      map[string]item
	locks      map[string]memoryLock
	order      *list.List
	maxEntries int
	lock       sync.RWMutex
	sequence   uint64
	lastSweep  time.Time
}

type item struct {
	value     interface{}
	expiry    time.Time
	version   uint64
	order     *list.Element
	protected bool
}

type memoryLock struct {
	owner  string
	expiry time.Time
}

type memoryCloneVisit struct {
	typ      reflect.Type
	kind     reflect.Kind
	pointer  uintptr
	length   int
	capacity int
}

// NewMemory 创建内存缓存驱动。
func NewMemory() *Memory {
	driver, _ := NewMemoryWithMaxEntries(0)
	return driver
}

// NewMemoryWithMaxEntries 创建带可选 FIFO 容量上限的内存缓存驱动；0 表示不限制容量。
func NewMemoryWithMaxEntries(maxEntries int) (*Memory, error) {
	if maxEntries < 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidMemoryCapacity, maxEntries)
	}
	driver := &Memory{
		items:      make(map[string]item),
		locks:      make(map[string]memoryLock),
		maxEntries: maxEntries,
	}
	if maxEntries > 0 {
		driver.order = list.New()
	}
	return driver, nil
}

func (c *Memory) Get(key string) (interface{}, bool, error) {
	if c == nil {
		return nil, false, fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.RLock()
	stored, found := c.items[key]
	value := stored.value
	expiry := stored.expiry
	version := stored.version
	c.lock.RUnlock()
	if !found {
		return nil, false, nil
	}
	if !expiry.IsZero() && !time.Now().Before(expiry) {
		c.lock.Lock()
		current, exists := c.items[key]
		if exists && current.version == version {
			c.removeItemLocked(key, current)
		}
		c.lock.Unlock()
		return nil, false, nil
	}
	return cloneMemoryValue(value), true, nil
}

func (c *Memory) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	now := time.Now()
	expiry := time.Time{}
	if ttl > 0 {
		expiry = now.Add(ttl)
	}
	cloned := cloneMemoryValue(value)
	c.lock.Lock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)
	c.sequence++
	err := c.storeItemLocked(key, cloned, expiry, c.sequence, isMetadataKey(key))
	c.lock.Unlock()
	return err
}

func (c *Memory) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

func (c *Memory) Delete(key string) error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.Lock()
	if stored, exists := c.items[key]; exists {
		c.removeItemLocked(key, stored)
	}
	c.lock.Unlock()
	return nil
}

func (c *Memory) Clear() error {
	if c == nil {
		return fmt.Errorf("内存缓存驱动为空")
	}
	c.lock.Lock()
	c.items = make(map[string]item)
	if c.maxEntries > 0 {
		c.order = list.New()
	} else {
		c.order = nil
	}
	c.lock.Unlock()
	return nil
}

func (c *Memory) Inc(key string, step int64) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)

	stored, found := c.items[key]
	if found && !stored.expiry.IsZero() && !now.Before(stored.expiry) {
		c.removeItemLocked(key, stored)
		stored = item{}
		found = false
	}
	current := int64(0)
	if found {
		var err error
		current, err = strictCounterValue(stored.value)
		if err != nil {
			return 0, err
		}
	}
	updated, err := checkedCounterAdd(current, step)
	if err != nil {
		return 0, err
	}
	c.sequence++
	if found {
		stored.value = updated
		stored.version = c.sequence
		c.items[key] = stored
	} else {
		if err = c.storeItemLocked(key, updated, time.Time{}, c.sequence, isMetadataKey(key)); err != nil {
			return 0, err
		}
	}
	return updated, nil
}

func (c *Memory) Dec(key string, step int64) (int64, error) {
	if c == nil {
		return 0, fmt.Errorf("内存缓存驱动为空")
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)

	stored, found := c.items[key]
	if found && !stored.expiry.IsZero() && !now.Before(stored.expiry) {
		c.removeItemLocked(key, stored)
		stored = item{}
		found = false
	}
	current := int64(0)
	if found {
		var err error
		current, err = strictCounterValue(stored.value)
		if err != nil {
			return 0, err
		}
	}
	updated, err := checkedCounterSubtract(current, step)
	if err != nil {
		return 0, err
	}
	c.sequence++
	if found {
		stored.value = updated
		stored.version = c.sequence
		c.items[key] = stored
	} else {
		if err = c.storeItemLocked(key, updated, time.Time{}, c.sequence, isMetadataKey(key)); err != nil {
			return 0, err
		}
	}
	return updated, nil
}

// AcquireLock 获取进程内锁，过期锁会在同一临界区内被替换。
func (c *Memory) AcquireLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureMapsLocked()
	c.sweepExpiredLocked(now)
	if existing, found := c.locks[key]; found && existing.expiry.After(now) {
		return false, nil
	}
	c.locks[key] = memoryLock{owner: owner, expiry: now.Add(ttl)}
	return true, nil
}

// ReleaseLock 仅允许未过期锁的 owner 释放锁。
func (c *Memory) ReleaseLock(key string, owner string) (bool, error) {
	if c == nil || owner == "" {
		return false, ErrInvalidCacheLock
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	existing, found := c.locks[key]
	if !found {
		return false, nil
	}
	if !existing.expiry.After(now) {
		delete(c.locks, key)
		return false, nil
	}
	if existing.owner != owner {
		return false, nil
	}
	delete(c.locks, key)
	return true, nil
}

// RenewLock 仅允许当前 owner 延长未过期的内存锁租约。
func (c *Memory) RenewLock(key string, owner string, ttl time.Duration) (bool, error) {
	if c == nil || owner == "" || ttl <= 0 {
		return false, ErrInvalidCacheLock
	}
	now := time.Now()
	c.lock.Lock()
	defer c.lock.Unlock()
	existing, found := c.locks[key]
	if !found || !existing.expiry.After(now) {
		delete(c.locks, key)
		return false, nil
	}
	if existing.owner != owner {
		return false, nil
	}
	existing.expiry = now.Add(ttl)
	c.locks[key] = existing
	return true, nil
}

// cloneMemoryValue 复制常见可变值，避免缓存内部状态被调用方通过 map、slice 或指针旁路修改。
func cloneMemoryValue(value interface{}) (cloned interface{}) {
	cloned = value
	defer func() {
		if recover() != nil {
			cloned = value
		}
	}()
	if value == nil {
		return nil
	}
	switch value.(type) {
	case bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, uintptr,
		float32, float64, complex64, complex128, time.Time:
		return value
	}
	result := cloneMemoryReflect(reflect.ValueOf(value), make(map[memoryCloneVisit]reflect.Value))
	if !result.IsValid() {
		return nil
	}
	return result.Interface()
}

func cloneMemoryReflect(value reflect.Value, visited map[memoryCloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneMemoryReflect(value.Elem(), visited)
		result := reflect.New(value.Type()).Elem()
		if cloned.IsValid() && cloned.Type().AssignableTo(value.Type()) {
			result.Set(cloned)
		}
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := memoryCloneVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		result := reflect.New(value.Type().Elem())
		visited[visit] = result
		result.Elem().Set(cloneMemoryReflect(value.Elem(), visited))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := memoryCloneVisit{typ: value.Type(), kind: value.Kind(), pointer: value.Pointer()}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = result
		iter := value.MapRange()
		for iter.Next() {
			result.SetMapIndex(iter.Key(), cloneMemoryReflect(iter.Value(), visited))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := memoryCloneVisit{
			typ:      value.Type(),
			kind:     value.Kind(),
			pointer:  value.Pointer(),
			length:   value.Len(),
			capacity: value.Cap(),
		}
		if cloned, exists := visited[visit]; exists {
			return cloned
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		visited[visit] = result
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMemoryReflect(value.Index(index), visited))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(cloneMemoryReflect(value.Index(index), visited))
		}
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if !result.Field(index).CanSet() {
				continue
			}
			result.Field(index).Set(cloneMemoryReflect(value.Field(index), visited))
		}
		return result
	default:
		return value
	}
}

func (c *Memory) ensureMapsLocked() {
	if c.items == nil {
		c.items = make(map[string]item)
	}
	if c.locks == nil {
		c.locks = make(map[string]memoryLock)
	}
	if c.maxEntries > 0 && c.order == nil {
		c.order = list.New()
		for key, stored := range c.items {
			stored.order = c.order.PushBack(key)
			c.items[key] = stored
		}
	}
}

// storeItemLocked 写入缓存项并按 FIFO 顺序淘汰最早的业务项；调用方必须持有写锁。
func (c *Memory) storeItemLocked(key string, value interface{}, expiry time.Time, version uint64, protected bool) error {
	if stored, exists := c.items[key]; exists {
		stored.value = value
		stored.expiry = expiry
		stored.version = version
		stored.protected = stored.protected || protected
		c.items[key] = stored
		return nil
	}
	if c.maxEntries <= 0 {
		c.items[key] = item{value: value, expiry: expiry, version: version, protected: protected}
		return nil
	}
	if protected {
		if len(c.items) >= c.maxEntries {
			return ErrMemoryCapacityExhausted
		}
	} else {
		for len(c.items) >= c.maxEntries {
			oldestKey, oldestItem, found := c.oldestEvictableItemLocked()
			if !found {
				return ErrMemoryCapacityExhausted
			}
			c.removeItemLocked(oldestKey, oldestItem)
		}
	}
	stored := item{value: value, expiry: expiry, version: version, protected: protected}
	stored.order = c.order.PushBack(key)
	c.items[key] = stored
	return nil
}

func (c *Memory) oldestEvictableItemLocked() (string, item, bool) {
	for element := c.order.Front(); element != nil; element = element.Next() {
		key, ok := element.Value.(string)
		if !ok {
			continue
		}
		stored, exists := c.items[key]
		if exists && !stored.protected {
			return key, stored, true
		}
	}
	return "", item{}, false
}

func isMetadataKey(key string) bool {
	return strings.HasPrefix(key, cacheTagMetadataPrefix) || strings.HasPrefix(key, cacheTagReverseMetadataPrefix)
}

// removeItemLocked 删除缓存项及其 FIFO 索引；调用方必须持有写锁。
func (c *Memory) removeItemLocked(key string, stored item) {
	if stored.order != nil && c.order != nil {
		c.order.Remove(stored.order)
	}
	delete(c.items, key)
}

func (c *Memory) sweepExpiredLocked(now time.Time) {
	if !c.lastSweep.IsZero() && now.Sub(c.lastSweep) < memorySweepInterval {
		return
	}
	for key, stored := range c.items {
		if !stored.expiry.IsZero() && !now.Before(stored.expiry) {
			c.removeItemLocked(key, stored)
		}
	}
	for key, lock := range c.locks {
		if !lock.expiry.After(now) {
			delete(c.locks, key)
		}
	}
	c.lastSweep = now
}
