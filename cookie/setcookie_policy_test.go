package cookie

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetCookiePolicyControlsEveryWrite(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		factory, err := NewCookie(map[string]interface{}{"setcookie": enabled})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "https://example.com", nil)
		request.AddCookie(&http.Cookie{Name: "pref", Value: "readable"})
		for _, operation := range []string{"Set", "Delete", "BuildHeader"} {
			recorder := httptest.NewRecorder()
			current, err := factory.ForRequest(request, recorder)
			if err != nil {
				t.Fatal(err)
			}
			if current.GetConfig().SetCookie != enabled {
				t.Fatal("请求工厂丢失 setcookie 配置")
			}
			switch operation {
			case "Set":
				err = current.Set("pref", "new")
			case "Delete":
				err = current.Delete("pref")
			case "BuildHeader":
				var header string
				header, err = current.BuildHeader("pref", "new")
				if (header != "") != enabled {
					t.Errorf("BuildHeader 的头值与禁发策略不一致: %q", header)
				}
			}
			if (err == nil) != enabled {
				t.Errorf("%s enabled=%v, err=%v", operation, enabled, err)
			}
			if !enabled && !errors.Is(err, ErrCookieDisabled) {
				t.Errorf("%s 未返回明确禁发错误: %v", operation, err)
			}
			wantHeader := enabled && operation != "BuildHeader"
			if (len(recorder.Header().Values("Set-Cookie")) > 0) != wantHeader {
				t.Errorf("%s 实际头部与策略不一致: %v", operation, recorder.Header())
			}
			if value, found, err := current.Get("pref"); err != nil || !found || value != "readable" {
				t.Fatalf("禁发策略影响了请求读取: %q %v %v", value, found, err)
			}
		}
	}
}
