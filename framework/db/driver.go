package db

import (
	"fmt"
	"reflect"
	"regexp"
	"sync"
)

var (
	connectors           = make(map[string]Connector)
	lock                 sync.RWMutex
	connectorNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)
)

// RegisterConnector 注册命名连接器，拒绝非法名称、类型化 nil 和静默覆盖。
func RegisterConnector(name string, connector Connector) error {
	if !connectorNamePattern.MatchString(name) || isNilDatabaseDependency(connector) {
		return fmt.Errorf("%w: %q", ErrInvalidConnector, name)
	}
	lock.Lock()
	defer lock.Unlock()
	if _, exists := connectors[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateConnector, name)
	}
	connectors[name] = connector
	return nil
}

// GetConnector 返回已注册连接器。
func GetConnector(name string) (Connector, error) {
	if !connectorNamePattern.MatchString(name) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidConnector, name)
	}
	lock.RLock()
	defer lock.RUnlock()
	if connector, ok := connectors[name]; ok {
		return connector, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrConnectorNotFound, name)
}

func isNilDatabaseDependency(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
