package driver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"thinkgo/framework/db"
	"time"
)

var cacheTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DB 是基于数据库表的缓存驱动。
type DB struct {
	conn  db.Connection
	table string
	incMu sync.Mutex // 保护 Inc/Dec 的读改写，保证单进程内不丢增量
}

// NewDB 创建 DB 缓存驱动实例。
func NewDB(conn db.Connection, table string) *DB {
	return &DB{
		conn:  conn,
		table: table,
	}
}

// EnsureTable 确保缓存表存在，并按底层 SQL 方言生成安全的建表语句。
func (c *DB) EnsureTable() error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("cache database connection is nil")
	}
	if !cacheTablePattern.MatchString(c.table) {
		return fmt.Errorf("unsafe cache table name: %s", c.table)
	}
	raw, ok := c.conn.(db.RawQueryable)
	if !ok {
		return fmt.Errorf("cache database connection does not support raw execution")
	}
	_, err := raw.Execute(c.createTableSQL())
	return err
}

func (c *DB) createTableSQL() string {
	quote := c.quoteIdentifier
	table := quote(c.table)
	keyColumn := quote("key")
	valueColumn := quote("value")
	expiryColumn := quote("expiry")

	if sqlConn, ok := c.conn.(*db.SQLConnection); ok && sqlConn.Builder != nil {
		switch fmt.Sprintf("%T", sqlConn.Builder) {
		case "*builder.Sqlsrv":
			return fmt.Sprintf(
				"IF OBJECT_ID(N'%s', N'U') IS NULL BEGIN CREATE TABLE %s (%s NVARCHAR(255) NOT NULL PRIMARY KEY, %s NVARCHAR(MAX) NOT NULL, %s BIGINT NOT NULL DEFAULT 0) END",
				c.table, table, keyColumn, valueColumn, expiryColumn,
			)
		case "*builder.Oracle":
			return fmt.Sprintf(
				"BEGIN EXECUTE IMMEDIATE 'CREATE TABLE %s (%s VARCHAR2(255) PRIMARY KEY, %s CLOB NOT NULL, %s NUMBER(19) DEFAULT 0 NOT NULL)'; EXCEPTION WHEN OTHERS THEN IF SQLCODE != -955 THEN RAISE; END IF; END;",
				table, keyColumn, valueColumn, expiryColumn,
			)
		}
	}

	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s VARCHAR(255) PRIMARY KEY, %s TEXT NOT NULL, %s BIGINT NOT NULL DEFAULT 0)",
		table, keyColumn, valueColumn, expiryColumn,
	)
}

func (c *DB) quoteIdentifier(name string) string {
	if sqlConn, ok := c.conn.(*db.SQLConnection); ok && sqlConn.Builder != nil {
		return sqlConn.Builder.QuoteIdentifier(name)
	}
	return "`" + name + "`"
}

func (c *DB) Get(key string) interface{} {
	// 每次查询创建独立 DB 管理器，避免链式查询状态在并发请求间共享。
	database := db.NewDB(c.conn)
	res, err := database.Table(c.table).Where("key", key).Find()
	if err != nil || res == nil {
		return nil
	}

	// 不同驱动可能把整型列返回为 float64/int64/[]byte/string，统一安全解析，避免类型断言 panic。
	expiry := toInt64(res["expiry"])
	if expiry > 0 && time.Now().Unix() > expiry {
		c.Delete(key)
		return nil
	}

	value, ok := res["value"].(string)
	if !ok {
		return nil
	}
	var val interface{}
	if err := json.Unmarshal([]byte(value), &val); err != nil {
		return nil
	}
	return val
}

// toInt64 把数据库返回的多种数值类型安全转换为 int64。
func toInt64(raw interface{}) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	case []byte:
		var n int64
		fmt.Sscanf(string(v), "%d", &n)
		return n
	case string:
		var n int64
		fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
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
	// Delete() 禁止无 WHERE 条件执行，这里用恒真条件清空全表缓存。
	db.NewDB(c.conn).Table(c.table).WhereRaw("1 = 1").Delete()
}

func (c *DB) Inc(key string, step int64) int64 {
	c.incMu.Lock()
	defer c.incMu.Unlock()

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
