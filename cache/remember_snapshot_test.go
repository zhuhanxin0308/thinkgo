package cache

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
)

// 序列化驱动接受含有私有引用的对象，共享加载仍须在写入前验证能否隔离返回值。
func TestRememberValidatesSnapshotBeforeCaching(t *testing.T) {
	store := NewCache(nil, atomicOrderRedis(t))
	const key = "snapshot"
	if _, err := store.Remember(key, time.Minute, func() (any, error) {
		return bytes.NewBufferString("private"), nil
	}); !errors.Is(err, driver.ErrUnsupportedMemoryValue) {
		t.Fatalf("不能隔离的结果应在提交前被拒绝：%v", err)
	}
	if _, found, err := store.Get(key); err != nil || found {
		t.Fatalf("被拒绝的结果已写入缓存：found=%t err=%v", found, err)
	}
}
