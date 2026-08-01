package framework

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	frameworkcontext "thinkgo/framework/context"
	"thinkgo/framework/lang"
)

func newLanguageMiddlewareApp(t *testing.T, config map[string]interface{}) *App {
	t.Helper()
	directory := t.TempDir()
	for name, body := range map[string]string{
		"zh-cn": `{"title":"中文"}`,
		"en-us": `{"title":"English"}`,
		"ja-jp": `{"title":"日本語"}`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatalf("写入语言包 %s 失败: %v", name, err)
		}
	}
	manager := lang.NewLang()
	if err := manager.Init(config); err != nil {
		t.Fatalf("初始化语言配置失败: %v", err)
	}
	if err := manager.LoadAll(directory); err != nil {
		t.Fatalf("加载语言包失败: %v", err)
	}
	return &App{lang: manager}
}

func runLanguageMiddleware(t *testing.T, app *App, raw *http.Request) string {
	t.Helper()
	request := frameworkcontext.MustNewRequest(raw)
	called := false
	response := app.LoadLangPack()(request, func(current *frameworkcontext.Request) *frameworkcontext.Response {
		called = true
		return frameworkcontext.NewResponse().Content(current.Route(LangRequestKey))
	})
	if !called || response == nil {
		t.Fatal("语言中间件必须继续执行下游处理器")
	}
	selected, _ := request.GetData(LangRequestKey).(string)
	return selected
}

// TestLoadLangPackHonorsConfiguredSources 验证自定义参数名、Cookie、语言头及候选合法性按稳定优先级生效。
func TestLoadLangPackHonorsConfiguredSources(t *testing.T) {
	app := newLanguageMiddlewareApp(t, map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": true,
		"allow_lang_list":     []interface{}{"zh-cn", "en-us", "ja-jp"},
		"detect_var":          "locale",
		"use_cookie":          true,
		"cookie_var":          "locale_cookie",
		"header_var":          "X-App-Lang",
	})

	raw := httptest.NewRequest(http.MethodGet, "http://example.com/?locale=unsupported", nil)
	raw.AddCookie(&http.Cookie{Name: "locale_cookie", Value: "en-us"})
	raw.Header.Set("X-App-Lang", "ja-jp")
	if selected := runLanguageMiddleware(t, app, raw); selected != "en-us" {
		t.Fatalf("不支持的查询参数应回落到 Cookie，实际为 %q", selected)
	}

	headerRequest := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	headerRequest.Header.Set("X-App-Lang", "ja-jp")
	if selected := runLanguageMiddleware(t, app, headerRequest); selected != "ja-jp" {
		t.Fatalf("自定义语言头应生效，实际为 %q", selected)
	}

	queryRequest := httptest.NewRequest(http.MethodGet, "http://example.com/?locale=zh-cn", nil)
	queryRequest.AddCookie(&http.Cookie{Name: "locale_cookie", Value: "en-us"})
	if selected := runLanguageMiddleware(t, app, queryRequest); selected != "zh-cn" {
		t.Fatalf("合法查询参数应具有最高优先级，实际为 %q", selected)
	}
}

// TestLoadLangPackHonorsAcceptLanguageQuality 验证 q=0 被排除且最高权重语言优先。
func TestLoadLangPackHonorsAcceptLanguageQuality(t *testing.T) {
	app := newLanguageMiddlewareApp(t, map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": true,
		"allow_lang_list":     []interface{}{"zh-cn", "en-us", "ja-jp"},
		"detect_var":          "lang",
		"use_cookie":          false,
		"cookie_var":          "lang",
		"header_var":          "X-App-Lang",
	})
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("Accept-Language", "en-US;q=0, zh-CN;q=0.5, ja-JP;q=1")
	if selected := runLanguageMiddleware(t, app, raw); selected != "ja-jp" {
		t.Fatalf("Accept-Language 权重解析错误，实际为 %q", selected)
	}

	nanRequest := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	nanRequest.Header.Set("Accept-Language", "en-US;q=NaN, ja-JP;q=0.5")
	if selected := runLanguageMiddleware(t, app, nanRequest); selected != "ja-jp" {
		t.Fatalf("非有限 q 值必须被拒绝，实际为 %q", selected)
	}
}

// TestLoadLangPackRespectsAllowListAndBrowserSwitch 验证未允许语言和关闭浏览器检测时统一回退默认语言。
func TestLoadLangPackRespectsAllowListAndBrowserSwitch(t *testing.T) {
	app := newLanguageMiddlewareApp(t, map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": false,
		"allow_lang_list":     []interface{}{"zh-cn", "en-us"},
		"detect_var":          "lang",
		"use_cookie":          false,
		"cookie_var":          "lang",
		"header_var":          "X-App-Lang",
	})
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("X-App-Lang", "ja-jp")
	raw.Header.Set("Accept-Language", "en-US")
	if selected := runLanguageMiddleware(t, app, raw); selected != "zh-cn" {
		t.Fatalf("未允许的显式语言和关闭的浏览器检测应回退默认语言，实际为 %q", selected)
	}
}
