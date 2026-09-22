package framework

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/view"
	viewDriver "github.com/zhuhanxin0308/thinkgo/framework/view/driver"
)

// TestControllerViewUsesRequestLanguage 验证缓存模板在并发请求中使用各自语言且不会串请求。
func TestControllerViewUsesRequestLanguage(t *testing.T) {
	basePath := t.TempDir()
	viewPath := filepath.Join(basePath, "view")
	if err := os.MkdirAll(viewPath, 0o755); err != nil {
		t.Fatalf("创建视图目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(viewPath, "welcome.html"), []byte(`{{lang "welcome"}}`), 0o600); err != nil {
		t.Fatalf("写入测试模板失败: %v", err)
	}
	zhFile := filepath.Join(basePath, "zh-cn.json")
	enFile := filepath.Join(basePath, "en-us.json")
	if err := os.WriteFile(zhFile, []byte(`{"welcome":"你好"}`), 0o600); err != nil {
		t.Fatalf("写入中文语言包失败: %v", err)
	}
	if err := os.WriteFile(enFile, []byte(`{"welcome":"Hello"}`), 0o600); err != nil {
		t.Fatalf("写入英文语言包失败: %v", err)
	}

	app := NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.lang.Load(zhFile, "zh-cn"); err != nil {
		t.Fatalf("加载中文语言包失败: %v", err)
	}
	if err := app.lang.Load(enFile, "en-us"); err != nil {
		t.Fatalf("加载英文语言包失败: %v", err)
	}
	manager := view.NewView(nil, map[string]interface{}{
		"view_path":   viewPath,
		"view_suffix": "html",
		"cache":       true,
	})
	if err := manager.SetDriver(viewDriver.NewGoTemplate()); err != nil {
		t.Fatalf("安装模板驱动失败: %v", err)
	}
	if err := manager.SetFuncMap(map[string]interface{}{
		"lang": func(key string) string { return app.lang.Get(key, nil, "") },
	}); err != nil {
		t.Fatalf("注册模板语言函数失败: %v", err)
	}
	app.view = manager

	const workers = 64
	start := make(chan struct{})
	errCh := make(chan string, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		language := "zh-cn"
		expected := "你好"
		if index%2 == 1 {
			language = "en-us"
			expected = "Hello"
		}
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
			request.Set(LangRequestKey, language)
			controller := &Controller{App: app, Request: request}
			<-start
			if actual := controller.View("welcome"); actual != expected {
				errCh <- language + ":" + actual
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errCh)
	for result := range errCh {
		t.Errorf("请求语言模板渲染错误: %s", result)
	}
	if fallback, err := manager.Fetch("welcome", nil); err != nil || fallback != "你好" {
		t.Fatalf("请求级函数不得污染全局默认语言: content=%q err=%v", fallback, err)
	}
}
