package framework

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type AutomaticModelBase struct {
	*db.Model
}

type nestedAutomaticModel struct {
	*AutomaticModelBase
}

func (*nestedAutomaticModel) ConfigureModel(model *db.Model) error {
	model.Table("configured_users")
	return nil
}

type recursiveSchemaModel struct {
	//lint:ignore U1000 此匿名字段由模型反射遍历，用于验证递归嵌入不会导致无限递归。
	*recursiveSchemaModel
}

type AlternateAutomaticModelBase struct {
	*db.Model
}

type duplicateAutomaticModelBase struct {
	*AutomaticModelBase
	*AlternateAutomaticModelBase
}

type duplicateAutomaticModelColumn struct {
	*db.Model
	First  string `thinkgo:"same"`
	Second string `thinkgo:"same"`
}

type privateAutomaticModelBase struct {
	*db.Model
}

type inaccessibleAutomaticModel struct {
	*privateAutomaticModelBase
}

type ignoredAutomaticModelBase struct {
	*AutomaticModelBase `thinkgo:"-"`
}

type copiedAutomaticModelBase struct {
	db.Model
}

type fixedModelResolver struct {
	value interface{}
	err   error
}

func (resolver fixedModelResolver) Make(string, ...interface{}) (interface{}, error) {
	return resolver.value, resolver.err
}

// TestAutomaticModelNestedConfigurationAndOptions 验证继承模型独立初始化，配置钩子及失败选项都会生效。
func TestAutomaticModelNestedConfigurationAndOptions(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("Nested", &nestedAutomaticModel{}); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterModel("Schema", &recursiveSchemaModel{}); err != nil {
		t.Fatal(err)
	}
	database := db.NewDB(&automaticModelConnection{marker: "nested"})
	t.Cleanup(func() { _ = database.Close() })
	if err := app.Instance(string(ServiceDB), database); err != nil {
		t.Fatal(err)
	}
	if err := app.initializeModelBindings(); err != nil {
		t.Fatal(err)
	}
	first, err := ResolveModel[*nestedAutomaticModel](app)
	if err != nil || first.AutomaticModelBase == nil || first.Model == nil {
		t.Fatalf("嵌套模型没有初始化: model=%v err=%v", first, err)
	}
	second, err := ResolveModel[*nestedAutomaticModel](app)
	if err != nil || second.AutomaticModelBase == first.AutomaticModelBase || second.Model == first.Model {
		t.Fatalf("嵌套模型共享了可变实例: %v", err)
	}
	row, err := first.FindMap()
	if err != nil || row["table"] != "configured_users" {
		t.Fatalf("模型配置钩子未执行: row=%v err=%v", row, err)
	}
	expected := errors.New("业务模型选项失败")
	if _, err := ResolveModel[*nestedAutomaticModel](app, func(*db.Model) error { return expected }); !errors.Is(err, expected) {
		t.Fatalf("构造选项错误必须保留: %v", err)
	}
	if _, err := ResolveModel[*nestedAutomaticModel](app, db.WithModelTransaction(nil)); !errors.Is(err, db.ErrInvalidTransaction) {
		t.Fatalf("无效事务必须传递到模型核心: %v", err)
	}
	if app.HasModelType(reflect.TypeOf((*recursiveSchemaModel)(nil))) || app.Has("model.Schema") {
		t.Fatal("递归普通类型不能成为自动 ORM 模型")
	}
}

// TestAutomaticModelExactTypeValidation 验证泛型边界拒绝不合法类型、错误解析结果与缺少解析器。
func TestAutomaticModelExactTypeValidation(t *testing.T) {
	for _, target := range []reflect.Type{nil, reflect.TypeOf(""), reflect.TypeOf((*string)(nil)), reflect.TypeOf(&struct{}{})} {
		if _, err := ResolveModelType(fixedModelResolver{}, target); !errors.Is(err, db.ErrInvalidModel) {
			t.Errorf("非法模型类型 %v 未被拒绝: %v", target, err)
		}
	}
	for _, result := range []interface{}{nil, (*automaticUserModel)(nil), "wrong", &discoveredAlphaModel{}} {
		if _, err := ResolveModel[*automaticUserModel](fixedModelResolver{value: result}); !errors.Is(err, ErrServiceTypeMismatch) {
			t.Errorf("错误模型结果 %T 未被拒绝: %v", result, err)
		}
	}
	if _, err := ResolveModel[*automaticUserModel](nil); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("nil 模型解析器未被拒绝: %v", err)
	}
	var nilApp *App
	if nilApp.HasModelType(reflect.TypeOf((*automaticUserModel)(nil))) {
		t.Fatal("nil 应用不能提供模型类型")
	}
}

// TestAutomaticModelNativeApplicationIsolation 验证原生多应用装配后的同名模型分别解析各应用数据库。
func TestAutomaticModelNativeApplicationIsolation(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "index")
	writeNativeApplicationConfig(t, basePath, "admin", "admin")
	project := newAppWithOptions(true, basePath)
	t.Cleanup(func() { _ = project.Close() })
	loader := func(current *App) error { return current.RegisterModel("User", &automaticUserModel{}) }
	if err := project.RegisterApplications(func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: loader},
		ApplicationDefinition{Name: "admin", Register: loader},
	); err != nil {
		t.Fatal(err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatal(err)
	}
	for name, app := range applications {
		if err := app.Initialize(); err != nil {
			t.Fatalf("初始化应用 %s 失败: %v", name, err)
		}
		database := db.NewDB(&automaticModelConnection{marker: name})
		t.Cleanup(func() { _ = database.Close() })
		if err := app.Instance(string(ServiceDB), database); err != nil {
			t.Fatal(err)
		}
	}
	for name, app := range applications {
		model, err := ResolveModel[*automaticUserModel](app)
		if err != nil {
			t.Fatal(err)
		}
		row, err := model.FindMap()
		if err != nil || row["marker"] != name {
			t.Fatalf("应用 %s 使用了其它应用模型连接: row=%v err=%v", name, row, err)
		}
	}
}

