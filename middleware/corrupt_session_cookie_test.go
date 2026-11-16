package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func TestCorruptSessionStopsRequestAndClearsOnlyMatchingCookie(t *testing.T) {
	const otherID = "feedfacefeedfacefeedfacefeedface"
	const oversize = 256
	for _, test := range []struct {
		name, payload, presentedID, requestedID string
		clearCookie                             bool
	}{
		{"损坏记录", `{"version":1,"data":`, middlewareKnownSessionID, "", true},
		{"超限记录", `{"version":1,"data":{"secret":"` + strings.Repeat("x", oversize) + `"}}`, middlewareKnownSessionID, "", true},
		{"显式 ID 与 Cookie 一致", `{"version":99,"data":{}}`, middlewareKnownSessionID, middlewareKnownSessionID, true},
		{"显式 ID 与 Cookie 不同", `{"version":99,"data":{}}`, otherID, middlewareKnownSessionID, false},
		{"只有显式 ID", `{"version":99,"data":{}}`, "", middlewareKnownSessionID, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := driver.NewMemory()
			if err := backend.Write(middlewareKnownSessionID, test.payload); err != nil {
				t.Fatal(err)
			}
			factory, err := cookie.NewCookie(map[string]any{"prefix": "app_"})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := session.NewSession(map[string]any{
				"name": "SID", "var_session_id": "upload_sid", "max_data_bytes": oversize,
				"cookie_path": "/account", "domain": "example.com",
			}, backend, factory)
			if err != nil {
				t.Fatal(err)
			}
			mw := &Session{Manager: manager}
			const address = "https://example.com/account"
			raw := httptest.NewRequest(http.MethodPost, address, strings.NewReader("upload_sid="+test.requestedID))
			raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.presentedID != "" {
				raw.AddCookie(&http.Cookie{Name: "app_SID", Value: test.presentedID})
			}
			response := mw.Handle(context.MustNewRequest(raw), func(*context.Request) *context.Response {
				t.Error("损坏会话不能执行当前业务动作")
				return context.NewResponse()
			})
			writer := httptest.NewRecorder()
			if err := response.Send(writer); err != nil {
				t.Fatal(err)
			}
			if writer.Code != http.StatusInternalServerError {
				t.Fatalf("首次请求必须报告初始化失败: %d, %q", writer.Code, writer.Body.String())
			}
			cookies := writer.Result().Cookies()
			if test.clearCookie {
				if len(cookies) != 1 || cookies[0].Name != "app_SID" || cookies[0].Value != "" || cookies[0].MaxAge >= 0 ||
					cookies[0].Path != "/account" || cookies[0].Domain != "example.com" || !cookies[0].Secure || !cookies[0].HttpOnly {
					t.Fatalf("删除 Cookie 的行为或作用域错误: %v", cookies)
				}
			} else if len(cookies) != 0 {
				t.Fatalf("错误 ID 不应清除无关 Cookie: %v", cookies)
			}

			if value, found, err := backend.Read(middlewareKnownSessionID); err != nil || !found || value != test.payload {
				t.Fatalf("清除客户端 Cookie 不应删除故障记录: found=%t err=%v", found, err)
			}
			fresh := mw.Handle(context.MustNewRequest(httptest.NewRequest(http.MethodGet, address, nil)), func(req *context.Request) *context.Response {
				if _, ok := req.GetData("_session").(*session.Session); !ok {
					t.Error("下一次请求应重建会话")
				}
				return context.NewResponse().Content("继续")
			})
			if fresh.GetStatus() != http.StatusOK {
				t.Fatalf("移除坏 Cookie 后请求未恢复: %d", fresh.GetStatus())
			}
		})
	}
}
