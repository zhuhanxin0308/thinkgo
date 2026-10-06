package driver

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRedisSessionCommitRejectsLostOwnershipAndCancellation 检查回调与提交之间的租约变更和取消。
func TestRedisSessionCommitRejectsLostOwnershipAndCancellation(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	assertRedisSessionCommitBoundaries(t, backend)
}

// assertRedisSessionCommitBoundaries 通过真实 Redis 命令制造后端状态变化，不绕过被测提交脚本。
func assertRedisSessionCommitBoundaries(t *testing.T, backend *Redis) {
	t.Helper()
	for _, mode := range []string{"replace_write", "replace_delete", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			id := "boundary-" + mode
			initial := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
			newer := marshalRedisTestEnvelope(2, time.Now().Add(time.Minute))
			if err := backend.Write(id, initial); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), redisTestTimeout)
				defer cancel()
				if err := backend.client.Del(ctx, backend.dataKey(id), backend.lockKey(id)).Err(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := backend.UpdateContext(ctx, id, func(current string, found bool) (string, bool, error) {
				if !found || current != initial {
					return "", false, errors.New("初始会话不符")
				}
				if mode == "cancel" {
					cancel()
				} else {
					// 模拟旧租约失效后，新 owner 已写入数据；旧请求不得覆盖或删除它。
					if err := backend.client.Set(t.Context(), backend.lockKey(id), "successor", time.Minute).Err(); err != nil {
						return "", false, err
					}
					if err := backend.client.Set(t.Context(), backend.dataKey(id), newer, time.Minute).Err(); err != nil {
						return "", false, err
					}
				}
				return marshalRedisTestEnvelope(3, time.Now().Add(time.Minute)), mode == "replace_delete", nil
			})
			wantErr := ErrSessionLockLost
			wantValue := newer
			if mode == "cancel" {
				wantErr = context.Canceled
				wantValue = initial
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("错误身份丢失: got=%v want=%v", err, wantErr)
			}
			if got, found, err := backend.Read(id); err != nil || !found || got != wantValue {
				t.Fatalf("失败提交改变了会话: found=%t err=%v", found, err)
			}
			if mode == "cancel" {
				if n, err := backend.client.Exists(t.Context(), backend.lockKey(id)).Result(); err != nil || n != 0 {
					t.Fatalf("取消后锁未释放: %d %v", n, err)
				}
			} else if owner, err := backend.client.Get(t.Context(), backend.lockKey(id)).Result(); err != nil || owner != "successor" {
				t.Fatalf("释放了新 owner 的锁: %q %v", owner, err)
			}
		})
	}
}
