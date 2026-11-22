package cookie

import (
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDeleteWithOptionsRemovesOnlyTheSetScope(t *testing.T) {
	for _, test := range []struct{ name, secret string }{
		{"未签名", ""},
		{"签名", strings.Repeat("s", minCookieSecretBytes)},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "https://account.example.com/account/", nil)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			config := DefaultConfig()
			config.Secret, config.Prefix = test.secret, "app_"
			rec := httptest.NewRecorder()
			current, err := NewCookieForRequest(config, req, rec)
			if err != nil {
				t.Fatal(err)
			}
			target := CookieOptions{Path: "/account", Domain: "example.com", SameSite: "Strict", Expire: 60}
			if err := current.Set("pref", "keep"); err != nil {
				t.Fatal(err)
			}
			if err := current.Set("pref", "target", target); err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(req.URL, rec.Result().Cookies())
			before := jar.Cookies(req.URL)
			if len(before) != 2 {
				t.Fatalf("两个同名 Cookie 应共存：%v", before)
			}
			rec = httptest.NewRecorder()
			if err := current.SetWriter(rec); err != nil {
				t.Fatal(err)
			}
			if err := current.DeleteWithOptions("pref", target); err != nil {
				t.Fatal(err)
			}
			deleted := rec.Result().Cookies()
			if len(deleted) != 1 {
				t.Fatalf("期望一个删除头：%v", deleted)
			}
			got := deleted[0]
			if got.Name != "app_pref" || got.Path != target.Path || got.Domain != target.Domain ||
				got.MaxAge >= 0 || !got.Expires.Before(time.Now()) || !got.Secure || !got.HttpOnly || got.SameSite != http.SameSiteStrictMode {
				t.Fatalf("作用域、有效期或安全属性错误：%#v", got)
			}
			jar.SetCookies(req.URL, deleted)
			after := jar.Cookies(req.URL)
			if len(after) != 1 || after[0].Value != before[1].Value {
				t.Fatalf("应仅保留根路径的 Cookie：%v", after)
			}
			sibling := httptest.NewRequest(http.MethodGet, "https://www.example.com/account/", nil)
			if got := jar.Cookies(sibling.URL); len(got) != 0 {
				t.Fatalf("跨子域的 Cookie 未清除：%v", got)
			}
		})
	}
}

func TestDeleteWithOptionsPropagatesErrors(t *testing.T) {
	var current *Cookie
	if err := current.DeleteWithOptions("pref", CookieOptions{}); !errors.Is(err, ErrCookieWriterUnavailable) {
		t.Fatalf("缺少 writer 的错误为 %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	current, err := NewCookieForRequest(DefaultConfig(), req, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.DeleteWithOptions("pref", CookieOptions{Path: "relative"}); !errors.Is(err, ErrInvalidCookieOptions) {
		t.Fatalf("作用域校验错误为 %v", err)
	}
	if len(rec.Header().Values("Set-Cookie")) != 0 {
		t.Fatal("失败的删除写入了响应")
	}
}
