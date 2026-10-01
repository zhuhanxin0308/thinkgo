package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestRedisSessionClearPreservesNestedNamespaces 验证合法配置的嵌套前缀不使 Clear 越界删除数据或锁。
func TestRedisSessionClearPreservesNestedNamespaces(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	assertRedisSessionClearScope(t, backend)
}

// assertRedisSessionClearScope 同时用于模拟和真实 Redis，检查实际键与锁的所有权。
func assertRedisSessionClearScope(t *testing.T, backend *Redis) {
	t.Helper()
	nested := &Redis{client: backend.client, prefix: backend.prefix + "data:tenant:", timeout: backend.timeout}
	const id = "session-A"
	initial := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := backend.Write(id, initial); err != nil {
		t.Fatal(err)
	}
	if err := nested.Write(id, initial); err != nil {
		t.Fatal(err)
	}
	ownLock := backend.lockKey("unrelated")
	if err := backend.client.Set(t.Context(), ownLock, "active-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	foreign := []string{backend.dataKey(""), backend.dataKey("not:an:id"), backend.dataKey(strings.Repeat("x", maxSessionIDBytes+1))}
	for _, key := range foreign {
		if err := backend.client.Set(t.Context(), key, "foreign", time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// 清理只删除本次测试明确创建的键，不使用相同的 Clear 来证明自己。
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), redisTestTimeout)
		defer cancel()
		keys := append([]string{backend.dataKey(id), nested.dataKey(id), nested.lockKey(id), ownLock}, foreign...)
		if err := backend.client.Del(ctx, keys...).Err(); err != nil {
			t.Error(err)
		}
	})
	updateErr := nested.Update(id, func(current string, found bool) (string, bool, error) {
		if !found || current != initial {
			return "", false, errors.New("嵌套会话初值缺失")
		}
		owner, err := backend.client.Get(t.Context(), nested.lockKey(id)).Result()
		if err != nil {
			return "", false, err
		}
		if err := backend.Clear(); err != nil {
			return "", false, err
		}
		if _, found, err := backend.Read(id); err != nil || found {
			t.Errorf("本命名空间未清理: found=%t err=%v", found, err)
		}
		if data, found, err := nested.Read(id); err != nil || !found || data != initial {
			t.Errorf("越界删除嵌套会话: found=%t err=%v", found, err)
		}
		if after, err := backend.client.Get(t.Context(), nested.lockKey(id)).Result(); err != nil || after != owner {
			t.Errorf("越界删除活动锁: owner=%q err=%v", after, err)
		}
		if after, err := backend.client.Get(t.Context(), ownLock).Result(); err != nil || after != "active-owner" {
			t.Errorf("本驱动的活动锁被清理: %q %v", after, err)
		}
		for _, key := range foreign {
			if after, err := backend.client.Get(t.Context(), key).Result(); err != nil || after != "foreign" {
				t.Errorf("无法归属的键被删除: %q %v", key, err)
			}
		}
		return marshalRedisTestEnvelope(2, time.Now().Add(time.Minute)), false, nil
	})
	if updateErr != nil {
		t.Fatalf("其它命名空间清理破坏原子更新: %v", updateErr)
	}
	if data, found, err := nested.Read(id); err != nil || !found || decodeRedisTestCounter(t, data) != 2 {
		t.Fatalf("嵌套会话更新丢失: found=%t err=%v", found, err)
	}
	if err := backend.Clear(); err != nil {
		t.Fatal(err)
	}
}

// TestRedisSessionClearScansAllOwnedIDs 验证过滤后仍清理多个 SCAN 批次中的合法 ID。
func TestRedisSessionClearScansAllOwnedIDs(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	for index := 0; index < 600; index++ {
		key := backend.dataKey(fmt.Sprintf("ID_%03d-valid", index))
		if err := backend.client.Set(t.Context(), key, "owned", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := backend.Clear(); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 600; index++ {
		if found, err := backend.client.Exists(t.Context(), backend.dataKey(fmt.Sprintf("ID_%03d-valid", index))).Result(); err != nil || found != 0 {
			t.Fatalf("合法 ID 遗漏: %d %d %v", index, found, err)
		}
	}
}
