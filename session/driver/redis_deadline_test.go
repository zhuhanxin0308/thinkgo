package driver

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2/server"
)

// TestSessionRedisReadHonorsInFlightDeadline 验证已经发出的 Session 读取遵守较短的请求期限。
func TestSessionRedisReadHonorsInFlightDeadline(t *testing.T) {
	const requestBudget = 50 * time.Millisecond
	const verificationBudget = time.Second
	driver, backend := newMiniredisSession(t)
	if err := driver.client.Ping(t.Context()).Err(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce sync.Once
	t.Cleanup(func() { close(release) })
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
	go func() { _, _, err := driver.ReadContext(ctx, "deadline"); result <- err }()
	select {
	case <-entered:
	case <-time.After(verificationBudget):
		t.Fatal("Session 未进入网络读取")
	}
	select {
	case err := <-result:
		if err == nil || ctx.Err() == nil {
			t.Fatalf("Session 阻塞读取未按请求期限失败: %v", err)
		}
	case <-time.After(verificationBudget):
		t.Fatal("Session 读取忽略短请求期限")
	}
}
