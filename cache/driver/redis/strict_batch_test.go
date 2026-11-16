package redis

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStrictBatchDistinguishesMissingKeysFromWrongTypes(t *testing.T) {
	ctx := t.Context()
	backend := newConditionalRedis(t)
	if err := backend.SetContext(ctx, "value", "visible", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := backend.client.RPush(ctx, backend.withPrefix("list"), "hidden").Err(); err != nil {
		t.Fatal(err)
	}
	values, err := backend.GetManyStrictContext(ctx, []string{"missing", "value"})
	if err != nil || len(values) != 1 || values["value"] != "visible" {
		t.Fatalf("批量 GET 应区分命中和缺键: %#v，%v", values, err)
	}
	values, err = backend.GetManyStrictContext(ctx, []string{"missing", "value", "list"})
	if values != nil || err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("缺键之后的类型错误被吞掉: %#v，%v", values, err)
	}
	if err := backend.client.Set(ctx, backend.withPrefix("invalid"), "!", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if values, err := backend.GetManyStrictContext(ctx, []string{"value", "invalid"}); values != nil || !errors.Is(err, ErrCorruptCacheEntry) {
		t.Fatalf("批量 GET 应拒绝损坏的缓存: %#v，%v", values, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := backend.GetManyStrictContext(cancelled, []string{"value"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消后仍尝试读取: %v", err)
	}
}
