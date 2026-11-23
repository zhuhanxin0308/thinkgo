package cleanup

import (
	"errors"
	"os"
	"reflect"
	"runtime"
	"testing"
)

// 普通错误保留原身份，成功的步骤不引入伪错误。
func TestRunCollectsReturnedErrors(t *testing.T) {
	var result error
	Run(&result, "成功", func() error { return nil })
	if result != nil {
		t.Fatal(result)
	}
	first, second := errors.New("第一项失败"), errors.New("第二项失败")
	Run(&result, "第一项", func() error { return first })
	Run(&result, "第二项", func() error { return second })
	if !errors.Is(result, first) || !errors.Is(result, second) || errors.Is(result, ErrAborted) {
		t.Fatalf("正常返回的错误被丢失或误判为退出：%v", result)
	}
	previous := result
	Run(&result, "后续成功", func() error { return nil })
	if result != previous {
		t.Fatal("成功步骤重新包装了已有错误")
	}
}

// Goexit 仍终止原调用协程，已登记的后续清理先于最终结果发布执行。
func TestRunPreservesGoexitAndDeferredContinuation(t *testing.T) {
	finished := make(chan struct{})
	var result error
	var order []string
	returned := false
	followingErr := errors.New("后续步骤失败")
	go func() {
		defer close(finished)
		defer Run(&result, "后续步骤", func() error {
			order = append(order, "following")
			return followingErr
		})
		Run(&result, "退出步骤", func() error {
			order = append(order, "exiting")
			runtime.Goexit()
			return nil
		})
		returned = true
	}()
	<-finished
	if returned || !reflect.DeepEqual(order, []string{"exiting", "following"}) {
		t.Fatalf("协程退出被吞掉或后续步骤未执行：返回=%t，顺序=%v", returned, order)
	}
	if !errors.Is(result, ErrAborted) || !errors.Is(result, followingErr) {
		t.Fatalf("协程退出或后续错误未发布：%v", result)
	}
}

// panic 的处理责任留给现有隔离边界，不得把它吞掉或改成 Goexit。
func TestRunPreservesPanicIdentity(t *testing.T) {
	marker := &struct{ message string }{message: "原始 panic"}
	previous := errors.New("先前错误")
	var result error = previous
	func() {
		defer func() {
			if recovered := recover(); recovered != marker {
				t.Errorf("panic 身份被改变：%v", recovered)
			}
		}()
		Run(&result, "异常步骤", func() error { panic(marker) })
		t.Error("panic 被错误吞掉")
	}()
	if result != previous {
		t.Fatalf("panic 传播改写了已有错误：%v", result)
	}
}

// 旧兼容模式的 recover 返回 nil，仍须重新抛出 panic，不能当作 Goexit 或正常返回。
func TestRunPreservesLegacyNilPanic(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",panicnil=1")
	previous := errors.New("已有错误")
	var result error = previous
	continued := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("未保留旧模式的 nil panic：%v", recovered)
			}
		}()
		Run(&result, "nil panic", func() error { panic(nil) })
		continued = true
	}()
	if continued || result != previous {
		t.Fatalf("nil panic 被吞掉或伪报协程退出：继续=%t，错误=%v", continued, result)
	}
}
