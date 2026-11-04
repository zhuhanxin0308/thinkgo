package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func TestSessionCommitDeduplicatesActualCookieNameAfterRegenerate(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		secret string
	}{
		{name: "unprefixed"},
		{name: "prefixed", prefix: "tenant_"},
		{name: "unprefixed signed", secret: strings.Repeat("s", 32)},
		{name: "prefixed signed", prefix: "tenant_", secret: strings.Repeat("s", 32)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cookieFactory, err := cookie.NewCookie(map[string]interface{}{"prefix": test.prefix, "secret": test.secret})
			if err != nil {
				t.Fatalf("创建 Cookie 工厂失败: %v", err)
			}
			manager, err := session.NewSession(map[string]interface{}{
				"name":        "SID",
				"cookie_path": "/session",
				"domain":      "example.com",
				"secure":      true,
				"samesite":    "Strict",
				"expire":      600,
			}, driver.NewMemory(), cookieFactory)
			if err != nil {
				t.Fatalf("创建 Session 管理器失败: %v", err)
			}
			actualName := test.prefix + "SID"
			otherCookies := []string{
				"theme=dark; Path=/; SameSite=Lax",
				actualName + "_hint=one; Path=/prefs",
				actualName + "_hint=two; Path=/other",
				"x" + actualName + "=other; Path=/",
			}
			if test.prefix != "" {
				otherCookies = append(otherCookies, "SID=application-owned; Path=/")
			}
			writer := &middlewareCommitHookWriter{}
			for _, value := range otherCookies {
				writer.Header().Add("Set-Cookie", value)
			}
			decodeID := func(item *http.Cookie) string {
				t.Helper()
				raw := httptest.NewRequest(http.MethodGet, "https://example.com/session", nil)
				raw.AddCookie(item)
				reader, err := cookieFactory.ForRequest(raw, nil)
				if err != nil {
					t.Fatalf("绑定 Cookie 读取器失败: %v", err)
				}
				id, found, err := reader.Get("SID")
				if err != nil || !found || id == "" {
					t.Fatalf("实际 Cookie 必须可验证并还原会话 ID: found=%t err=%v", found, err)
				}
				return id
			}
			req := context.MustNewRequest(
				httptest.NewRequest(http.MethodGet, "https://example.com/session", nil),
				context.WithResponseWriter(writer),
			)
			mw := &Session{Manager: manager}
			var requestSession *session.Session
			var firstID, rotatedID string
			response := mw.Handle(req, func(req *context.Request) *context.Response {
				requestSession = req.GetData("_session").(*session.Session)
				if err := requestSession.SetResponseWriter(writer); err != nil {
					t.Fatalf("绑定中途保存 writer 失败: %v", err)
				}
				if err := requestSession.Set("stage", "first"); err != nil {
					t.Fatalf("设置初始 Session 失败: %v", err)
				}
				if err := requestSession.Save(); err != nil {
					t.Fatalf("首次保存失败: %v", err)
				}
				cookies := sessionCookiesNamed(t, writer.Header(), actualName)
				if len(cookies) != 1 {
					t.Fatalf("首次保存应生成一个实际名称 Cookie，实际为 %d", len(cookies))
				}
				firstID = decodeID(cookies[0])
				if err := requestSession.Regenerate(); err != nil {
					t.Fatalf("轮换会话 ID 失败: %v", err)
				}
				if err := requestSession.Set("stage", "second"); err != nil {
					t.Fatalf("设置轮换后 Session 失败: %v", err)
				}
				if err := requestSession.Save(); err != nil {
					t.Fatalf("轮换后保存失败: %v", err)
				}
				cookies = sessionCookiesNamed(t, writer.Header(), actualName)
				if len(cookies) != 2 {
					t.Fatalf("最终提交前应存在两个阶段的 Cookie，实际为 %d", len(cookies))
				}
				rotatedID = decodeID(cookies[1])
				if firstID == rotatedID {
					t.Fatal("测试必须实际轮换会话 ID")
				}
				return context.NewCommittedResponse(http.StatusOK)
			})
			if response == nil || !response.Committed() || requestSession == nil {
				t.Fatalf("响应必须等待已注册的 writer 提交钩子: response=%#v", response)
			}
			if err := requestSession.Regenerate(); err != nil {
				t.Fatalf("最终提交前再次轮换会话 ID 失败: %v", err)
			}
			if err := requestSession.Set("final", "saved-at-commit"); err != nil {
				t.Fatalf("设置最终提交前的变更失败: %v", err)
			}
			if err := writer.commit(); err != nil {
				t.Fatalf("最终响应提交失败: %v", err)
			}
			cookies := sessionCookiesNamed(t, writer.Header(), actualName)
			if len(cookies) != 1 {
				t.Fatalf("所有保存阶段必须只留下最终实际名称 Cookie，实际为 %d", len(cookies))
			}
			finalCookie := cookies[0]
			if finalID := decodeID(finalCookie); finalID == firstID || finalID == rotatedID {
				t.Fatal("最终提交必须保留最后轮换的会话 ID，不能保留中间保存阶段的 Cookie")
			}
			if finalCookie.Path != "/session" || finalCookie.Domain != "example.com" || !finalCookie.Secure || !finalCookie.HttpOnly || finalCookie.SameSite != http.SameSiteStrictMode || finalCookie.MaxAge != 600 {
				t.Fatalf("去重不得改变最终 Cookie 属性: %#v", finalCookie)
			}
			var retainedOthers []string
			for _, value := range writer.Header().Values("Set-Cookie") {
				parsed, err := http.ParseSetCookie(value)
				if err != nil {
					t.Fatalf("最终 Cookie 语法错误: %v", err)
				}
				if parsed.Name != actualName {
					retainedOthers = append(retainedOthers, value)
				}
			}
			if !reflect.DeepEqual(retainedOthers, otherCookies) {
				t.Fatalf("其他 Cookie（包括相似名称和重复值）必须按原顺序完整保留: got=%v want=%v", retainedOthers, otherCookies)
			}
			resumedRequest := httptest.NewRequest(http.MethodGet, "https://example.com/session", nil)
			resumedRequest.AddCookie(finalCookie)
			resumed, err := manager.NewRequestSession(resumedRequest, nil)
			if err != nil {
				t.Fatalf("最终 Cookie 恢复 Session 失败: %v", err)
			}
			if value, found := resumed.Get("final"); !found || value != "saved-at-commit" {
				t.Fatalf("最终提交的会话数据必须可恢复: value=%v found=%t", value, found)
			}
			if value, found := resumed.Get("stage"); !found || value != "second" {
				t.Fatalf("轮换前的数据必须保留: value=%v found=%t", value, found)
			}
		})
	}
}

func sessionCookiesNamed(t *testing.T, header http.Header, name string) []*http.Cookie {
	t.Helper()
	var cookies []*http.Cookie
	for _, value := range header.Values("Set-Cookie") {
		parsed, err := http.ParseSetCookie(value)
		if err != nil {
			t.Fatalf("解析 Cookie 失败: %v", err)
		}
		if parsed.Name == name {
			cookies = append(cookies, parsed)
		}
	}
	return cookies
}
