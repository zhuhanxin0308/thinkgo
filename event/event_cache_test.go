package event

import (
	"strconv"
	"testing"
)

// TestDispatcherResolutionCacheInvalidation 验证监听器变更后不会继续使用旧的解析快照。
func TestDispatcherResolutionCacheInvalidation(t *testing.T) {
	dispatcher := NewDispatcher()
	called := make([]string, 0, 3)
	first := &SimpleListener{Handler: func(Event) error {
		called = append(called, "first")
		return nil
	}}
	second := &SimpleListener{Handler: func(Event) error {
		called = append(called, "second")
		return nil
	}}
	if err := dispatcher.Listen("cache.changed", first); err != nil {
		t.Fatalf("注册首个监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("cache.changed", nil)); err != nil {
		t.Fatalf("首次分发失败: %v", err)
	}
	if err := dispatcher.Listen("cache.changed", second); err != nil {
		t.Fatalf("注册第二个监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("cache.changed", nil)); err != nil {
		t.Fatalf("监听器变更后的分发失败: %v", err)
	}
	if len(called) != 3 || called[1] != "first" || called[2] != "second" {
		t.Fatalf("注册监听器后仍使用旧快照，实际调用顺序为 %#v", called)
	}
	if err := dispatcher.Forget("cache.changed"); err != nil {
		t.Fatalf("移除监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("cache.changed", nil)); err != nil {
		t.Fatalf("移除监听器后的分发失败: %v", err)
	}
	if len(called) != 3 {
		t.Fatalf("移除监听器后不应继续调用旧快照，实际调用次数为 %d", len(called))
	}
}

// TestDispatcherResolutionCacheInvalidatesWildcard 验证新增通配监听器会使精确事件缓存失效。
func TestDispatcherResolutionCacheInvalidatesWildcard(t *testing.T) {
	dispatcher := NewDispatcher()
	called := 0
	if err := dispatcher.Listen("cache.wildcard", &SimpleListener{Handler: func(Event) error {
		called++
		return nil
	}}); err != nil {
		t.Fatalf("注册精确监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("cache.wildcard", nil)); err != nil {
		t.Fatalf("首次分发失败: %v", err)
	}
	if err := dispatcher.Listen("cache.*", &SimpleListener{Handler: func(Event) error {
		called++
		return nil
	}}); err != nil {
		t.Fatalf("注册通配监听器失败: %v", err)
	}
	if err := dispatcher.Dispatch(NewEvent("cache.wildcard", nil)); err != nil {
		t.Fatalf("新增通配监听器后的分发失败: %v", err)
	}
	if called != 3 {
		t.Fatalf("新增通配监听器后应执行精确和通配监听器，实际调用次数为 %d", called)
	}
}

// TestDispatcherResolutionCacheIsBounded 验证动态事件名不会令解析缓存无限增长。
func TestDispatcherResolutionCacheIsBounded(t *testing.T) {
	dispatcher := NewDispatcher()
	for index := 0; index < maxResolvedEventCacheEntries*2; index++ {
		eventName := "cache.dynamic." + strconv.Itoa(index)
		if err := dispatcher.Dispatch(NewEvent(eventName, nil)); err != nil {
			t.Fatalf("分发动态事件失败: %v", err)
		}
	}
	if len(dispatcher.resolved) > maxResolvedEventCacheEntries {
		t.Fatalf("解析缓存超过容量上限，实际为 %d", len(dispatcher.resolved))
	}
}
