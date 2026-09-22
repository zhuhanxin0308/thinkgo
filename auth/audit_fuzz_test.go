package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

// FuzzBearerRequestIsolation 验证任意未知 token 和重复头均不能取得已授权主体，也不能污染下一请求。
func FuzzBearerRequestIsolation(f *testing.F) {
	for _, token := range []string{"", "unknown", "known", "known\r\n", "中文"} {
		f.Add(token, false)
		f.Add(token, true)
	}
	identity, err := NewIdentity("owner", nil, nil, nil)
	if err != nil {
		f.Fatal(err)
	}
	store, err := NewStaticTokenStore(map[string]Identity{"known": identity})
	if err != nil {
		f.Fatal(err)
	}
	authenticator, err := Bearer(store)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, token string, duplicate bool) {
		if len(token) > 9000 {
			return
		}
		raw := httptest.NewRequest(http.MethodGet, "/", nil)
		raw.Header.Add("Authorization", "Bearer "+token)
		if duplicate {
			raw.Header.Add("Authorization", "Bearer known")
		}
		request := fwcontext.MustNewRequest(raw)
		actual, err := authenticator.Authenticate(t.Context(), request)
		allowed := token == "known" && !duplicate
		if allowed && (err != nil || actual.Subject != "owner") || !allowed && err == nil {
			t.Fatal("凭据判定越过身份边界")
		}
		if err := request.Cleanup(); err != nil {
			t.Fatal(err)
		}
		anonymous := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
		if _, err := authenticator.Authenticate(t.Context(), anonymous); err == nil {
			t.Fatal("身份跨请求泄漏")
		}
		if err := anonymous.Cleanup(); err != nil {
			t.Fatal(err)
		}
	})
}
