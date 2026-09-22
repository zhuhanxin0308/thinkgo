//go:build cgo

package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/db/builder"
)

// TestSQLiteCountersAndGenerationCleanup 使用两个真实连接池验证跨实例计数和旧代清理。
func TestSQLiteCountersAndGenerationCleanup(t *testing.T) {
	databasePath := filepath.ToSlash(filepath.Join(t.TempDir(), "cache.db"))
	if filepath.VolumeName(databasePath) != "" {
		databasePath = "/" + databasePath
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath}).String() + "?_busy_timeout=10000&_journal_mode=WAL"
	drivers := make([]*DB, 2)
	for index := range drivers {
		pool, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(4)
		connection := db.NewSQLConnection(pool, &builder.Sqlite{})
		t.Cleanup(func() {
			if err := connection.Close(); err != nil {
				t.Error(err)
			}
		})
		drivers[index], err = NewDB(connection, "think_cache")
		if err != nil {
			t.Fatal(err)
		}
		if err := drivers[index].EnsureTable(); err != nil {
			t.Fatal(err)
		}
	}
	const workers, iterations = 8, 20
	var group sync.WaitGroup
	errors := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		group.Go(func() {
			for iteration := 0; iteration < iterations; iteration++ {
				if _, err := drivers[worker%len(drivers)].Inc("shared", 1); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	value, found, err := drivers[0].Get("shared")
	if err != nil || !found || value != float64(workers*iterations) {
		t.Fatalf("真实数据库丢失更新: %v %v %v", value, found, err)
	}
	if acquired, err := drivers[0].AcquireLock("business", "owner", time.Minute); err != nil || !acquired {
		t.Fatalf("业务租约失败: %v %v", acquired, err)
	}
	if err := drivers[1].Clear(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := drivers[0].Get("shared"); err != nil || found {
		t.Fatalf("旧代仍可见: %v %v", found, err)
	}
	if acquired, err := drivers[1].AcquireLock("business", "other", time.Minute); err != nil || acquired {
		t.Fatalf("Clear 破坏了业务租约: %v %v", acquired, err)
	}
	for index, driver := range drivers {
		if err := driver.Set(fmt.Sprint(index), index, 0); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.NewDB(drivers[0].conn).Table("think_cache").WhereLike("key", dbDataStoragePrefix+"%").Select()
	if err != nil || len(rows) != len(drivers) {
		t.Fatalf("旧代未回收或新代被误删: %#v %v", rows, err)
	}
}
