package driver

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidMemoryBudget    = errors.New("内存缓存预算非法")
	ErrMemoryValueTooLarge    = errors.New("内存缓存值超过预算")
	ErrUnsupportedMemoryValue = errors.New("内存缓存预算不支持该值类型")
)

// MemoryBudget defines explicit per-driver admission limits. Bytes are conservative
// charges for supported value graphs, not measurements of the Go heap or RSS.
// All limits must be positive. MaxEntries includes tags and fencing records;
// MaxBytes also includes lock keys and owners. A full budget rejects writes.
type MemoryBudget struct {
	MaxEntries    int
	MaxValueBytes uint64
	MaxBytes      uint64
	MaxLocks      int
}

// NewMemoryWithBudget creates a strict cache without changing the legacy FIFO
// constructors. Unsupported graphs fail before being retained or copied.
func NewMemoryWithBudget(budget MemoryBudget) (*Memory, error) {
	if budget.MaxEntries <= 0 || budget.MaxLocks <= 0 || budget.MaxValueBytes == 0 || budget.MaxBytes < budget.MaxValueBytes {
		return nil, ErrInvalidMemoryBudget
	}
	memory, _ := NewMemoryWithMaxEntries(budget.MaxEntries)
	memory.budget = &budget
	return memory, nil
}

const (
	memoryEntryCharge      = uint64(256)
	memoryLockCharge       = uint64(128)
	memoryGraphVisitCharge = uint64(128)
	memoryGraphMaxDepth    = 64
	memoryGraphMaxNodes    = 65536
)

// budgetSnapshot first traverses the graph without allocating its payload, then
// clones it. Callers own the source and must not mutate it concurrently.
func budgetSnapshot(value interface{}, limit uint64) (interface{}, uint64, error) {
	charge, err := budgetValueCharge(value, limit)
	if err != nil {
		return nil, 0, err
	}
	if value == nil {
		return nil, 0, nil
	}
	v := reflect.ValueOf(value)
	cloned := cloneBudgetValue(v, make(map[memoryCloneVisit]reflect.Value))
	return cloned.Interface(), charge, nil
}

func budgetValueCharge(value interface{}, limit uint64) (uint64, error) {
	if value == nil {
		return 0, nil
	}
	v := reflect.ValueOf(value)
	inspection := memoryGraphInspection{limit: limit, seen: make(map[memoryCloneVisit]bool)}
	if err := inspection.add(uint64(v.Type().Size())); err != nil {
		return 0, err
	}
	if err := inspection.inspect(v, 0); err != nil {
		return 0, err
	}
	return inspection.bytes, nil
}

type memoryGraphInspection struct {
	limit, bytes uint64
	nodes        int
	seen         map[memoryCloneVisit]bool
}

func (g *memoryGraphInspection) add(bytes uint64) error {
	if bytes > g.limit-g.bytes {
		return ErrMemoryValueTooLarge
	}
	g.bytes += bytes
	return nil
}

func (g *memoryGraphInspection) inspect(value reflect.Value, depth int) error {
	if !value.IsValid() {
		return nil
	}
	g.nodes++
	if depth > memoryGraphMaxDepth || g.nodes > memoryGraphMaxNodes {
		return ErrMemoryValueTooLarge
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		if err := g.add(uint64(value.Elem().Type().Size())); err != nil {
			return err
		}
		return g.inspect(value.Elem(), depth+1)
	case reflect.String:
		return g.addProduct(value.Len(), 1)
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if value.IsNil() {
			return nil
		}
		visit := budgetVisit(value)
		if g.seen[visit] {
			return nil
		}
		if err := g.add(memoryGraphVisitCharge); err != nil {
			return err
		}
		g.seen[visit] = true
		switch value.Kind() {
		case reflect.Pointer:
			if err := g.add(uint64(value.Type().Elem().Size())); err != nil {
				return err
			}
			return g.inspect(value.Elem(), depth+1)
		case reflect.Slice:
			if err := g.addProduct(value.Len(), uint64(value.Type().Elem().Size())); err != nil {
				return err
			}
			for i := 0; i < value.Len(); i++ {
				if err := g.inspect(value.Index(i), depth+1); err != nil {
					return err
				}
			}
		case reflect.Map:
			if !budgetMapKey(value.Type().Key()) {
				return ErrUnsupportedMemoryValue
			}
			// Includes conservative bucket/entry overhead, even for zero-size values.
			entry := uint64(value.Type().Key().Size()) + uint64(value.Type().Elem().Size()) + 128
			if err := g.addProduct(value.Len(), entry); err != nil {
				return err
			}
			for _, key := range budgetMapKeys(value) {
				if err := g.inspect(key, depth+1); err != nil {
					return err
				}
				if err := g.inspect(value.MapIndex(key), depth+1); err != nil {
					return err
				}
			}
		}
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := g.inspect(value.Index(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).PkgPath != "" {
				if !budgetInlineType(value.Field(i).Type(), depth+1) {
					return ErrUnsupportedMemoryValue
				}
				continue
			}
			if err := g.inspect(value.Field(i), depth+1); err != nil {
				return err
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedMemoryValue, value.Type())
	}
	return nil
}

func (g *memoryGraphInspection) addProduct(length int, size uint64) error {
	if length < 0 {
		return ErrMemoryValueTooLarge
	}
	count := uint64(length)
	if size != 0 && count > (g.limit-g.bytes)/size {
		return ErrMemoryValueTooLarge
	}
	return g.add(count * size)
}

func budgetMapKey(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func budgetInlineType(t reflect.Type, depth int) bool {
	if depth > memoryGraphMaxDepth {
		return false
	}
	if budgetMapKey(t) {
		return t.Kind() != reflect.String
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		return budgetInlineType(t.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if !budgetInlineType(t.Field(i).Type, depth+1) {
				return false
			}
		}
		return true
	}
	return false
}

// Stable scalar-key order gives inspection and cloning identical DFS paths.
// Float and complex keys are unsupported, including non-reflexive NaN keys.
func budgetMapKeys(value reflect.Value) []reflect.Value {
	keys := value.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		switch value.Type().Key().Kind() {
		case reflect.Bool:
			return !keys[i].Bool() && keys[j].Bool()
		case reflect.String:
			return keys[i].String() < keys[j].String()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return keys[i].Int() < keys[j].Int()
		default:
			return keys[i].Uint() < keys[j].Uint()
		}
	})
	return keys
}

