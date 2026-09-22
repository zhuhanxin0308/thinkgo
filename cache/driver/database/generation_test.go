package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

type blockedCacheWrite struct {
	db.Connection
	started chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (connection *blockedCacheWrite) Update(ctx context.Context, request db.UpdateRequest) (db.UpdateResult, error) {
	key, _ := request.Data()["key"].(string)
	digest := sha256.Sum256([]byte("slow"))
	if key == "slow" || strings.HasSuffix(key, hex.EncodeToString(digest[:])) {
		connection.once.Do(func() { close(connection.started); <-connection.resume })
	}
	return connection.Connection.Update(ctx, request)
}

// TestIndependentKeysAndClearDoNotWaitForOldWriter 验证跨驱动实例的独立键写入与清空不会等待旧写入，且旧代数据不可见。
func TestIndependentKeysAndClearDoNotWaitForOldWriter(t *testing.T) {
	connection := newRecordingCacheConn()
	blocked := &blockedCacheWrite{Connection: connection, started: make(chan struct{}), resume: make(chan struct{})}
	slow, err := NewDB(blocked, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	fast, err := NewDB(connection, "think_cache")
	if err != nil {
		t.Fatal(err)
	}
	var resume sync.Once
	t.Cleanup(func() { resume.Do(func() { close(blocked.resume) }) })
	slowResult := make(chan error, 1)
	go func() { slowResult <- slow.Set("slow", "old", 0) }()
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("写入没有进入测试屏障")
	}
	fastResult := make(chan error, 1)
	go func() {
		if err := fast.Set("fast", "new", 0); err != nil {
			fastResult <- err
			return
		}
		fastResult <- fast.Clear()
	}()
	select {
	case err := <-fastResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("独立键写入或 Clear 被旧写入阻塞")
	}
	resume.Do(func() { close(blocked.resume) })
	if err := <-slowResult; err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"slow", "fast"} {
		if value, found, err := fast.Get(key); err != nil || found {
			t.Fatalf("Clear 后旧值重新出现: %s=%v found=%v err=%v", key, value, found, err)
		}
	}
	if err := fast.Set("slow", "current", 0); err != nil {
		t.Fatal(err)
	}
	if value, found, err := slow.Get("slow"); err != nil || !found || value != "current" {
		t.Fatalf("跨实例未看到新代数据: %v %v %v", value, found, err)
	}
}
