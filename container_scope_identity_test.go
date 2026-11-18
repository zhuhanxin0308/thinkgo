package framework

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
)

type scopedFunctionCloser func() error

func (closer scopedFunctionCloser) Close() error { return closer() }

// newScopedFunctionCloser 保持统一代码入口，使不同捕获值的身份碰撞可确定复现。
//
//go:noinline
func newScopedFunctionCloser(name string, order *[]string) scopedFunctionCloser {
	return func() error { *order = append(*order, name); return nil }
}

type scopedValueCloser struct {
	name  string
	order *[]string
}

func (scopedValueCloser) String() string { return "相同的展示文本" }
func (closer scopedValueCloser) Close() error {
	*closer.order = append(*closer.order, closer.name)
	return nil
}

// TestContainerScopeResourceIdentity 验证独立函数、值资源及共享指针别名的关闭责任。
func TestContainerScopeResourceIdentity(t *testing.T) {
	var order []string
	first := newScopedFunctionCloser("函数一", &order)
	second := newScopedFunctionCloser("函数二", &order)
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("测试必须使用共享代码入口的独立闭包")
	}
	shared := &scopedValueCloser{name: "共享指针", order: &order}
	value := scopedValueCloser{name: "值资源", order: &order}
	container := NewContainer()
	scope := container.NewScope()
	for index, closer := range []io.Closer{first, second, value, value, shared, shared} {
		name := fmt.Sprintf("resource.%d", index)
		container.BindScoped(name, func() interface{} { return closer })
		if _, err := scope.Make(name); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := scope.Close(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"共享指针", "值资源", "值资源", "函数二", "函数一"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("独立资源遗漏或别名重复关闭: 实际=%v, 预期=%v", order, want)
	}
}

type scopedZeroSizeCloserA struct{}
type scopedZeroSizeCloserB struct{}

var (
	scopedZeroSizeCloseOrderMu sync.Mutex
	scopedZeroSizeCloseOrder   []string
)

func (*scopedZeroSizeCloserA) Close() error {
	scopedZeroSizeCloseOrderMu.Lock()
	scopedZeroSizeCloseOrder = append(scopedZeroSizeCloseOrder, "zeroA")
	scopedZeroSizeCloseOrderMu.Unlock()
	return nil
}

func (*scopedZeroSizeCloserB) Close() error {
	scopedZeroSizeCloseOrderMu.Lock()
	scopedZeroSizeCloseOrder = append(scopedZeroSizeCloseOrder, "zeroB")
	scopedZeroSizeCloseOrderMu.Unlock()
	return nil
}

// TestContainerScopeZeroSizeStructPointerClosersAllClose 验证共享 runtime.zerobase 的零尺寸结构体指针不会被误去重跳过关闭。
func TestContainerScopeZeroSizeStructPointerClosersAllClose(t *testing.T) {
	scopedZeroSizeCloseOrderMu.Lock()
	scopedZeroSizeCloseOrder = nil
	scopedZeroSizeCloseOrderMu.Unlock()
	t.Cleanup(func() {
		scopedZeroSizeCloseOrderMu.Lock()
		scopedZeroSizeCloseOrder = nil
		scopedZeroSizeCloseOrderMu.Unlock()
	})

	container := NewContainer()
	container.BindScoped("zero.a1", func() *scopedZeroSizeCloserA { return &scopedZeroSizeCloserA{} })
	container.BindScoped("zero.a2", func() *scopedZeroSizeCloserA { return &scopedZeroSizeCloserA{} })
	container.BindScoped("zero.b1", func() *scopedZeroSizeCloserB { return &scopedZeroSizeCloserB{} })

	scope := container.NewScope()
	for _, name := range []string{"zero.a1", "zero.a2", "zero.b1"} {
		if _, err := scope.Make(name); err != nil {
			t.Fatalf("解析零尺寸作用域服务 %s 失败: %v", name, err)
		}
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("关闭零尺寸作用域服务失败: %v", err)
	}

	scopedZeroSizeCloseOrderMu.Lock()
	got := append([]string(nil), scopedZeroSizeCloseOrder...)
	scopedZeroSizeCloseOrderMu.Unlock()
	want := []string{"zeroB", "zeroA", "zeroA"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("零尺寸指针 Closer 被误合并漏关或顺序错误: 实际=%v, 预期=%v", got, want)
	}
}

// TestContainerScopeFunctionErrorsAndPanic 确认更换身份策略后，逆序释放仍聚合错误并跨越 panic。
func TestContainerScopeFunctionErrorsAndPanic(t *testing.T) {
	firstErr, secondErr := errors.New("第一项错误"), errors.New("第二项错误")
	closed := 0
	container := NewContainer()
	scope := container.NewScope()
	for index, closer := range []scopedFunctionCloser{
		func() error { closed++; return firstErr },
		func() error { closed++; panic("清理失败") },
		func() error { closed++; return secondErr },
	} {
		name := fmt.Sprintf("failure.%d", index)
		container.BindScoped(name, func() interface{} { return closer })
		if _, err := scope.Make(name); err != nil {
			t.Fatal(err)
		}
	}
	err := scope.Close()
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) || closed != 3 {
		t.Fatalf("错误聚合或后续清理失败: 次数=%d, 错误=%v", closed, err)
	}
	if repeated := scope.Close(); !errors.Is(repeated, firstErr) || closed != 3 {
		t.Fatalf("重复关闭不幂等: 次数=%d, 错误=%v", closed, repeated)
	}
}
