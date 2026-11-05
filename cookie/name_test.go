package cookie

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestResolveNameMatchesSerializedCookie(t *testing.T) {
	for _, prefix := range []string{"", "tenant_"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			factory, err := NewCookie(map[string]interface{}{"prefix": prefix})
			if err != nil {
				t.Fatalf("创建 Cookie 工厂失败: %v", err)
			}
			name, err := factory.ResolveName("SID")
			if err != nil || name != prefix+"SID" {
				t.Fatalf("实际名称解析错误: name=%q err=%v", name, err)
			}
			header, err := factory.BuildHeader("SID", "value")
			if err != nil {
				t.Fatalf("创建 Cookie 响应头失败: %v", err)
			}
			parsed, err := http.ParseSetCookie(header)
			if err != nil || parsed.Name != name {
				t.Fatalf("名称访问器必须与实际序列化一致: header=%q name=%q err=%v", header, name, err)
			}
		})
	}
}

func TestResolveNameRejectsInvalidNamesAndNilReceiver(t *testing.T) {
	factory, err := NewCookie(map[string]interface{}{"prefix": "tenant_"})
	if err != nil {
		t.Fatalf("创建 Cookie 工厂失败: %v", err)
	}
	for _, name := range []string{"", "bad name", strings.Repeat("n", maxCookieNameBytes)} {
		if resolved, err := factory.ResolveName(name); resolved != "" || !errors.Is(err, ErrInvalidCookieName) {
			t.Fatalf("非法名称必须显式失败: resolved=%q err=%v", resolved, err)
		}
	}
	var missing *Cookie
	if resolved, err := missing.ResolveName("SID"); resolved != "" || !errors.Is(err, ErrInvalidCookieName) {
		t.Fatalf("nil Cookie 名称解析必须显式失败: resolved=%q err=%v", resolved, err)
	}
}
