package session

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/session/driver"
)

func TestCommitResponseSealsEverySessionMutationAPI(t *testing.T) {
	manager := newTestSessionManager(t, driver.NewMemory(), map[string]interface{}{"name": "BOUNDARYSID"}, nil)
	recorder := httptest.NewRecorder()
	requestSession, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatalf("创建请求 Session 失败: %v", err)
	}
	if err = requestSession.Set("before", "persisted"); err != nil {
		t.Fatalf("设置提交前数据失败: %v", err)
	}
	if err = requestSession.CommitResponse(); err != nil {
		t.Fatalf("提交 Session 响应边界失败: %v", err)
	}
	if len(recorder.Header().Values("Set-Cookie")) != 1 {
		t.Fatalf("提交边界前必须持久化 Cookie: %v", recorder.Header())
	}

	mutations := []struct {
		name string
		call func() error
	}{
		{name: "set", call: func() error { return requestSession.Set("late", true) }},
		{name: "delete", call: func() error { return requestSession.Delete("before") }},
		{name: "clear", call: requestSession.Clear},
		{name: "regenerate", call: requestSession.Regenerate},
		{name: "destroy", call: requestSession.Destroy},
		{name: "writer", call: func() error { return requestSession.SetResponseWriter(httptest.NewRecorder()) }},
		{name: "save", call: requestSession.Save},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.call(); !errors.Is(err, ErrSessionCommitted) {
				t.Fatalf("响应提交后的变更必须返回 ErrSessionCommitted: %v", err)
			}
		})
	}
}
