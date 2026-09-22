package redis

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
)

// TestRedisReadHonorsInFlightDeadline 在已建连接上阻塞响应，区别于请求发出前取消的测试。
func TestRedisReadHonorsInFlightDeadline(t *testing.T) {
	const requestBudget = 50 * time.Millisecond
	const verificationBudget = time.Second
	backend := miniredis.RunT(t)
	port, err := strconv.Atoi(backend.Port())
	if err != nil {
		t.Fatal(err)
	}
	driver, err := NewRedis(map[string]interface{}{"host": backend.Host(), "port": port, "timeout_ms": (3 * time.Second).Milliseconds()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	if err := driver.client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	backend.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		if strings.EqualFold(command, "get") {
			enterOnce.Do(func() { close(entered) })
			<-release
		}
		return false
	})
	ctx, cancel := context.WithTimeout(t.Context(), requestBudget)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, _, err := driver.GetContext(ctx, "deadline"); result <- err }()
	select {
	case <-entered:
	case <-time.After(verificationBudget):
		t.Fatal("未在已建立连接上进入读取")
	}
	select {
	case err := <-result:
		if err == nil || ctx.Err() == nil {
			t.Fatalf("阻塞读取未按请求期限失败: %v", err)
		}
	case <-time.After(verificationBudget):
		t.Fatal("读取忽略短请求期限，仍等待驱动超时")
	}
}
