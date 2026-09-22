package framework

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/health"
)

type readinessRuntimeConnection struct {
	appFakeManagerConnection
	unavailable atomic.Bool
	checks      atomic.Int32
}

func (connection *readinessRuntimeConnection) PingContext(ctx context.Context) error {
	connection.checks.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if connection.unavailable.Load() {
		return errors.New("数据库暂时不可用")
	}
	return nil
}

// TestDatabaseReadinessTracksRuntimeRecovery 验证启动成功后的断连与恢复都由当前探针决定。
func TestDatabaseReadinessTracksRuntimeRecovery(t *testing.T) {
	backend := &readinessRuntimeConnection{appFakeManagerConnection: appFakeManagerConnection{identity: db.NewConnectionID("runtime-readiness")}}
	manager := db.NewManager("primary")
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Add("primary", db.NewDB(backend)); err != nil {
		t.Fatal(err)
	}
	app := &App{health: health.NewRegistry(), dbManager: manager, databaseStartupPolicy: DatabaseStartupRequired}
	app.registerDatabaseReadinessCheck()
	for _, unavailable := range []bool{false, true, false} {
		backend.unavailable.Store(unavailable)
		if report := app.health.Readiness(context.Background()); report.Healthy() == unavailable {
			t.Fatalf("readiness 没有反映当前可达性: unavailable=%t report=%#v", unavailable, report)
		}
	}
	if backend.checks.Load() != 3 {
		t.Fatalf("必须每次检查当前连接，实际探测 %d 次", backend.checks.Load())
	}
}

// TestDatabaseReadinessRecoversWithoutBusinessTraffic 验证被流量摘除后仅靠探针也能恢复惰性连接。
func TestDatabaseReadinessRecoversWithoutBusinessTraffic(t *testing.T) {
	for _, policy := range []DatabaseStartupPolicy{DatabaseStartupLazy, DatabaseStartupDegraded} {
		t.Run(string(policy), func(t *testing.T) {
			manager := db.NewManager("primary")
			t.Cleanup(func() { _ = manager.Close() })
			var available atomic.Bool
			backend := &readinessRuntimeConnection{appFakeManagerConnection: appFakeManagerConnection{identity: db.NewConnectionID("recovery-readiness")}}
			failure := errors.New("启动连接失败")
			if err := manager.RegisterFactory("primary", func() (*db.DB, error) {
				if !available.Load() {
					return nil, failure
				}
				return db.NewDB(backend), nil
			}); err != nil {
				t.Fatal(err)
			}
			app := &App{health: health.NewRegistry(), dbManager: manager, databaseStartupPolicy: policy}
			app.setDatabaseReadinessError(failure)
			app.registerDatabaseReadinessCheck()
			if report := app.health.Readiness(context.Background()); report.Healthy() {
				t.Fatal("连接不可用时不应接收业务流量")
			}
			available.Store(true)
			if report := app.health.Readiness(context.Background()); !report.Healthy() {
				t.Fatalf("没有新业务请求时探针也应重新建立连接: %#v", report)
			}
		})
	}
}
