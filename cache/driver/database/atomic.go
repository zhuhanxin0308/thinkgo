package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// Update 通过乐观条件比较完成数据库缓存的单键原子读改写。
func (c *DB) Update(key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	return c.UpdateContext(context.Background(), key, ttl, update)
}

// UpdateContext 在支持请求取消的前提下执行数据库缓存单键原子更新。
func (c *DB) UpdateContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool) (interface{}, bool, error)) error {
	if update == nil {
		return ErrNilAtomicUpdate
	}
	return c.UpdateWithExpiryContext(ctx, key, ttl, func(value interface{}, found bool, _ time.Time) (interface{}, bool, *time.Time, error) {
		next, remove, err := update(value, found)
		return next, remove, nil, err
	})
}

// UpdatePreserveTTL 原子更新值并保留未过期记录原有的绝对过期时间。
func (c *DB) UpdatePreserveTTL(key string, update func(interface{}, bool) (interface{}, bool, error)) error {
	if update == nil {
		return ErrNilAtomicUpdate
	}
	return c.UpdatePreserveTTLConditionally(key, func(value interface{}, found bool) (interface{}, bool, bool, error) {
		next, remove, err := update(value, found)
		return next, remove, true, err
	})
}

// UpdatePreserveTTLConditionally 在同一次条件提交中原子决定保留原过期时间或切换为永久项。
func (c *DB) UpdatePreserveTTLConditionally(key string, update func(interface{}, bool) (interface{}, bool, bool, error)) error {
	if update == nil {
		return ErrNilAtomicUpdate
	}
	return c.UpdateWithExpiryContext(context.Background(), key, 0, func(value interface{}, found bool, expiry time.Time) (interface{}, bool, *time.Time, error) {
		next, remove, preserve, err := update(value, found)
		var override *time.Time
		if preserve && found {
			override = &expiry
		}
		return next, remove, override, err
	})
}

// UpdateWithExpiryContext 在乐观条件比较边界内读取值与过期时间，并原子提交更新或删除。
func (c *DB) UpdateWithExpiryContext(ctx context.Context, key string, ttl time.Duration, update func(interface{}, bool, time.Time) (interface{}, bool, *time.Time, error)) error {
	if c == nil || c.conn == nil {
		return ErrInvalidCacheDatabase
	}
	if ctx == nil || update == nil {
		return ErrNilAtomicUpdate
	}
	if err := validateDriverTTL(ttl); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	physicalKey, err := c.storageKey(key)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(dbCompareWait)
	for attempt := 0; attempt < dbCompareAttempts && (attempt == 0 || time.Now().Before(deadline)); attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := db.NewDB(c.conn).Table(c.table).WithContext(ctx).Where("key", physicalKey).Find()
		if err != nil {
			return err
		}
		var (
			found          bool
			current        interface{}
			originalExpiry time.Time
			rawPrevious    []byte
		)
		if row != nil {
			expiryMillis, parseErr := parseDBExpiry(row["expiry"])
			if parseErr != nil {
				return parseErr
			}
			rawPrevious, err = databaseCacheBytes(row["value"])
			if err != nil {
				return err
			}
			if !cacheExpiryReached(expiryMillis, time.Now()) {
				found = true
				originalExpiry = dbExpiryTime(expiryMillis)
				current, err = unmarshalDBCacheValue(rawPrevious)
				if err != nil {
					return err
				}
			}
		}
		next, remove, expiryOverride, err := update(current, found, originalExpiry)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		targetExpiry := int64(0)
		if !remove {
			if expiryOverride != nil {
				if !expiryOverride.IsZero() {
					if !expiryOverride.After(time.Now()) {
						remove = true
					} else {
						targetExpiry = dbInstantMillis(*expiryOverride)
					}
				}
			} else if ttl > 0 {
				targetExpiry = dbExpiryMillis(time.Now(), ttl)
			}
		}
		if remove {
			if row == nil {
				return nil
			}
			deleted, delErr := c.compareValue(physicalKey, string(rawPrevious)).WithContext(ctx).Where("expiry", row["expiry"]).Delete()
			if delErr != nil {
				return delErr
			}
			if deleted > 0 {
				return nil
			}
			after, checkErr := db.NewDB(c.conn).Table(c.table).WithContext(ctx).Where("key", physicalKey).Find()
			if checkErr != nil {
				return checkErr
			}
			if after == nil {
				return nil
			}
			continue
		}
		encoded, err := marshalDBCacheValue(next)
		if err != nil {
			return err
		}
		if row == nil {
			data := map[string]interface{}{"key": physicalKey, "value": encoded, "expiry": targetExpiry}
			if _, insertErr := db.NewDB(c.conn).Table(c.table).WithContext(ctx).Insert(data); insertErr == nil {
				return c.reclaimObsoleteWrite(physicalKey)
			} else {
				after, checkErr := db.NewDB(c.conn).Table(c.table).WithContext(ctx).Where("key", physicalKey).Find()
				if checkErr != nil || after == nil {
					return errors.Join(insertErr, checkErr)
				}
				continue
			}
		}
		updated, updErr := c.compareValue(physicalKey, string(rawPrevious)).WithContext(ctx).Where("expiry", row["expiry"]).Update(map[string]interface{}{
			"value":  encoded,
			"expiry": targetExpiry,
		})
		if updErr != nil {
			return updErr
		}
		if updated > 0 {
			return c.reclaimObsoleteWrite(physicalKey)
		}
		if storedRowMatches(row, encoded, targetExpiry) {
			after, checkErr := db.NewDB(c.conn).Table(c.table).WithContext(ctx).Where("key", physicalKey).Find()
			if checkErr != nil {
				return checkErr
			}
			if after != nil && storedRowMatches(after, encoded, targetExpiry) {
				return c.reclaimObsoleteWrite(physicalKey)
			}
		}
	}
	return fmt.Errorf("%w: 数据库原子更新重试超过 %d 次", ErrAtomicUpdateConflict, dbCompareAttempts)
}

func dbExpiryTime(expiryMillis int64) time.Time {
	if expiryMillis <= 0 {
		return time.Time{}
	}
	if expiryMillis < millisecondEpochBoundary {
		return time.Unix(expiryMillis, 0).UTC()
	}
	return time.UnixMilli(expiryMillis).UTC()
}
