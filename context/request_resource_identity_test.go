package context

import (
	"io"
	"net/http/httptest"
	"testing"
)

type valueRequestCloser struct {
	count   *int
	payload any
}

func (closer valueRequestCloser) Close() error { *closer.count++; return nil }

// TestRequestResourceIdentity 与容器保持同一身份契约，值资源独立接管且接口内不可比值不触发 panic。
func TestRequestResourceIdentity(t *testing.T) {
	count := 0
	resolver := &requestServiceScopeProbe{}
	value := valueRequestCloser{count: &count}
	uncomparable := valueRequestCloser{count: &count, payload: []string{"值"}}
	pointer := &valueRequestCloser{count: &count}
	request := MustNewRequest(httptest.NewRequest("GET", "/", nil))
	for _, closer := range []io.Closer{value, value, uncomparable, uncomparable, pointer, pointer} {
		if err := WithServiceScope(resolver, closer)(request); err != nil {
			t.Fatal(err)
		}
	}
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("独立值资源与指针别名的关闭次数错误: %d", count)
	}
}
