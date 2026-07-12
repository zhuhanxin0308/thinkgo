package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"time"

	"thinkgo/framework/db"
)

const (
	maxDBCacheKeyBytes       = 255
	millisecondEpochBoundary = int64(1_000_000_000_000)
)

var cacheTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DB 是显式传播 SQL 错误并保留毫秒 TTL 的数据库缓存驱动。
type DB struct {
	conn  db.Connection
	table string
	incMu sync.Mutex
}

// NewDB 验证连接与缓存表名后创建驱动。
func NewDB(conn db.Connection, table string) (*DB, error) {
	if conn == nil {
		return nil, ErrInvalidCacheDatabase
	}
	if !cacheTablePattern.MatchString(table) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidCacheTable, table)
	}
	return &DB{conn: conn, table: table}, nil
}

// EnsureTable 确保缓存表存在，并按底层 SQL 方言生成安全建表语句。
func (c *DB) EnsureTable() error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	raw, ok := c.conn.(db.RawQueryable)
	if !ok {
		return fmt.Errorf("%w: 连接不支持原生执行", ErrInvalidCacheDatabase)
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

func (c *DB) Get(key string) (interface{}, bool, error) {
	value, found, _, err := c.getStored(key, false)
	return value, found, err
}

func (c *DB) Set(key string, value interface{}, ttl time.Duration) error {
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	expiry := int64(0)
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UnixMilli()
	}
	return c.writeStored(key, value, expiry)
}

func (c *DB) Has(key string) (bool, error) {
	_, found, err := c.Get(key)
	return found, err
}

func (c *DB) Delete(key string) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	_, err := db.NewDB(c.conn).Table(c.table).Where("key", key).Delete()
	return err
}

func (c *DB) Clear() error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	_, err := db.NewDB(c.conn).Table(c.table).WhereRaw("1 = 1").Delete()
	return err
}

func (c *DB) Inc(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, false)
}

func (c *DB) Dec(key string, step int64) (int64, error) {
	return c.changeCounter(key, step, true)
}

func (c *DB) changeCounter(key string, step int64, subtract bool) (int64, error) {
	if c == nil || c.conn == nil {
		return 0, ErrInvalidCacheDatabase
	}
	if err := validateCounterStep(step); err != nil {
		return 0, err
	}
	c.incMu.Lock()
	defer c.incMu.Unlock()
	value, found, expiry, err := c.getStored(key, true)
	if err != nil {
		return 0, err
	}
	current := int64(0)
	if found {
		current, err = strictCounterValue(value)
		if err != nil {
			return 0, err
		}
	}
	updated := int64(0)
	if subtract {
		updated, err = checkedCounterSubtract(current, step)
	} else {
		updated, err = checkedCounterAdd(current, step)
	}
	if err != nil {
		return 0, err
	}
	if err = c.writeStored(key, updated, expiry); err != nil {
		return 0, err
	}
	return updated, nil
}

func (c *DB) getStored(key string, preserveNumber bool) (interface{}, bool, int64, error) {
	if err := c.validateKey(key); err != nil {
		return nil, false, 0, err
	}
	result, err := db.NewDB(c.conn).Table(c.table).Where("key", key).Find()
	if err != nil || result == nil {
		return nil, false, 0, err
	}
	expiry, err := parseDBExpiry(result["expiry"])
	if err != nil {
		return nil, false, 0, err
	}
	if cacheExpiryReached(expiry, time.Now()) {
		// 过期读取与并发刷新可能交错，必须按读取到的旧 expiry 条件删除。
		if deleteErr := c.deleteExpiredVersion(key, result["expiry"]); deleteErr != nil {
			return nil, false, 0, deleteErr
		}
		return nil, false, 0, nil
	}
	encoded, err := databaseCacheBytes(result["value"])
	if err != nil {
		return nil, false, 0, err
	}
	var value interface{}
	if preserveNumber {
		value, err = decodeCounterJSON(encoded)
	} else {
		err = json.Unmarshal(encoded, &value)
	}
	if err != nil {
		return nil, false, 0, fmt.Errorf("%w: %v", ErrCorruptCacheEntry, err)
	}
	return value, true, expiry, nil
}

func (c *DB) deleteExpiredVersion(key string, rawExpiry interface{}) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	_, err := db.NewDB(c.conn).
		Table(c.table).
		Where("key", key).
		Where("expiry", rawExpiry).
		Delete()
	return err
}

func (c *DB) writeStored(key string, value interface{}, expiry int64) error {
	if err := c.validateKey(key); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data := map[string]interface{}{"key": key, "value": string(encoded), "expiry": expiry}
	query := db.NewDB(c.conn).Table(c.table).Where("key", key)
	updated, updateErr := query.Update(data)
	if updateErr != nil {
		return updateErr
	}
	if updated > 0 {
		return nil
	}
	if _, insertErr := db.NewDB(c.conn).Table(c.table).Insert(data); insertErr == nil {
		return nil
	} else {
		// 并发首次写入可能由另一进程先插入；重试更新可安全完成 upsert。
		retried, retryErr := db.NewDB(c.conn).Table(c.table).Where("key", key).Update(data)
		if retryErr == nil && retried > 0 {
			return nil
		}
		return errors.Join(insertErr, retryErr)
	}
}

func (c *DB) validateKey(key string) error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	if len(key) > maxDBCacheKeyBytes {
		return fmt.Errorf("%w: %d", ErrCacheKeyTooLong, len(key))
	}
	return nil
}

func parseDBExpiry(raw interface{}) (int64, error) {
	switch typed := raw.(type) {
	case nil:
		return 0, nil
	case string:
		value, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: expiry", ErrCorruptCacheEntry)
		}
		return validateDBExpiry(value)
	case []byte:
		return parseDBExpiry(string(typed))
	default:
		value, err := strictCounterValue(raw)
		if err != nil {
			return 0, fmt.Errorf("%w: expiry: %v", ErrCorruptCacheEntry, err)
		}
		return validateDBExpiry(value)
	}
}

func validateDBExpiry(value int64) (int64, error) {
	if value < 0 {
		return 0, fmt.Errorf("%w: expiry 不能为负数", ErrCorruptCacheEntry)
	}
	return value, nil
}

func databaseCacheBytes(raw interface{}) ([]byte, error) {
	switch typed := raw.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return append([]byte(nil), typed...), nil
	default:
		return nil, fmt.Errorf("%w: value 类型 %T", ErrCorruptCacheEntry, raw)
	}
}

func cacheExpiryReached(expiry int64, now time.Time) bool {
	if expiry <= 0 {
		return false
	}
	if expiry < millisecondEpochBoundary {
		return !now.Before(time.Unix(expiry, 0))
	}
	return !now.Before(time.UnixMilli(expiry))
}