// TestContainerResolutionContextPreservesCancellationSemantics 验证容器传递取消状态而不代替服务决定中止。
func TestContainerResolutionContextPreservesCancellationSemantics(t *testing.T) {
	container := NewContainer()
	container.BindFactory("inner", func(current *Container) context.Context { return current.Context() })
	container.BindFactory("outer", func(current *Container) (interface{}, error) { return current.Make("inner") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := container.MakeContext(ctx, "outer")
	if err != nil || result != ctx || result.(context.Context).Err() != context.Canceled {
		t.Fatalf("取消状态应完整传递给工厂: result=%v err=%v", result, err)
	}
	if container.Context().Err() != nil {
		t.Fatal("一次解析上下文不能污染根容器")
	}
	var absent *Container
	if absent.Context() == nil {
		t.Fatal("空容器应提供安全后台上下文")
	}
	if _, err := absent.MakeContext(context.Background(), "inner"); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("空容器错误不正确: %v", err)
	}
	var absentScope *ContainerScope
	if _, err := absentScope.MakeContext(context.Background(), "inner"); !errors.Is(err, ErrContainerScopeClosed) {
		t.Fatalf("空作用域错误不正确: %v", err)
	}
	scope := container.NewScope()
	if err := scope.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.MakeContext(context.Background(), "inner"); !errors.Is(err, ErrContainerScopeClosed) {
		t.Fatalf("已关闭作用域不应解析服务: %v", err)
	}
}

// TestAutomaticModelAssemblyValidatesBeforePublishing 验证结构错误在装配时拒绝，且不会留下部分可用模型。
func TestAutomaticModelAssemblyValidatesBeforePublishing(t *testing.T) {
	for name, prototype := range map[string]interface{}{
		"duplicate_base":   &duplicateAutomaticModelBase{},
		"duplicate_column": &duplicateAutomaticModelColumn{},
		"private_base":     &inaccessibleAutomaticModel{},
		"ignored_base":     &ignoredAutomaticModelBase{},
		"copied_base":      &copiedAutomaticModelBase{},
	} {
		t.Run(name, func(t *testing.T) {
			app := NewAppUninitialized(t.TempDir())
			t.Cleanup(func() { _ = app.Close() })
			if err := app.RegisterModel("Valid", &automaticUserModel{}); err != nil {
				t.Fatal(err)
			}
			if err := app.RegisterModel("Invalid", prototype); err != nil {
				t.Fatal(err)
			}
			if err := app.initializeModelBindings(); !errors.Is(err, db.ErrInvalidModel) {
				t.Fatalf("结构无效的模型必须在装配时失败: %v", err)
			}
			if app.Has("model.Valid") || app.Has("model.Invalid") || app.HasModelType(reflect.TypeOf((*automaticUserModel)(nil))) {
				t.Fatal("装配失败后不应留下部分自动模型工厂")
			}
		})
	}
}

// TestContainerResolutionContextAvoidsWrapperAllocations 验证显式传递上下文不增加无依赖工厂的包装分配。
func TestContainerResolutionContextAvoidsWrapperAllocations(t *testing.T) {
	container := NewContainer()
	container.BindFactory("value", func() string { return "value" })
	plain := testing.AllocsPerRun(100, func() {
		if _, err := container.Make("value"); err != nil {
			t.Fatal(err)
		}
	})
	withContext := testing.AllocsPerRun(100, func() {
		if _, err := container.MakeContext(context.Background(), "value"); err != nil {
			t.Fatal(err)
		}
	})
	if withContext > plain {
		t.Fatalf("上下文解析增加了无用包装分配: 普通=%g 上下文=%g", plain, withContext)
	}
}

// TestAutomaticModelTypeLookupUsesApplicationIndex 验证动作依赖识别不在每个请求重新反射和拼接服务键。
func TestAutomaticModelTypeLookupUsesApplicationIndex(t *testing.T) {
	app := newAutomaticModelApp(t, "lookup")
	for _, target := range []reflect.Type{
		reflect.TypeOf((*automaticUserModel)(nil)),
		reflect.TypeOf((*discoveredAlphaModel)(nil)),
		nil,
	} {
		allocations := testing.AllocsPerRun(100, func() { app.HasModelType(target) })
		if allocations != 0 {
			t.Fatalf("模型类型 %v 的热路径识别产生了额外分配: %g", target, allocations)
		}
	}
	key, err := modelTypeServiceKey(reflect.TypeOf((*automaticUserModel)(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Delete(key); err != nil {
		t.Fatal(err)
	}
	if app.HasModelType(reflect.TypeOf((*automaticUserModel)(nil))) {
		t.Fatal("类型索引不能绕过已删除的容器绑定")
	}
}
