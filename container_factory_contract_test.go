package framework

import (
	"errors"
	"fmt"
	"os"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type panicInIsError struct{}

func (panicInIsError) Error() string { return "无法判断的错误" }
func (panicInIsError) Is(error) bool { panic("Is 失败") }

type panicInUnwrapError struct{}

func (panicInUnwrapError) Error() string { return "无法解包的错误" }
func (panicInUnwrapError) Unwrap() error { panic("Unwrap 失败") }

// 分类要调用扩展错误的方法；即使这些方法发生 panic，Make 仍应返回原有诊断。
func TestContainerFactoryRecoversBrokenErrorClassification(t *testing.T) {
	var nilPath *os.PathError
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"空路径错误指针", nilPath},
		{"组合错误中的空指针", errors.Join(nilPath, fwcontext.ErrRequestServiceScopeUnavailable)},
		{"Is 方法异常", panicInIsError{}},
		{"Unwrap 方法异常", panicInUnwrapError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := NewContainer()
			container.BindFactory("broken", func() interface{} { panic(tc.cause) })
			// fmt 对空指针及 Error 方法自身的 panic 都有既定的诊断格式。
			want := fmt.Sprintf("build factory broken failed: 工厂执行 panic: %v", tc.cause)
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("分类失败的 panic 逃出了容器：%v", recovered)
				}
			}()
			value, err := container.Make("broken")
			if value != nil || err == nil || err.Error() != want {
				t.Fatalf("工厂异常诊断错误：value=%v err=%v，期望 %q", value, err, want)
			}
			if errors.Is(err, ErrServiceNotBound) || errors.Is(err, fwcontext.ErrRequestServiceScopeUnavailable) {
				t.Fatalf("分类失败不应暴露缺失绑定或作用域不可用错误：%v", err)
			}
		})
	}
}

// TestInvalidFactorySignatureDoesNotExecute 验证各生命周期都在工厂产生副作用前拒绝非法返回签名。
func TestInvalidFactorySignatureDoesNotExecute(t *testing.T) {
	for _, lifecycle := range []LifecycleType{Singleton, Factory, Scoped} {
		container := NewContainer()
		scope := container.NewScope()
		t.Cleanup(func() { _ = scope.Close() })
		calls := 0
		for _, factory := range []interface{}{
			func() { calls++ },
			func() (int, string, bool) { calls++; return 1, "", false },
			func() (int, string) { calls++; return 1, "" },
		} {
			switch lifecycle {
			case Singleton:
				container.Bind("invalid", factory)
			case Factory:
				container.BindFactory("invalid", factory)
			case Scoped:
				container.BindScoped("invalid", factory)
			}
			if _, err := scope.Make("invalid"); err == nil {
				t.Errorf("生命周期 %v 未拒绝非法工厂 %T", lifecycle, factory)
			}
		}
		if calls != 0 {
			t.Errorf("生命周期 %v 执行了 %d 次非法工厂", lifecycle, calls)
		}
	}
}
