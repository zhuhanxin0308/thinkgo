package redis

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRedisContextMethodsRejectNilContext 验证 Redis 上下文 API 不会静默接受 nil 上下文。
func TestRedisContextMethodsRejectNilContext(t *testing.T) {
	driver := &Redis{}
	var nilContext context.Context
	if _, _, err := driver.GetContext(nilContext, "key"); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatalf("nil GetContext 应返回 ErrInvalidRedisContext，实际为 %v", err)
	}
	if err := driver.SetContext(nilContext, "key", "value", time.Minute); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatalf("nil SetContext 应返回 ErrInvalidRedisContext，实际为 %v", err)
	}
	if _, err := driver.AcquireLockContext(nilContext, "key", "owner", time.Minute); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatalf("nil AcquireLockContext 应返回 ErrInvalidRedisContext，实际为 %v", err)
	}
	if _, err := driver.ReleaseLockContext(nilContext, "key", "owner"); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatalf("nil ReleaseLockContext 应返回 ErrInvalidRedisContext，实际为 %v", err)
	}
	if _, err := driver.RenewLockContext(nilContext, "key", "owner", time.Minute); !errors.Is(err, ErrInvalidRedisContext) {
		t.Fatalf("nil RenewLockContext 应返回 ErrInvalidRedisContext，实际为 %v", err)
	}
}
