package framework

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	frameworkContext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

type automaticUserModel struct {
	*db.Model
}

type AutomaticConfiguredBase struct {
	*db.Model
	configurationCount int
}

func (base *AutomaticConfiguredBase) ConfigureModel(model *db.Model) error {
	base.configurationCount++
	model.Table("inherited_users")
	return nil
}

type inheritedAutomaticModel struct {
	*AutomaticConfiguredBase
}

type automaticModelContextKey struct{}

type automaticModelConnection struct {
	initTestConnection
	marker string
}

func (connection *automaticModelConnection) Select(ctx context.Context, request db.SelectRequest) ([]map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []map[string]interface{}{{
		"id":      1,
		"marker":  connection.marker,
		"context": ctx.Value(automaticModelContextKey{}),
		"table":   request.Table(),
	}}, nil
}

func newAutomaticModelApp(t *testing.T, marker string) *App {
	t.Helper()
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("User", &automaticUserModel{}); err != nil {
		t.Fatalf("注册模型失败: %v", err)
	}
	database := db.NewDB(&automaticModelConnection{marker: marker})
	t.Cleanup(func() { _ = database.Close() })
	if err := app.Instance(string(ServiceDB), database); err != nil {
		t.Fatalf("绑定数据库失败: %v", err)
	}
	if err := app.initializeModelBindings(); err != nil {
		t.Fatalf("装配模型失败: %v", err)
	}
	return app
}

// TestAutomaticModelResolutionCreatesIndependentInstances 验证模型自动初始化且每次解析保持独立状态。
func TestAutomaticModelResolutionCreatesIndependentInstances(t *testing.T) {
	app := newAutomaticModelApp(t, "first")
	first, err := ResolveModel[*automaticUserModel](app)
	if err != nil || first == nil || first.Model == nil {
		t.Fatalf("模型未自动构造: model=%v err=%v", first, err)
	}
	secondValue, err := app.Make("model.User")
	if err != nil {
		t.Fatalf("按注册名解析模型失败: %v", err)
	}
	second, ok := secondValue.(*automaticUserModel)
	if !ok || second == first || second.Model == first.Model {
		t.Fatal("模型与基础模型必须每次独立创建")
	}
	first.Table("changed_users")
	row, err := second.FindMap()
	if err != nil || row["table"] != "automatic_user_model" || row["marker"] != "first" {
		t.Fatalf("模型查询或实例隔离失败: row=%v err=%v", row, err)
	}
	if !app.HasModelType(reflect.TypeOf(first)) {
		t.Fatal("注册类型应可供控制器动作识别")
	}
}

// TestAutomaticModelRequestContextAndCleanup 验证请求最新上下文传入查询且清理后禁止继续解析。
func TestAutomaticModelRequestContextAndCleanup(t *testing.T) {
	app := newAutomaticModelApp(t, "request")
	scope, err := app.NewScope()
	if err != nil {
		t.Fatal(err)
	}
	request := frameworkContext.MustNewRequest(httptest.NewRequest("GET", "/", nil), frameworkContext.WithServiceScope(scope, scope))
	t.Cleanup(func() { _ = request.Cleanup() })
	updated := context.WithValue(request.Context(), automaticModelContextKey{}, "middleware")
	*request.Raw() = *request.Raw().WithContext(updated)
	model, err := ResolveModel[*automaticUserModel](request)
	if err != nil {
		t.Fatalf("请求模型解析失败: %v", err)
	}
	row, err := model.FindMap()
	if err != nil || row["context"] != "middleware" {
		t.Fatalf("中间件更新后的上下文丢失: row=%v err=%v", row, err)
	}
	activeContext, stop := context.WithCancel(updated)
	*request.Raw() = *request.Raw().WithContext(activeContext)
	cancelable, err := ResolveModel[*automaticUserModel](request)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := cancelable.FindMap(); !errors.Is(err, context.Canceled) {
		t.Fatalf("已经解析的模型必须遵守随后发生的请求取消: %v", err)
	}
	canceled, cancel := context.WithCancel(updated)
	cancel()
	if _, err := scope.MakeContext(canceled, "model.User"); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消请求必须拒绝解析: %v", err)
	}
	if err := request.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveModel[*automaticUserModel](request); !errors.Is(err, frameworkContext.ErrRequestServiceScopeUnavailable) {
		t.Fatalf("已清理请求不得解析模型: %v", err)
	}
}

// TestAutomaticModelApplicationsAndConcurrentRequests 验证同名模型分别使用所属应用数据库且并发请求上下文互不串用。
func TestAutomaticModelApplicationsAndConcurrentRequests(t *testing.T) {
	apps := []*App{newAutomaticModelApp(t, "index"), newAutomaticModelApp(t, "admin")}
	var workers sync.WaitGroup
	for index, app := range apps {
		for worker := 0; worker < 12; worker++ {
			workers.Add(1)
			go func(app *App, applicationIndex, workerIndex int) {
				defer workers.Done()
				ctx := context.WithValue(context.Background(), automaticModelContextKey{}, workerIndex)
				instance, err := app.MakeContext(ctx, "model.User")
				if err != nil {
					t.Errorf("并发解析失败: %v", err)
					return
				}
				row, err := instance.(*automaticUserModel).FindMap()
				if err != nil || row["context"] != workerIndex || row["marker"] != []string{"index", "admin"}[applicationIndex] {
					t.Errorf("应用或请求状态串用: row=%v err=%v", row, err)
				}
			}(app, index, worker)
		}
	}
	workers.Wait()
}

