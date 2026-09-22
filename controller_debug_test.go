package framework

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/v3/cache/driver"
	frameworkContext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	"github.com/zhuhanxin0308/thinkgo/v3/view"
)

type controllerDebugViewDriver struct{}

func (d *controllerDebugViewDriver) Config(map[string]interface{}) error { return nil }

func (d *controllerDebugViewDriver) Fetch(name string, _ map[string]interface{}) (string, error) {
	return name, nil
}

func (d *controllerDebugViewDriver) Display(writer io.Writer, name string, _ map[string]interface{}) error {
	_, err := io.WriteString(writer, name)
	return err
}

func (d *controllerDebugViewDriver) Exists(string) (bool, error) { return true, nil }

func (d *controllerDebugViewDriver) SetFuncMap(map[string]interface{}) error { return nil }

// TestControllerRequestDebugIsolation 验证控制器把缓存和模板记录显式写入当前请求 collector。
func TestControllerRequestDebugIsolation(t *testing.T) {
	rootCache := cache.NewCache(nil, cacheDriver.NewMemory())
	rootView := view.NewView(nil, nil)
	if err := rootView.SetDriver(&controllerDebugViewDriver{}); err != nil {
		t.Fatalf("安装控制器测试视图驱动失败: %v", err)
	}
	app := &App{cache: rootCache, view: rootView}

	collectorA := debug.NewRequestDebug(true)
	collectorB := debug.NewRequestDebug(true)
	controllerA := newDebugTestController(app, collectorA)
	controllerB := newDebugTestController(app, collectorB)

	if output := controllerA.View("controller-a"); output != "controller-a" {
		t.Fatalf("请求 A 控制器视图输出错误: %q", output)
	}
	if err := controllerA.RequestCache().Set("controller-cache-a", true, time.Minute); err != nil {
		t.Fatalf("请求 A 控制器缓存写入失败: %v", err)
	}
	if output := controllerB.Fetch("controller-b"); output != "controller-b" {
		t.Fatalf("请求 B 控制器视图输出错误: %q", output)
	}
	if value, found, err := controllerB.RequestCache().Get("controller-cache-a"); err != nil || !found || value != true {
		t.Fatalf("请求 facade 应共享根缓存状态: value=%#v found=%t err=%v", value, found, err)
	}
	if err := rootCache.Set("root-cache", true, 0); err != nil {
		t.Fatalf("根缓存写入失败: %v", err)
	}

	if files := controllerDebugFiles(collectorA); !reflect.DeepEqual(files, []string{"controller-a"}) {
		t.Fatalf("请求 A 模板记录隔离错误: %#v", files)
	}
	if files := controllerDebugFiles(collectorB); !reflect.DeepEqual(files, []string{"controller-b"}) {
		t.Fatalf("请求 B 模板记录隔离错误: %#v", files)
	}
	keysA := controllerDebugCacheKeys(collectorA)
	keysB := controllerDebugCacheKeys(collectorB)
	if !keysA["controller-cache-a"] || keysA["root-cache"] {
		t.Fatalf("请求 A 缓存记录隔离错误: %#v", keysA)
	}
	if !keysB["controller-cache-a"] || keysB["root-cache"] {
		t.Fatalf("请求 B 缓存记录隔离错误: %#v", keysB)
	}
}

// TestControllerDebugCompatibilityAliasAndNilBoundaries 验证兼容键别名以及无 App/Cache 时的安全边界。
func TestControllerDebugCompatibilityAliasAndNilBoundaries(t *testing.T) {
	if DebugRequestKey != debug.RequestKey {
		t.Fatalf("framework 调试键必须引用 debug 统一键，framework=%q debug=%q", DebugRequestKey, debug.RequestKey)
	}
	var controller *Controller
	if requestCache := controller.RequestCache(); requestCache != nil {
		t.Fatalf("空控制器不应返回请求缓存，实际为 %#v", requestCache)
	}
	controller = &Controller{}
	if requestCache := controller.RequestCache(); requestCache != nil {
		t.Fatalf("无 App 控制器不应返回请求缓存，实际为 %#v", requestCache)
	}
	controller.App = &App{}
	if requestCache := controller.RequestCache(); requestCache != nil {
		t.Fatalf("无根缓存控制器不应返回请求缓存，实际为 %#v", requestCache)
	}
}

// TestAppRootCacheAndViewDoNotCollectRequestData 验证应用根服务只提供共享能力，不再保存跨请求调试数据。
func TestAppRootCacheAndViewDoNotCollectRequestData(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	// 测试配置显式使用 app/view，因此模板也应写入该目录；根 view
	// 只在没有显式 view_path 时参与 ThinkPHP 的默认目录回退。
	viewPath := filepath.Join(basePath, "app", "view")
	if err := os.MkdirAll(viewPath, 0o755); err != nil {
		t.Fatalf("创建测试视图目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(viewPath, "root.html"), []byte("root-view"), 0o600); err != nil {
		t.Fatalf("写入测试模板失败: %v", err)
	}

	app := mustBuildTestApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.StartupError(); err != nil {
		t.Fatalf("初始化测试应用失败: %v", err)
	}
	app.debug.Enabled = true
	if err := app.cache.Set("root-cache", true, 0); err != nil {
		t.Fatalf("应用根缓存写入失败: %v", err)
	}
	var output strings.Builder
	if err := app.view.Render(&output, "root", nil); err != nil {
		t.Fatalf("应用根视图渲染失败: %v", err)
	}
	if output.String() != "root-view" {
		t.Fatalf("应用根视图输出错误: %q", output.String())
	}

	info := app.debug.GetInfo()
	if entries := info["cache"].([]map[string]interface{}); len(entries) != 0 {
		t.Fatalf("应用根缓存不得写入配置级 Debug，实际为 %#v", entries)
	}
	if files := info["files"].([]string); len(files) != 0 {
		t.Fatalf("应用根视图不得写入配置级 Debug，实际为 %#v", files)
	}
}

func newDebugTestController(app *App, collector *debug.Debug) *Controller {
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com", nil))
	request.Set(debug.RequestKey, collector)
	return &Controller{App: app, Request: request}
}

func controllerDebugFiles(collector *debug.Debug) []string {
	return collector.GetInfo()["files"].([]string)
}

func controllerDebugCacheKeys(collector *debug.Debug) map[string]bool {
	keys := make(map[string]bool)
	for _, entry := range collector.GetInfo()["cache"].([]map[string]interface{}) {
		key, _ := entry["key"].(string)
		keys[key] = true
	}
	return keys
}
