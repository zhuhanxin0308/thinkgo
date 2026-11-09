//go:build cgo

package database

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/builder"
)

// TestSQLiteClearAcrossProcesses 确认迟到写入回收不依赖同进程锁或内存代际缓存。
func TestSQLiteClearAcrossProcesses(t *testing.T) {
	const childDatabase = "THINKGO_GENERATION_CHILD_DATABASE"
	if dsn := os.Getenv(childDatabase); dsn != "" {
		pool, err := sql.Open("sqlite3", dsn)
		if err != nil {
			t.Fatal(err)
		}
		connection := db.NewSQLConnection(pool, &builder.Sqlite{})
		defer connection.Close()
		backend, err := NewDB(connection, "generation_cache")
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.Clear(); err != nil {
			t.Fatal(err)
		}
		if err := backend.Set("value", "child-generation", 0); err != nil {
			t.Fatal(err)
		}
		return
	}
	writer, reader, barrier := newGenerationInterleavingDrivers(t, "")
	var sequence int
	var name, path string
	if err := reader.conn.(*db.SQLConnection).DB.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	// file URI 的盘符必须放在绝对路径中，不能被解释成主机名；反斜杠先按平台转换。
	databasePath := filepath.ToSlash(path)
	if !strings.HasPrefix(databasePath, "/") {
		databasePath = "/" + databasePath
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath}).String() + "?_busy_timeout=10000&_journal_mode=WAL"
	barrier.armed.Store(true)
	finished := make(chan error, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(barrier.release) }) }
	defer release()
	go func() { finished <- writer.Set("value", "old-generation", 0) }()
	const waitBudget = 10 * time.Second
	select {
	case <-barrier.entered:
	case <-time.After(waitBudget):
		t.Fatal("父进程写入未读取旧代")
	}
	ctx, cancel := context.WithTimeout(t.Context(), waitBudget)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteClearAcrossProcesses$")
	command.Env = append(os.Environ(), childDatabase+"="+dsn)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("独立进程 Clear 失败: %v, %s", err, output)
	}
	release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if value, found, err := reader.Get("value"); err != nil || !found || value != "child-generation" {
		t.Fatalf("父进程迟到写入破坏新代: %v, %t, %v", value, found, err)
	}
	rows, err := db.NewDB(reader.conn).Table(reader.table).WhereLike("key", dbDataStoragePrefix+"%").Select()
	if err != nil || len(rows) != 1 {
		t.Fatalf("跨进程清理后仍残留不可达行: 行数=%d, 错误=%v", len(rows), err)
	}
}
