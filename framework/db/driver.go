package db

import (
	"fmt"
	"sync"
)

var (
	connectors = make(map[string]Connector)
	lock       sync.RWMutex
)

// RegisterConnector registers a database connector
func RegisterConnector(name string, connector Connector) {
	lock.Lock()
	defer lock.Unlock()
	connectors[name] = connector
}

// GetConnector gets a database connector
func GetConnector(name string) (Connector, error) {
	lock.RLock()
	defer lock.RUnlock()
	if connector, ok := connectors[name]; ok {
		return connector, nil
	}
	return nil, fmt.Errorf("connector not found: %s", name)
}
