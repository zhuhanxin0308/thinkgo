package driver

import (
	"encoding/json"
	"thinkgo/framework/db"
	"time"
)

// DB cache driver
type DB struct {
	conn  db.Connection
	table string
}

// NewDB creates a new DB driver
func NewDB(conn db.Connection, table string) *DB {
	return &DB{
		conn:  conn,
		table: table,
	}
}

// EnsureTable creates the cache table if not exists
func (c *DB) EnsureTable() {
	// Simple check, in real app should be migration
	// CREATE TABLE cache (key VARCHAR(255) PRIMARY KEY, value TEXT, expiry BIGINT)
}

func (c *DB) Get(key string) interface{} {
	// Create a new DB instance for each query to ensure thread safety regarding query state
	database := db.NewDB(c.conn)
	res, err := database.Table(c.table).Where("key", key).Find()
	if err != nil || res == nil {
		return nil
	}

	expiry := int64(res["expiry"].(float64)) // JSON numbers are float64
	if expiry > 0 && time.Now().Unix() > expiry {
		c.Delete(key)
		return nil
	}

	var val interface{}
	json.Unmarshal([]byte(res["value"].(string)), &val)
	return val
}

func (c *DB) Set(key string, val interface{}, ttl time.Duration) {
	expiry := int64(0)
	if ttl > 0 {
		expiry = time.Now().Add(ttl).Unix()
	}

	value, _ := json.Marshal(val)
	
	data := map[string]interface{}{
		"key":    key,
		"value":  string(value),
		"expiry": expiry,
	}

	if c.Has(key) {
		db.NewDB(c.conn).Table(c.table).Where("key", key).Update(data)
	} else {
		db.NewDB(c.conn).Table(c.table).Insert(data)
	}
}

func (c *DB) Has(key string) bool {
	count, _ := db.NewDB(c.conn).Table(c.table).Where("key", key).Count()
	return count > 0
}

func (c *DB) Delete(key string) {
	db.NewDB(c.conn).Table(c.table).Where("key", key).Delete()
}

func (c *DB) Clear() {
	db.NewDB(c.conn).Table(c.table).Delete()
}

func (c *DB) Inc(key string, step int64) int64 {
	// Simplified, not atomic
	val := c.Get(key)
	var intVal int64 = 0
	if val != nil {
		if v, ok := val.(float64); ok {
			intVal = int64(v)
		} else if v, ok := val.(int64); ok {
			intVal = v
		}
	}
	intVal += step
	c.Set(key, intVal, 0)
	return intVal
}

func (c *DB) Dec(key string, step int64) int64 {
	return c.Inc(key, -step)
}
