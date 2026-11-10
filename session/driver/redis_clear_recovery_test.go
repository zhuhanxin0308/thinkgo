package driver

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestRedisClearingRecoversAfterFinishFailure 验证结束命令失败后，新实例能在有限期限后恢复写入。
func TestRedisClearingRecoversAfterFinishFailure(t *testing.T) {
	backend, server := newMiniredisSession(t)
	other := newRedisClearPeer(t, backend)
	injected := errors.New("测试清理结束时 Redis 不可用")
	backend.client.AddHook(&redisClearCommandHook{
		match: func(command redis.Cmder) bool {
			return command.Name() == "eval" && command.Args()[1] == redisSessionClearFinishScript
		},
		before: func() error { return injected },
	})
	value := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	err := backend.withSessionLock("old", func(owner string) error {
		if err := backend.Clear(); !errors.Is(err, injected) {
			t.Fatalf("未触发结束失败: %v", err)
		}
		if err := other.Write("new", value); !errors.Is(err, ErrSessionCleared) {
			t.Fatalf("清理有效期内不应允许新写入: %v", err)
		}
		server.FastForward(backend.timeout*redisSessionClearTimeoutFactor + time.Second)
		if err := other.Write("new", value); err != nil {
			t.Fatalf("结束失败永久阻断新会话: %v", err)
		}
		return backend.writeUnlocked(owner, "old", value)
	})
	if !errors.Is(err, ErrSessionCleared) {
		t.Fatalf("故障恢复后允许了旧代际提交: %v", err)
	}
	if _, found, err := other.Read("old"); err != nil || found {
		t.Fatalf("故障恢复使旧会话复活: %v %v", found, err)
	}
	if err := other.Delete("new"); err != nil {
		t.Fatal(err)
	}
}

func TestRedisAbandonedClearCannotResumeAfterLeaseExpires(t *testing.T) {
	backend, server := newMiniredisSession(t)
	other := newRedisClearPeer(t, backend)
	const owner = "abandoned-process"
	clearing := redisSessionClearingPrefix + owner
	lease := backend.timeout * redisSessionClearTimeoutFactor
	if err := backend.client.Eval(t.Context(), redisSessionClearStartScript,
		[]string{backend.generationKey(), backend.clearLeaseKey()}, clearing, lease.Milliseconds()).Err(); err != nil {
		t.Fatal(err)
	}
	// 模拟进程在启动清理后直接退出，没有执行结束脚本。
	server.FastForward(lease + time.Millisecond)
	value := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := other.Write("successor", value); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{redisSessionClearDeleteScript, redisSessionClearFinishScript} {
		result, err := backend.client.Eval(t.Context(), script,
			[]string{backend.generationKey(), backend.clearLeaseKey(), backend.dataKey("successor")},
			clearing, redisSessionReadyPrefix+owner).Result()
		if err != nil || result != int64(redisSessionGenerationChanged) {
			t.Fatalf("失效清理继续删除或结束了后继代际: %v %v", result, err)
		}
	}
	if got, found, err := other.Read("successor"); err != nil || !found || got != value {
		t.Fatalf("故障恢复后的新会话丢失: %v %v", found, err)
	}
	if ttl := server.TTL(backend.generationKey()); ttl != 0 {
		t.Fatalf("持久代际不得随活动租约到期: %v", ttl)
	}
}

func TestRedisRecoversLegacyClearingMarkerWithoutLease(t *testing.T) {
	backend, _ := newMiniredisSession(t)
	if err := backend.client.Set(t.Context(), backend.generationKey(), redisSessionClearingPrefix+"legacy", 0).Err(); err != nil {
		t.Fatal(err)
	}
	value := marshalRedisTestEnvelope(1, time.Now().Add(time.Minute))
	if err := newRedisClearPeer(t, backend).Write("new", value); err != nil {
		t.Fatalf("旧版本遗留的无租约状态无法自动恢复: %v", err)
	}
	if generation, err := backend.client.Get(t.Context(), backend.generationKey()).Result(); err != nil || generation != redisSessionReadyPrefix+"legacy" {
		t.Fatalf("旧代际恢复错误: %q %v", generation, err)
	}
}
