//go:build integration

package framework

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/db/connector"
)

// liveDatabaseProxy 只中断本测试创建的连接，用真实 MySQL 验证断连恢复而不停止用户数据库。
type liveDatabaseProxy struct {
	listener    net.Listener
	target      string
	mu          sync.Mutex
	available   bool
	connections map[net.Conn]struct{}
	workers     sync.WaitGroup
}

func newLiveDatabaseProxy(t *testing.T, target string) *liveDatabaseProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &liveDatabaseProxy{listener: listener, target: target, available: true, connections: make(map[net.Conn]struct{})}
	proxy.workers.Add(1)
	go func() {
		defer proxy.workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			proxy.mu.Lock()
			if !proxy.available {
				proxy.mu.Unlock()
				_ = client.Close()
				continue
			}
			proxy.connections[client] = struct{}{}
			proxy.workers.Add(1)
			proxy.mu.Unlock()
			go proxy.relay(client)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		proxy.setAvailable(false)
		proxy.workers.Wait()
	})
	return proxy
}

func (proxy *liveDatabaseProxy) relay(client net.Conn) {
	defer proxy.workers.Done()
	defer client.Close()
	defer func() {
		proxy.mu.Lock()
		delete(proxy.connections, client)
		proxy.mu.Unlock()
	}()
	server, err := net.DialTimeout("tcp", proxy.target, time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	finished := make(chan struct{})
	go func() {
		_, _ = io.Copy(server, client)
		_ = server.Close()
		close(finished)
	}()
	_, _ = io.Copy(client, server)
	_ = client.Close()
	<-finished
}

func (proxy *liveDatabaseProxy) setAvailable(available bool) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.available = available
	if !available {
		for connection := range proxy.connections {
			_ = connection.Close()
		}
	}
}

// TestLiveMySQLReadinessRecovery 覆盖启动失败、运行时断连，以及无业务流量时的探针恢复。
func TestLiveMySQLReadinessRecovery(t *testing.T) {
	host, configured := os.LookupEnv("THINKGO_LIVE_MYSQL_HOST")
	if !configured {
		t.Skip("THINKGO_LIVE_MYSQL_* 未配置")
	}
	for _, name := range []string{"PORT", "DATABASE", "USER", "PASSWORD"} {
		if _, exists := os.LookupEnv("THINKGO_LIVE_MYSQL_" + name); !exists {
			t.Fatalf("真实 MySQL 环境缺少 %s", name)
		}
	}
	connector.RegisterBuiltins()
	for _, policy := range []DatabaseStartupPolicy{DatabaseStartupRequired, DatabaseStartupDegraded, DatabaseStartupLazy} {
		t.Run(string(policy), func(t *testing.T) {
			proxy := newLiveDatabaseProxy(t, net.JoinHostPort(host, os.Getenv("THINKGO_LIVE_MYSQL_PORT")))
			if policy == DatabaseStartupDegraded {
				proxy.setAvailable(false)
			}
			basePath := t.TempDir()
			writeTestAppConfigFiles(t, basePath)
			setDatabaseStartupPolicyForTest(t, basePath, policy)
			proxyHost, proxyPort, err := net.SplitHostPort(proxy.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			configuration := map[string]any{"default": "mysql", "connections": map[string]any{"mysql": map[string]any{
				"type": "mysql", "hostname": proxyHost, "hostport": proxyPort,
				"database": os.Getenv("THINKGO_LIVE_MYSQL_DATABASE"), "username": os.Getenv("THINKGO_LIVE_MYSQL_USER"),
				"password": os.Getenv("THINKGO_LIVE_MYSQL_PASSWORD"), "params": map[string]string{"tls": "false"},
			}}}
			encoded, err := json.Marshal(configuration)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(basePath, "config", "database.json"), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			app := mustBuildTestApp(t, basePath)
			t.Cleanup(func() { _ = app.Close() })
			if policy == DatabaseStartupDegraded {
				if report := app.health.Readiness(t.Context()); report.Healthy() {
					t.Fatal("启动连接不可达时 readiness 应失败")
				}
				proxy.setAvailable(true)
			} else {
				if _, err := app.dbManager.Default(); err != nil {
					t.Fatalf("建立真实数据库连接失败: %v", err)
				}
			}
			if report := app.health.Readiness(t.Context()); !report.Healthy() {
				t.Fatalf("真实数据库可达时 readiness 应成功: %#v", report)
			}
			proxy.setAvailable(false)
			if report := app.health.Readiness(t.Context()); report.Healthy() {
				t.Fatal("运行时断连不能继续上报就绪")
			}
			proxy.setAvailable(true)
			if report := app.health.Readiness(t.Context()); !report.Healthy() {
				t.Fatalf("恢复真实连接后仅靠探针应恢复就绪: %#v", report)
			}
			if _, err := app.dbManager.Default(); err != nil {
				t.Fatalf("探针恢复后业务连接仍不可用: %v", err)
			}
			businessDatabase, err := ResolveServiceAs[*db.DB](app, ServiceDB)
			if err != nil {
				t.Fatalf("恢复后的默认数据库服务无法注入: %v", err)
			}
			if rows, err := businessDatabase.Query("SELECT 1 AS available"); err != nil || len(rows) != 1 {
				t.Fatalf("恢复后的业务数据库服务无法执行查询: rows=%v err=%v", rows, err)
			}
		})
	}
}
