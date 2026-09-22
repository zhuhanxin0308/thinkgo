package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTaskValidatesAndFreezesUntrustedData 验证类型、负载、Header 边界和防御性复制。
func TestTaskValidatesAndFreezesUntrustedData(t *testing.T) {
	payload := []byte(`{"user_id":42}`)
	headers := map[string]string{"traceparent": "00-abc"}
	task, err := NewTask("mail.send", payload, headers)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	payload[0] = 'x'
	headers["traceparent"] = "changed"
	resolvedPayload := task.Payload()
	resolvedHeaders := task.Headers()
	if string(resolvedPayload) != `{"user_id":42}` || resolvedHeaders["traceparent"] != "00-abc" {
		t.Fatalf("任务快照被调用方修改: payload=%q headers=%v", resolvedPayload, resolvedHeaders)
	}
	resolvedPayload[0] = 'y'
	resolvedHeaders["traceparent"] = "again"
	if string(task.Payload()) != `{"user_id":42}` || task.Headers()["traceparent"] != "00-abc" {
		t.Fatal("任务访问器未返回防御性副本")
	}

	invalid := []struct {
		taskType string
		payload  []byte
		headers  map[string]string
	}{
		{taskType: ""},
		{taskType: "bad type"},
		{taskType: strings.Repeat("a", MaximumTaskTypeBytes+1)},
		{taskType: "valid", payload: make([]byte, MaximumPayloadBytes+1)},
		{taskType: "valid", headers: map[string]string{"bad\nname": "value"}},
		{taskType: "valid", headers: map[string]string{"name": "bad\nvalue"}},
	}
	for _, input := range invalid {
		if _, err := NewTask(input.taskType, input.payload, input.headers); !errors.Is(err, ErrInvalidTask) {
			t.Fatalf("非法任务未被拒绝: %#v err=%v", input, err)
		}
	}
}

// TestRouterUsesExactHandlersAndFreezes 验证精确任务类型匹配、重复注册和冻结生命周期。
func TestRouterUsesExactHandlersAndFreezes(t *testing.T) {
	router := NewRouter()
	called := false
	if err := router.Register("mail.send", HandlerFunc(func(_ context.Context, task Task) error {
		called = task.Type() == "mail.send"
		return nil
	})); err != nil {
		t.Fatalf("注册任务处理器失败: %v", err)
	}
	if err := router.Register("mail.send", HandlerFunc(func(context.Context, Task) error { return nil })); !errors.Is(err, ErrDuplicateHandler) {
		t.Fatalf("重复处理器必须被拒绝，实际为 %v", err)
	}
	task, _ := NewTask("mail.send", nil, nil)
	if err := router.HandleTask(context.Background(), task); err != nil || !called {
		t.Fatalf("任务分派失败: called=%t err=%v", called, err)
	}
	if err := router.Register("mail.other", HandlerFunc(func(context.Context, Task) error { return nil })); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("首次处理后路由器必须冻结，实际为 %v", err)
	}
	unknown, _ := NewTask("mail.unknown", nil, nil)
	if err := router.HandleTask(context.Background(), unknown); !errors.Is(err, ErrHandlerNotFound) {
		t.Fatalf("未知任务必须返回稳定错误，实际为 %v", err)
	}
}

// TestEnqueueOptionsValidation 验证重试、超时、调度、唯一性和保留期的安全范围。
func TestEnqueueOptionsValidation(t *testing.T) {
	now := time.Now()
	valid := EnqueueOptions{Queue: "critical", MaxRetry: 3, Timeout: time.Minute, ProcessAt: now.Add(time.Minute), UniqueFor: time.Minute, Retention: time.Hour}
	if err := valid.Validate(now); err != nil {
		t.Fatalf("合法入队选项校验失败: %v", err)
	}
	invalid := []EnqueueOptions{
		{Queue: "bad queue"},
		{MaxRetry: -1},
		{MaxRetry: MaximumRetries + 1},
		{MaxRetry: 1, DisableRetry: true},
		{Timeout: MaximumTaskTimeout + time.Second},
		{ProcessAt: now.Add(-time.Second)},
		{UniqueFor: time.Millisecond},
		{Retention: -time.Second},
	}
	for _, options := range invalid {
		if err := options.Validate(now); !errors.Is(err, ErrInvalidOptions) {
			t.Fatalf("非法入队选项未被拒绝: %#v err=%v", options, err)
		}
	}
}

// TestQueueDefaultsAndExplicitFreeze 验证默认队列、默认时限和显式冻结契约。
func TestQueueDefaultsAndExplicitFreeze(t *testing.T) {
	defaults := EnqueueOptions{}
	if defaults.NormalizedQueue() != DefaultQueueName || defaults.EffectiveTimeout() != DefaultTaskTimeout || defaults.EffectiveMaxRetry() != DefaultMaxRetries {
		t.Fatalf("队列默认值错误: queue=%q timeout=%s retry=%d", defaults.NormalizedQueue(), defaults.EffectiveTimeout(), defaults.EffectiveMaxRetry())
	}
	explicit := EnqueueOptions{Queue: "critical", Timeout: time.Second, DisableRetry: true}
	if explicit.NormalizedQueue() != "critical" || explicit.EffectiveTimeout() != time.Second || explicit.EffectiveMaxRetry() != 0 {
		t.Fatal("显式队列选项被默认值覆盖")
	}
	router := NewRouter()
	router.Freeze()
	if err := router.Register("mail.send", HandlerFunc(func(context.Context, Task) error { return nil })); !errors.Is(err, ErrRouterFrozen) {
		t.Fatalf("显式冻结后注册必须失败，实际为 %v", err)
	}
	var nilRouter *Router
	nilRouter.Freeze()
	if err := nilRouter.HandleTask(context.Background(), Task{}); !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("nil 路由器必须返回稳定错误，实际为 %v", err)
	}
}
