package framework

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"testing/synctest"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
)

// 关闭器退出协程后仍须逆序处理其余资源，且不能越过正在阻塞的关闭器发布完成。
func TestContainerScopeContinuesAfterGoexit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		container := NewContainer()
		scope := container.NewScope()
		var order []string
		release := make(chan struct{})
		lastErr := errors.New("最后一个资源关闭失败")
		closers := []scopedFunctionCloser{
			func() error { order = append(order, "last"); return lastErr },
			func() error { order = append(order, "exit-1"); runtime.Goexit(); return nil },
			func() error { order = append(order, "blocked"); <-release; return nil },
			func() error { order = append(order, "exit-2"); runtime.Goexit(); return nil },
		}
		for index, closer := range closers {
			name := fmt.Sprintf("exit.resource.%d", index)
			container.BindScoped(name, func() interface{} { return closer })
			if _, err := scope.Make(name); err != nil {
				t.Fatal(err)
			}
		}
		result := make(chan error, 1)
		go func() { result <- scope.Close() }()
		synctest.Wait()
		if want := []string{"exit-2", "blocked"}; !reflect.DeepEqual(order, want) {
			t.Errorf("退出后未按序进入阻塞关闭器：实际 %v，预期 %v", order, want)
		}
		var closeErr error
		returned := false
		select {
		case closeErr = <-result:
			returned = true
			t.Errorf("尚有资源未结束时提前报告关闭完成：%v", closeErr)
		default:
		}
		close(release)
		if !returned {
			closeErr = <-result
		}
		want := []string{"exit-2", "blocked", "exit-1", "last"}
		if !reflect.DeepEqual(order, want) || !errors.Is(closeErr, lastErr) || !errors.Is(closeErr, cleanup.ErrAborted) {
			t.Fatalf("关闭未完成全部资源或丢失原错误：顺序 %v，错误 %v", order, closeErr)
		}
		if repeated := scope.Close(); repeated != closeErr || !reflect.DeepEqual(order, want) {
			t.Fatalf("重复关闭重新执行了资源或改变错误：%v，%v", order, repeated)
		}
	})
}