// TestAutomaticModelDatabaseResolutionStaysLazy 验证装配不会触发数据库工厂且模型解析使用当次连接绑定。
func TestAutomaticModelDatabaseResolutionStaysLazy(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("User", &automaticUserModel{}); err != nil {
		t.Fatal(err)
	}
	var connections atomic.Int32
	database := db.NewDB(&automaticModelConnection{marker: "lazy"})
	t.Cleanup(func() { _ = database.Close() })
	if err := app.bindDeferredManagedService(string(ServiceDB), func() (*db.DB, error) {
		connections.Add(1)
		return database, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeModelBindings(); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 0 {
		t.Fatal("模型工厂装配不应提前连接数据库")
	}
	if _, err := ResolveModel[*automaticUserModel](app); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveModel[*automaticUserModel](app); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 1 {
		t.Fatalf("数据库工厂执行次数错误: %d", connections.Load())
	}
	replacement := db.NewDB(&automaticModelConnection{marker: "replacement"})
	t.Cleanup(func() { _ = replacement.Close() })
	if err := app.Instance(string(ServiceDB), replacement); err != nil {
		t.Fatal(err)
	}
	model, err := ResolveModel[*automaticUserModel](app)
	if err != nil {
		t.Fatal(err)
	}
	row, err := model.FindMap()
	if err != nil || row["marker"] != "replacement" {
		t.Fatalf("模型工厂捕获了旧数据库: row=%v err=%v", row, err)
	}
}

// TestAutomaticModelPreservesSchemaAndControllerRegistrations 验证模型名称空间与控制器及普通元数据类型兼容。
func TestAutomaticModelPreservesSchemaAndControllerRegistrations(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("User", &automaticUserModel{}); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterModel("Schema", &discoveredAlphaModel{}); err != nil {
		t.Fatal(err)
	}
	controller := &struct{ Name string }{Name: "controller"}
	if err := app.Instance("User", controller); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeModelBindings(); err != nil {
		t.Fatal(err)
	}
	if app.Get("User") != controller || len(app.RegisteredModelTypes()) != 2 || app.Has("model.Schema") {
		t.Fatal("自动模型绑定破坏控制器或普通 schema 注册")
	}
	if _, err := ResolveModel[*discoveredAlphaModel](app); err == nil {
		t.Fatal("普通元数据类型不能自动解析成 ORM 模型")
	}
	if _, err := ResolveModel[automaticUserModel](app); err == nil {
		t.Fatal("泛型模型必须使用结构体指针")
	}
}

// TestAutomaticModelFailuresRemainVisible 验证缺失数据库、服务捕获和注册冲突不会被默认值掩盖。
func TestAutomaticModelFailuresRemainVisible(t *testing.T) {
	app := newAutomaticModelApp(t, "first")
	if err := app.Bind("singleton.model", func(container *Container) (interface{}, error) {
		return ResolveModel[*automaticUserModel](container)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Make("singleton.model"); !errors.Is(err, ErrScopedDependencyCaptured) {
		t.Fatalf("单例不得捕获操作级模型: %v", err)
	}
	//lint:ignore SA1012 此处专门验证空上下文被拒绝，不能用有效上下文替代错误输入。
	if _, err := app.MakeContext(nil, "model.User"); !errors.Is(err, ErrInvalidContainerResolutionContext) {
		t.Fatalf("nil 上下文必须报错: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveModel[*automaticUserModel](app); !errors.Is(err, ErrApplicationClosed) {
		t.Fatalf("已关闭应用必须拒绝模型解析: %v", err)
	}
	missing := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = missing.Close() })
	if err := missing.RegisterModel("User", &automaticUserModel{}); err != nil {
		t.Fatal(err)
	}
	if err := missing.initializeModelBindings(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveModel[*automaticUserModel](missing); err == nil {
		t.Fatal("数据库缺失必须在模型解析时报告")
	}
	conflict := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = conflict.Close() })
	if err := conflict.RegisterModel("User", &automaticUserModel{}); err != nil {
		t.Fatal(err)
	}
	if err := conflict.Instance("model.User", "existing"); err != nil {
		t.Fatal(err)
	}
	if err := conflict.initializeModelBindings(); !errors.Is(err, ErrDuplicateRegistration) {
		t.Fatalf("模型工厂不得覆盖显式服务绑定: %v", err)
	}
}

// TestAutomaticModelPromotedConfigurationUsesIndependentBase 验证提升的配置方法收到已分配且逐实例独立的嵌入接收器。
func TestAutomaticModelPromotedConfigurationUsesIndependentBase(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("Inherited", &inheritedAutomaticModel{}); err != nil {
		t.Fatal(err)
	}
	database := db.NewDB(&automaticModelConnection{marker: "inherited"})
	t.Cleanup(func() { _ = database.Close() })
	if err := app.Instance(string(ServiceDB), database); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeModelBindings(); err != nil {
		t.Fatal(err)
	}
	first, err := ResolveModel[*inheritedAutomaticModel](app)
	if err != nil {
		t.Fatalf("继承模型配置方法未获得可用接收器: %v", err)
	}
	second, err := ResolveModel[*inheritedAutomaticModel](app)
	if err != nil {
		t.Fatal(err)
	}
	if first.AutomaticConfiguredBase == nil || second.AutomaticConfiguredBase == nil || first.AutomaticConfiguredBase == second.AutomaticConfiguredBase {
		t.Fatal("每个模型应分配独立的配置基类")
	}
	if first.configurationCount != 1 || second.configurationCount != 1 {
		t.Fatalf("每个实例的配置方法应仅执行一次: first=%d second=%d", first.configurationCount, second.configurationCount)
	}
	first.configurationCount++
	if second.configurationCount != 1 {
		t.Fatal("配置基类字段不能跨模型共享")
	}
	row, err := second.FindMap()
	if err != nil || row["table"] != "inherited_users" {
		t.Fatalf("提升的模型配置未进入查询: row=%v err=%v", row, err)
	}
}
