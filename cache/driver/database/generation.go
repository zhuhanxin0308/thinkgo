package database

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

const (
	dbGenerationKey           = "__thinkgo_db_generation__"
	dbDataStoragePrefix       = "__thinkgo_db_data__:"
	dbGenerationWidth         = 19
	dbInitialGeneration int64 = 1
	dbCompareAttempts         = 128
	dbCompareWait             = 2 * time.Second
)

// currentGeneration 读取共享代号，所有进程均从数据库确认，不缓存可变代号。
func (c *DB) currentGeneration() (int64, error) {
	if c == nil || c.conn == nil {
		return 0, ErrInvalidCacheDatabase
	}
	query := db.NewDB(c.conn).Table(c.table).Where("key", dbGenerationKey)
	row, err := query.Find()
	if err != nil {
		return 0, err
	}
	if row == nil {
		_, insertErr := db.NewDB(c.conn).Table(c.table).Insert(map[string]interface{}{
			"key": dbGenerationKey, "value": strconv.FormatInt(dbInitialGeneration, 10), "expiry": int64(0),
		})
		if insertErr == nil {
			return dbInitialGeneration, nil
		}
		row, err = query.Find()
		if err != nil || row == nil {
			return 0, errors.Join(insertErr, err)
		}
	}
	encoded, err := databaseCacheBytes(row["value"])
	if err != nil {
		return 0, err
	}
	generation, err := strconv.ParseInt(string(encoded), 10, 64)
	if err != nil || generation < dbInitialGeneration || strconv.FormatInt(generation, 10) != string(encoded) {
		return 0, fmt.Errorf("%w: 缓存代号非法", ErrCorruptCacheEntry)
	}
	return generation, nil
}

func generationPrefix(generation int64) string {
	return fmt.Sprintf("%s%0*d:", dbDataStoragePrefix, dbGenerationWidth, generation)
}

func (c *DB) storageKey(key string) (string, error) {
	if err := c.validateKey(key); err != nil {
		return "", err
	}
	generation, err := c.currentGeneration()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(key))
	return generationPrefix(generation) + hex.EncodeToString(digest[:]), nil
}

// compareValue 使用读取到的值进行原子比较更新，Oracle 的 CLOB 使用其明确的比较能力。
func (c *DB) compareValue(key string, encoded string) *db.Query {
	query := db.NewDB(c.conn).Table(c.table).Where("key", key)
	if connection, ok := c.conn.(*db.SQLConnection); ok && connection.Builder != nil && connection.Builder.DialectName() == "oracle" {
		return query.WhereRaw(fmt.Sprintf("DBMS_LOB.COMPARE(%s, TO_CLOB(?)) = 0", c.quoteIdentifier("value")), encoded)
	}
	return query.Where("value", encoded)
}

// clearGeneration 的线性化点是代号的条件更新，普通写入只影响其开始时读取的那一代。
// 定宽十进制代号保证字典序等于数值序，清理旧代不会误删其他 Clear 创建的新代。
func (c *DB) clearGeneration() error {
	deadline := time.Now().Add(dbCompareWait)
	for attempt := 0; attempt < dbCompareAttempts && time.Now().Before(deadline); attempt++ {
		generation, err := c.currentGeneration()
		if err != nil {
			return err
		}
		if generation == math.MaxInt64 {
			return fmt.Errorf("%w: 缓存代号已耗尽", ErrCorruptCacheEntry)
		}
		next := generation + 1
		updated, err := c.compareValue(dbGenerationKey, strconv.FormatInt(generation, 10)).Update(map[string]interface{}{"value": strconv.FormatInt(next, 10)})
		if err != nil {
			return err
		}
		if updated == 0 {
			continue
		}
		_, err = db.NewDB(c.conn).Table(c.table).WhereLike("key", dbDataStoragePrefix+"%").Where("key", "<", generationPrefix(next)).Delete()
		return err
	}
	return ErrCacheLockBusy
}
