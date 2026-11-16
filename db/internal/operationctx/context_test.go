package operationctx

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDriverDefaultKeepsCallerCancellation 验证替换框架默认预算后仍继承请求值和取消。
func TestDriverDefaultKeepsCallerCancellation(t *testing.T) {
	type requestKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(t.Context(), requestKey{}, "request"))
	defer cancelParent()
	framework, cancelFramework := WithFrameworkDefault(parent, time.Minute)
	defer cancelFramework()
	driver, cancelDriver := WithDriverDefault(framework, 2*time.Minute)
	defer cancelDriver()
	if driver.Value(requestKey{}) != "request" {
		t.Fatal("驱动期限应继承调用方的值")
	}
	cancelParent()
	if !errors.Is(driver.Err(), context.Canceled) {
		t.Fatalf("驱动必须响应调用方取消: %v", driver.Err())
	}
}

// TestExplicitDeadlineSurvivesBothLayers 验证任一层均不修改调用方的截止时间。
func TestExplicitDeadlineSurvivesBothLayers(t *testing.T) {
	want := time.Now().Add(2 * time.Minute)
	parent, cancelParent := context.WithDeadline(t.Context(), want)
	defer cancelParent()
	framework, cancelFramework := WithFrameworkDefault(parent, time.Minute)
	defer cancelFramework()
	driver, cancelDriver := WithDriverDefault(framework, time.Second)
	defer cancelDriver()
	got, ok := driver.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("显式期限发生变化: %v", got)
	}
}
