package context

import (
	"errors"
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"
	"testing/synctest"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cleanup"
)

type exitingRequestScope struct {
	close func() error
}

func (*exitingRequestScope) Make(string, ...interface{}) (interface{}, error) {
	return nil, nil
}

func (scope *exitingRequestScope) Close() error { return scope.close() }

// 多次退出与阻塞交错时，Cleanup 必须等待全部已登记关闭器的同一次逆序尝试。
func TestRequestCleanupContinuesAfterGoexit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request, err := NewRequest(httptest.NewRequest("GET", "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		release := make(chan struct{})
		lastErr := errors.New("最后一个请求作用域关闭失败")
		for _, closeScope := range []func() error{
			func() error { order = append(order, "last"); return lastErr },
			func() error { order = append(order, "exit-1"); runtime.Goexit(); return nil },
			func() error { order = append(order, "blocked"); <-release; return nil },
			func() error { order = append(order, "exit-2"); runtime.Goexit(); return nil },
		} {
			scope := &exitingRequestScope{close: closeScope}
			if err := WithServiceScope(scope, scope)(request); err != nil {
				t.Fatal(err)
			}
			// 相同作用域重复绑定不能增加第二次关闭责任。
			if err := WithServiceScope(scope, scope)(request); err != nil {
				t.Fatal(err)
			}
		}
		result := make(chan error, 1)
		go func() { result <- request.Cleanup() }()
		synctest.Wait()
		if want := []string{"exit-2", "blocked"}; !reflect.DeepEqual(order, want) {
			t.Errorf("退出后未按序进入阻塞关闭器：实际 %v，预期 %v", order, want)
		}
		var cleanupErr error
		returned := false
		select {
		case cleanupErr = <-result:
			returned = true
			t.Errorf("尚有请求资源未结束时提前发布完成：%v", cleanupErr)
		default:
		}
		close(release)
		if !returned {
			cleanupErr = <-result
		}
		want := []string{"exit-2", "blocked", "exit-1", "last"}
		if !reflect.DeepEqual(order, want) || !errors.Is(cleanupErr, lastErr) || !errors.Is(cleanupErr, cleanup.ErrAborted) {
			t.Fatalf("清理未完成全部资源或丢失原错误：顺序 %v，错误 %v", order, cleanupErr)
		}
		if repeated := request.Cleanup(); repeated != cleanupErr || !reflect.DeepEqual(order, want) {
			t.Fatalf("重复清理重新执行了资源或改变错误：%v，%v", order, repeated)
		}
	})
}
