package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
)

func TestRequestSetSessionUsesTheBoundSessionAndPropagatesErrors(t *testing.T) {
	request := newRequestForTest(t, httptest.NewRequest(http.MethodGet, "http://example.test/", nil))
	if err := request.SetSession("account", "alice"); !errors.Is(err, ErrRequestSessionUnavailable) {
		t.Fatalf("未装配 Session 时应返回明确错误: %v", err)
	}
	current, closeSession := newThinkPHPRequestSession(t)
	defer closeSession()
	request.WithSession(current)
	if got := request.Session("account", "guest"); got != "guest" || current.Has("account") {
		t.Fatal("会话读取的默认值不得写入存储")
	}
	if err := request.SetSession("account", "alice"); err != nil {
		t.Fatal(err)
	}
	if got := request.Session("account"); got != "alice" {
		t.Fatalf("写入应能从当前 Session 读取: %v", got)
	}
	if err := request.SetSession("account", func() {}); !errors.Is(err, frameworksession.ErrInvalidSessionValue) {
		t.Fatalf("应保留 Session 写入的错误信息: %v", err)
	}
	if got := request.Session("account"); got != "alice" {
		t.Fatal("失败的写入不得覆盖已保存的值")
	}
}
