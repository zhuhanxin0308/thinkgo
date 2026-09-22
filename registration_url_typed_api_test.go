package framework

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
)

type discoveredAlphaModel struct {
	ID int
}

type discoveredBetaModel struct {
	ID int
}

// TestApplicationDiscoveryRegistryAPI 验证自动发现代码只需注册模型和路由
// 加载器，App 会稳定排序模型并只执行一次路由加载生命周期。
func TestApplicationDiscoveryRegistryAPI(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("beta", &discoveredBetaModel{}); err != nil {
		t.Fatalf("注册 beta 模型失败: %v", err)
	}
	if err := app.RegisterModel("alpha", discoveredAlphaModel{}); err != nil {
		t.Fatalf("注册 alpha 模型失败: %v", err)
	}
	types := app.RegisteredModelTypes()
	expectedTypes := []reflect.Type{reflect.TypeOf(discoveredAlphaModel{}), reflect.TypeOf(discoveredBetaModel{})}
	if !reflect.DeepEqual(types, expectedTypes) {
		t.Fatalf("模型类型快照未按注册名稳定排序: actual=%#v expected=%#v", types, expectedTypes)
	}
	types[0] = reflect.TypeOf("")
	if !reflect.DeepEqual(app.RegisteredModelTypes(), expectedTypes) {
		t.Fatal("RegisteredModelTypes 不得暴露注册表内部切片")
	}
	if err := app.RegisterModel("alpha", discoveredAlphaModel{}); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("重复模型注册必须返回 ErrDuplicateRegistration: %v", err)
	}
	if err := app.RegisterModel("nil", nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("nil 模型注册必须返回 ErrInvalidRegistration: %v", err)
	}
	if err := app.RegisterModel("scalar", "model"); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("非结构体模型注册必须返回 ErrInvalidRegistration: %v", err)
	}

	loaded := 0
	if err := app.RegisterRouteLoader(func(current *App) error {
		loaded++
		current.Route().Get("discovered", "index/discovered")
		return nil
	}); err != nil {
		t.Fatalf("注册路由加载器失败: %v", err)
	}
	if err := app.LoadRoutes(); err != nil {
		t.Fatalf("加载自动发现路由失败: %v", err)
	}
	if err := app.LoadRoutes(); err != nil || loaded != 1 {
		t.Fatalf("路由加载生命周期必须只执行一次: loaded=%d err=%v", loaded, err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/discovered", nil))
	matched, _, err := app.Route().Match(request)
	if err != nil || matched == nil || matched.Handler() != "index/discovered" {
		t.Fatalf("自动发现路由未进入 Route facade: route=%#v err=%v", matched, err)
	}

	if err := app.RegisterRouteLoader(nil); !errors.Is(err, ErrInvalidRegistration) {
		t.Fatalf("nil 路由加载器必须返回 ErrInvalidRegistration: %v", err)
	}
	var nilApp *App
	if err := nilApp.RegisterModel("model", discoveredAlphaModel{}); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.RegisterModel 错误: %v", err)
	}
	if err := nilApp.RegisterRouteLoader(func(*App) error { return nil }); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.RegisterRouteLoader 错误: %v", err)
	}
	if nilApp.RegisteredModelTypes() != nil {
		t.Fatal("nil App.RegisteredModelTypes 必须返回 nil")
	}
}

// TestAppURLFacadesAndWithRouteDefaults 验证静态资源、命名路由和显式路由
// 开关都使用 App 配置，不从请求 Host 推断业务域名。
func TestAppURLFacadesAndWithRouteDefaults(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.config.Set("app.server.domain", "https://api.example.com"); err != nil {
		t.Fatalf("设置应用域名失败: %v", err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://untrusted.example.net/", nil))
	if actual := app.AssetURLFor(request, "assets/app.css"); actual != "https://api.example.com/assets/app.css" {
		t.Fatalf("AssetURLFor 使用了错误域名: %q", actual)
	}
	if actual := app.URLFor(request, "https://cdn.example.com/app.css"); actual != "https://cdn.example.com/app.css" {
		t.Fatalf("URLFor 不应改写合法绝对 URL: %q", actual)
	}
	for _, invalid := range []string{"", "bad\npath", "javascript:alert(1)"} {
		if actual := app.URLFor(request, invalid); actual != "" && invalid != "javascript:alert(1)" {
			t.Errorf("非法 URLFor 路径 %q 必须拒绝: %q", invalid, actual)
		}
	}

	app.Route().Get("users/:id", "user/read").Name("user.read")
	generated, err := app.RouteURL(request, "user.read", map[string]interface{}{"id": 7})
	if err != nil || generated != "https://api.example.com/users/7.html" {
		t.Fatalf("RouteURL 结果错误: url=%q err=%v", generated, err)
	}
	if _, err = app.RouteURL(request, "missing", nil); err == nil {
		t.Fatal("不存在的命名路由必须返回错误")
	}
	if !app.WithRoute() {
		t.Fatal("with_route 缺省必须启用")
	}
	if err = app.config.Set("app.with_route", false); err != nil {
		t.Fatalf("设置 with_route 失败: %v", err)
	}
	if app.WithRoute() {
		t.Fatal("app.with_route=false 必须关闭显式路由加载")
	}
	var nilApp *App
	if !nilApp.WithRoute() || nilApp.AssetURLFor(request, "asset") != "" {
		t.Fatal("nil App 应保持 with_route 安全默认值且不能生成 URL")
	}
	if _, err = nilApp.RouteURL(request, "route", nil); !errors.Is(err, ErrApplicationURL) {
		t.Fatalf("nil App.RouteURL 必须返回 ErrApplicationURL: %v", err)
	}
}

// TestTypedFactoryAndManualBootServiceAPI 验证瞬时泛型工厂每次解析都创建
// 新实例，并支持显式启动单个 ThinkPHP 应用服务。
func TestTypedFactoryAndManualBootServiceAPI(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	key, err := NewTypedServiceKey[*typedServiceProbe]("factory.probe")
	if err != nil {
		t.Fatalf("创建瞬时泛型服务键失败: %v", err)
	}
	builds := 0
	if err = BindFactoryTyped(app, key, func(*Container) (*typedServiceProbe, error) {
		builds++
		return &typedServiceProbe{sequence: builds}, nil
	}); err != nil {
		t.Fatalf("绑定瞬时泛型工厂失败: %v", err)
	}
	first, err := ResolveTyped(app, key)
	if err != nil {
		t.Fatalf("首次解析瞬时泛型服务失败: %v", err)
	}
	second, err := ResolveTyped(app, key)
	if err != nil || first == second || first.sequence != 1 || second.sequence != 2 {
		t.Fatalf("瞬时泛型工厂未隔离实例: first=%#v second=%#v err=%v", first, second, err)
	}

	service := &manualBootService{}
	if err = app.BootService(service); err != nil || service.booted != 1 {
		t.Fatalf("BootService 未执行业务 Boot: booted=%d err=%v", service.booted, err)
	}
	var nilApp *App
	if err = nilApp.BootService(service); !errors.Is(err, ErrNilApplication) {
		t.Fatalf("nil App.BootService 错误: %v", err)
	}
}

type manualBootService struct {
	Service
	booted int
}

func (*manualBootService) Register() {}

func (service *manualBootService) Boot() {
	service.booted++
}
