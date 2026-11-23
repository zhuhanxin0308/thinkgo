//go:build cgo

package migration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// 锁回调及迁移保存点的异常退出不能提交半成品，也不能把旧式 nil panic 当作成功。
func TestSQLiteMigrationAbnormalExitPreservesRollbackAndPanic(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	const waitLimit = 5 * time.Second
	for _, boundary := range []string{"lock", "savepoint"} {
		for _, exit := range []string{"panic", "nil_panic", "goexit"} {
			t.Run(boundary+"/"+exit, func(t *testing.T) {
				database := newSQLiteMigrationDatabase(t)
				if _, err := database.Execute("CREATE TABLE interrupted_rows (id INTEGER PRIMARY KEY)"); err != nil {
					t.Fatal(err)
				}
				store, err := NewDatabaseStore(database)
				if err != nil {
					t.Fatal(err)
				}
				marker := errors.New("迁移写入后异常退出")
				mutate := func(ctx context.Context, executor Executor) error {
					if _, err := executor.ExecuteContext(ctx, "INSERT INTO interrupted_rows (id) VALUES (?)", 1); err != nil {
						return err
					}
					return exitMigrationCallback(exit, marker)
				}
				current, err := New("202611230001_interrupted_write", firstChecksum, mutate, nil)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), waitLimit)
				defer cancel()
				observation := observeMigrationExit(t, ctx, func() error {
					return store.WithMigrationLock(ctx, func(locked Store) error {
						if boundary == "lock" {
							return mutate(ctx, locked.(*DatabaseStore).activeExecutor())
						}
						return locked.Apply(ctx, current, 1)
					})
				})
				if observation.returned || observation.continued != (exit != "goexit") || ctx.Err() != nil {
					t.Errorf("异常退出被吞掉或误判：%+v ctx=%v", observation, ctx.Err())
				}
				if exit == "panic" && observation.recovered != marker || exit != "panic" && observation.recovered != nil {
					t.Errorf("异常值被改变：%v", observation.recovered)
				}
				if rows, err := database.QueryContext(ctx, "SELECT id FROM interrupted_rows"); err != nil || len(rows) != 0 {
					t.Errorf("失败迁移提交了部分业务写入：%v %v", rows, err)
				}
				// 下一次完整迁移锁与普通写入均可成功，说明数据库锁和物理连接已经归还。
				if err := store.WithMigrationLock(ctx, func(Store) error { return nil }); err != nil {
					t.Fatalf("异常收尾后无法再次获取迁移锁：%v", err)
				}
				if _, err := database.ExecuteContext(ctx, "INSERT INTO interrupted_rows (id) VALUES (?)", 2); err != nil {
					t.Fatalf("异常收尾后连接无法复用：%v", err)
				}
			})
		}
	}
}
