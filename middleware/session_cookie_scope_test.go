package middleware

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
	"github.com/zhuhanxin0308/thinkgo/v3/session"
	"github.com/zhuhanxin0308/thinkgo/v3/session/driver"
)

func TestSessionCommitPreservesOtherCookieScopes(t *testing.T) {
	const name = "app_SID"
	const path = "/session"
	const domain = "example.com"
	for _, test := range []struct {
		label         string
		rotateAtEnd   bool
		sessionDomain string
	}{
		{"提前保存后提交", false, domain},
		{"最终提交时写入并继承域", true, ""},
	} {
		t.Run(test.label, func(t *testing.T) {
			factory, err := cookie.NewCookie(map[string]interface{}{"prefix": "app_", "domain": domain})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := session.NewSession(map[string]interface{}{
				"name": "SID", "cookie_path": path, "domain": test.sessionDomain,
			}, driver.NewMemory(), factory)
			if err != nil {
				t.Fatal(err)
			}
			business := []string{
				name + "=preferences; Path=/prefs; Domain=example.com",
				name + "=first; Path=/session; Secure",
				name + "=second; Path=/session; Secure",
				name + "=partition; Path=/session; Domain=example.com; Secure; Partitioned",
			}
			writer := &middlewareCommitHookWriter{}
			raw := httptest.NewRequest(http.MethodGet, "https://app.example.com/session", nil)
			req := context.MustNewRequest(raw, context.WithResponseWriter(writer))
			var firstID string
			mw := &Session{Manager: manager}
			mw.Handle(req, func(req *context.Request) *context.Response {
				current := req.GetData("_session").(*session.Session)
				if err := current.SetResponseWriter(writer); err != nil {
					t.Fatal(err)
				}
				writer.Header().Add("Set-Cookie", business[0])
				if err := current.Set("active", true); err != nil {
					t.Fatal(err)
				}
				if err := current.Save(); err != nil {
					t.Fatal(err)
				}
				firstID = sessionCookiesNamed(t, writer.Header(), name)[1].Value
				if err := current.Regenerate(); err != nil {
					t.Fatal(err)
				}
				if !test.rotateAtEnd {
					if err := current.Save(); err != nil {
						t.Fatal(err)
					}
				}
				for _, value := range business[1:] {
					writer.Header().Add("Set-Cookie", value)
				}
				return context.NewCommittedResponse(http.StatusOK)
			})
			if err := writer.commit(); err != nil {
				t.Fatal(err)
			}
			var kept []string
			var sessionCookies []*http.Cookie
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, rawCookie := range writer.Header().Values("Set-Cookie") {
				item, err := http.ParseSetCookie(rawCookie)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(business, rawCookie) {
					kept = append(kept, rawCookie)
				} else {
					sessionCookies = append(sessionCookies, item)
				}
				// 标准库 Jar 不区分分区 Cookie，该项另用原始响应头校验。
				if !item.Partitioned {
					jar.SetCookies(raw.URL, []*http.Cookie{item})
				}
			}
			if !slices.Equal(kept, business) || len(sessionCookies) != 1 || sessionCookies[0].Value == firstID {
				t.Fatalf("业务 Cookie 或最终会话不完整：%v", writer.Header().Values("Set-Cookie"))
			}
			for requestPath, want := range map[string]string{"/prefs/view": "preferences", "/session/view": sessionCookies[0].Value} {
				url := httptest.NewRequest(http.MethodGet, "https://sibling.example.com"+requestPath, nil).URL
				if got := jar.Cookies(url); len(got) != 1 || got[0].Name != name || got[0].Value != want {
					t.Fatalf("路径 %s 的 Cookie 有误：%v", requestPath, got)
				}
			}
			next := httptest.NewRequest(http.MethodGet, "https://sibling.example.com/session/view", nil)
			for _, item := range jar.Cookies(next.URL) {
				next.AddCookie(item)
			}
			resumed, err := manager.NewRequestSession(next, nil)
			if err != nil || !resumed.Has("active") {
				t.Fatalf("轮换后的会话未恢复：%v", err)
			}
		})
	}
}
