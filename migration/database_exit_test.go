package migration

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type migrationExitObservation struct {
	returned  bool
	continued bool
	recovered interface{}
	err       error
}

// 恢复 nil panic 后内层函数仍会返回；Goexit 只展开 defer，不会恢复到下一条语句。
func observeMigrationExit(t *testing.T, ctx context.Context, operation func() error) migrationExitObservation {
	t.Helper()
	var observation migrationExitObservation
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		func() {
			defer func() { observation.recovered = recover() }()
			observation.err = operation()
			observation.returned = true
		}()
		observation.continued = true
	}()
	select {
	case <-finished:
		return observation
	case <-ctx.Done():
		t.Fatal("迁移异常退出后没有归还执行权")
		return migrationExitObservation{}
	}
}

func exitMigrationCallback(exit string, marker error) error {
	switch exit {
	case "panic":
		panic(marker)
	case "nil_panic":
		panic(nil)
	default:
		runtime.Goexit()
		return nil
	}
}

// 不依赖 CGO 验证异常路径的保存点回滚、解锁失败失效及原始退出方式。
func TestMigrationBoundariesRetainAbnormalExitAndAttemptCleanup(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	const waitLimit = 5 * time.Second
	for _, boundary := range []string{"savepoint", "lock"} {
		for _, exit := range []string{"panic", "nil_panic", "goexit"} {
			t.Run(boundary+"/"+exit, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), waitLimit)
				defer cancel()
				marker := errors.New("原始迁移异常")
				session := &migrationLockSessionProbe{
					dialect: "postgres",
					results: map[string]interface{}{"pg_advisory_lock": nil, "pg_advisory_unlock": false},
					errors:  map[string]error{"RELEASE SAVEPOINT": errors.New("释放保存点失败")},
				}
				store := &DatabaseStore{database: db.NewDB(nil), dialect: "postgres", executor: session}
				observation := observeMigrationExit(t, ctx, func() error {
					if boundary == "savepoint" {
						return store.runSQLiteSavepoint(ctx, func(Executor) error { return exitMigrationCallback(exit, marker) })
					}
					return store.withAcquiredMigrationSession(ctx, session, false, func() (bool, error) {
						return false, exitMigrationCallback(exit, marker)
					})
				})
				if observation.returned || observation.continued != (exit != "goexit") {
					t.Errorf("异常退出被吞掉或误判：%+v", observation)
				}
				if exit == "panic" && observation.recovered != marker || exit != "panic" && observation.recovered != nil {
					t.Errorf("清理错误遮蔽了原始异常：%v", observation.recovered)
				}
				if boundary == "savepoint" {
					want := []string{"SAVEPOINT " + sqliteMigrationSavepoint, "ROLLBACK TO SAVEPOINT " + sqliteMigrationSavepoint, "RELEASE SAVEPOINT " + sqliteMigrationSavepoint}
					if !slices.Equal(session.statements, want) {
						t.Fatalf("保存点清理缺失或乱序：%v", session.statements)
					}
				} else if !session.invalid || strings.Count(strings.Join(session.statements, "\n"), "pg_advisory_unlock") != 1 {
					t.Fatalf("解锁失败后没有失效会话或重复解锁：%v invalid=%t", session.statements, session.invalid)
				}
			})
		}
	}
}

// 保存点清理失败后，即使业务忽略了错误，也不能继续执行迁移或提交外层事务。
func TestMigrationSavepointCleanupFailureStopsFurtherSteps(t *testing.T) {
	for _, statement := range []string{"ROLLBACK TO SAVEPOINT", "RELEASE SAVEPOINT"} {
		t.Run(statement, func(t *testing.T) {
			cleanupErr, businessErr := errors.New("保存点清理失败"), errors.New("迁移步骤失败")
			session := &migrationLockSessionProbe{
				dialect: "sqlite",
				results: map[string]interface{}{"PRAGMA busy_timeout": int64(0), "SELECT fencing_token": int64(1)},
			}
			store := &DatabaseStore{database: db.NewDB(nil), dialect: "sqlite"}
			err := store.withMigrationSession(t.Context(), session, func(locked Store) error {
				bound := locked.(*DatabaseStore)
				session.errors = map[string]error{statement: cleanupErr}
				if err := bound.runAtomic(t.Context(), func(Executor) error { return businessErr }); !errors.Is(err, businessErr) || !errors.Is(err, cleanupErr) {
					t.Errorf("保存点错误没有保留业务和清理原因：%v", err)
				}
				if err := bound.runAtomic(t.Context(), func(Executor) error {
					t.Error("不确定的事务继续执行了迁移回调")
					return nil
				}); !errors.Is(err, cleanupErr) {
					t.Errorf("后续迁移未保留已知的事务故障：%v", err)
				}
				return nil
			})
			if !errors.Is(err, cleanupErr) || slices.Contains(session.statements, "COMMIT") || !slices.Contains(session.statements, "ROLLBACK") || session.invalid {
				t.Errorf("外层未报告原故障并尝试真实回滚：err=%v SQL=%v invalid=%t", err, session.statements, session.invalid)
			}
		})
	}
}
