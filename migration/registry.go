package migration

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// Registry 保存单个应用的迁移定义，并在首次执行前冻结。
type Registry struct {
	mu         sync.RWMutex
	migrations map[string]Migration
	frozen     bool
}

// NewRegistry 创建空的应用级迁移注册表。
func NewRegistry() *Registry {
	return &Registry{migrations: make(map[string]Migration)}
}

// Register 注册唯一迁移；运行器取得快照后禁止继续修改。
func (registry *Registry) Register(current Migration) error {
	if registry == nil || isNilMigration(current) {
		return fmt.Errorf("%w: 迁移定义为空", ErrInvalidMigration)
	}
	if !migrationNamePattern.MatchString(current.Name()) || !migrationChecksumPattern.MatchString(current.Checksum()) {
		return fmt.Errorf("%w: 迁移 %q 的名称或校验和非法", ErrInvalidMigration, current.Name())
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return ErrMigrationRegistryFrozen
	}
	if _, exists := registry.migrations[current.Name()]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateMigration, current.Name())
	}
	registry.migrations[current.Name()] = current
	return nil
}

func (registry *Registry) freezeSnapshot() ([]Migration, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: 迁移注册表为空", ErrInvalidMigration)
	}
	registry.mu.Lock()
	registry.frozen = true
	result := make([]Migration, 0, len(registry.migrations))
	for _, current := range registry.migrations {
		result = append(result, current)
	}
	registry.mu.Unlock()
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name() < result[right].Name()
	})
	return result, nil
}

func isNilMigration(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