func budgetVisit(v reflect.Value) memoryCloneVisit {
	visit := memoryCloneVisit{typ: v.Type(), kind: v.Kind(), pointer: v.Pointer()}
	if v.Kind() == reflect.Slice {
		visit.length, visit.capacity = v.Len(), v.Cap()
	}
	return visit
}

// cloneBudgetValue is used only after successful inspection: every retained
// reference can be independently copied, without the legacy shallow fallback.
func cloneBudgetValue(v reflect.Value, seen map[memoryCloneVisit]reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		result := reflect.New(v.Type()).Elem()
		result.SetString(strings.Clone(v.String()))
		return result
	case reflect.Interface:
		result := reflect.New(v.Type()).Elem()
		if !v.IsNil() {
			result.Set(cloneBudgetValue(v.Elem(), seen))
		}
		return result
	case reflect.Pointer, reflect.Slice, reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		visit := budgetVisit(v)
		if result, ok := seen[visit]; ok {
			return result
		}
		var result reflect.Value
		switch v.Kind() {
		case reflect.Pointer:
			result = reflect.New(v.Type().Elem())
			if result.Type() != v.Type() {
				result = result.Convert(v.Type())
			}
			seen[visit] = result
			result.Elem().Set(cloneBudgetValue(v.Elem(), seen))
		case reflect.Slice:
			result = reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			seen[visit] = result
			for i := 0; i < v.Len(); i++ {
				result.Index(i).Set(cloneBudgetValue(v.Index(i), seen))
			}
		case reflect.Map:
			result = reflect.MakeMapWithSize(v.Type(), v.Len())
			seen[visit] = result
			for _, key := range budgetMapKeys(v) {
				result.SetMapIndex(cloneBudgetValue(key, seen), cloneBudgetValue(v.MapIndex(key), seen))
			}
		}
		return result
	case reflect.Array, reflect.Struct:
		result := reflect.New(v.Type()).Elem()
		result.Set(v)
		if v.Kind() == reflect.Array {
			for i := 0; i < v.Len(); i++ {
				result.Index(i).Set(cloneBudgetValue(v.Index(i), seen))
			}
		} else {
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).PkgPath == "" {
					result.Field(i).Set(cloneBudgetValue(v.Field(i), seen))
				}
			}
		}
		return result
	default:
		return v
	}
}

// storeBudgetItemLocked admits all records, including monotonic fencing metadata.
// Admission never evicts live data; synchronization state cannot be discarded.
func (c *Memory) storeBudgetItemLocked(key string, value interface{}, expiry time.Time, version uint64, protected bool) error {
	c.sweepBudgetExpiredLocked(time.Now())
	previous, exists := c.items[key]
	oldCharge := previous.bytes
	if !exists && len(c.items) >= c.budget.MaxEntries {
		return ErrMemoryCapacityExhausted
	}
	base := memoryEntryCharge + uint64(len(key))
	available := c.budget.MaxBytes - (c.budgetBytes - previous.bytes)
	if base > available {
		return ErrMemoryCapacityExhausted
	}
	valueBytes, err := budgetValueCharge(value, c.budget.MaxValueBytes)
	if err != nil {
		return err
	}
	if valueBytes > available-base {
		return ErrMemoryCapacityExhausted
	}
	var cloned interface{}
	if value != nil {
		cloned = cloneBudgetValue(reflect.ValueOf(value), make(map[memoryCloneVisit]reflect.Value)).Interface()
	}
	charge := base + valueBytes
	key = strings.Clone(key)
	if exists {
		previous.value, previous.expiry, previous.version = cloned, expiry, version
		previous.protected = previous.protected || protected
		previous.bytes = charge
		c.items[key] = previous
	} else {
		stored := item{value: cloned, expiry: expiry, version: version, protected: protected, bytes: charge}
		stored.order = c.order.PushBack(key)
		c.items[key] = stored
		if !isCapacityExemptKey(key) {
			c.capacityEntries++
		}
	}
	c.budgetBytes = c.budgetBytes - oldCharge + charge
	c.noteCapacityExpiryLocked(key, expiry)
	return nil
}

func (c *Memory) sweepBudgetExpiredLocked(now time.Time) {
	c.sweepExpiredItemsLocked(now)
	for key, lease := range c.locks {
		if !lease.expiry.After(now) {
			c.removeLockLocked(key, lease)
		}
	}
}
