package cache

import (
	"testing"

	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
)

// TestCounterRebasesPastExistingGeneration 验证计数从当前值计算，并取得严格较新的独占提交身份。
func TestCounterRebasesPastExistingGeneration(t *testing.T) {
	backend := cacheDriver.NewMemory()
	manager := NewCache(nil, backend)
	original := newCacheValueEnvelope(2, int64(10))
	if err := backend.Set("counter", original, 0); err != nil {
		t.Fatal(err)
	}
	if count, err := manager.Inc("counter", 1); err != nil || count != 11 {
		t.Fatalf("计数未读取当前值继续合并: %d %v", count, err)
	}
	value, found, err := backend.Get("counter")
	if err != nil || !found {
		t.Fatalf("计数提交丢失: %#v %t %v", value, found, err)
	}
	decoded, generation, _, err := decodeCacheValueEnvelope(value)
	if err != nil || generation <= 2 || decoded != int64(11) {
		t.Fatalf("计数未获得新的独占提交身份: value=%v generation=%d err=%v", decoded, generation, err)
	}
}
