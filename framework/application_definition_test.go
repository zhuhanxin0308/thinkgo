package framework

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	frameworkcontext "thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

type applicationDefinitionTestController struct{}
type applicationDefinitionOtherController struct{}

func TestApplicationDefinitionRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name       string
		definition ApplicationDefinition
	}{
		{
			name:       "空名称",
			definition: ApplicationDefinition{Path: "app/index", Register: func(*App) error { return nil }},
		},
		{
			name:       "空路径",
			definition: ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
		},
		{
			name:       "路径穿越",
			definition: ApplicationDefinition{Name: "index", Path: "../outside", Register: func(*App) error { return nil }},
		},
		{
			name:       "绝对路径",
			definition: ApplicationDefinition{Name: "index", Path: "C:\\outside", Register: func(*App) error { return nil }},
		},
		{
			name:       "控制字符名称",
			definition: ApplicationDefinition{Name: "index\x00", Path: "app/index", Register: func(*App) error { return nil }},
		},
		{
			name:       "缺少注册回调",
			definition: ApplicationDefinition{Name: "index", Path: "app/index"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := RegisterApplication(test.definition); !errors.Is(err, ErrInvalidApplicationDefinition) {
				t.Fatalf("无效应用定义应返回 ErrInvalidApplicationDefinition，实际为 %v", err)
			}
		})
	}
}

func TestApplicationDefinitionsRejectDuplicateAndReturnSortedCopy(t *testing.T) {
	firstName := "__application_definition_first__"
	secondName := "__application_definition_second__"
	defer unregisterApplicationDefinition(firstName)
	defer unregisterApplicationDefinition(secondName)

	if err := RegisterApplication(ApplicationDefinition{
		Name: firstName,
		Path: "app/__application_definition_first__",
		Register: func(*App) error {
			return nil
		},
	}); err != nil {
		t.Fatalf("注册第一个应用失败: %v", err)
	}
	if err := RegisterApplication(ApplicationDefinition{
		Name: secondName,
		Path: "app/__application_definition_second__",
		Register: func(*App) error {
			return nil
		},
	}); err != nil {
		t.Fatalf("注册第二个应用失败: %v", err)
	}
	if err := RegisterApplication(ApplicationDefinition{
		Name: firstName,
		Path: "app/__application_definition_first__",
		Register: func(*App) error {
			return nil
		},
	}); !errors.Is(err, ErrDuplicateApplication) {
		t.Fatalf("重复应用应返回 ErrDuplicateApplication，实际为 %v", err)
	}

	definitions := ApplicationDefinitions()
	for index := 1; index < len(definitions); index++ {
		if definitions[index-1].Name > definitions[index].Name {
			t.Fatalf("应用快照应按名称排序: %q 排在 %q 后面", definitions[index-1].Name, definitions[index].Name)
		}
	}

	var found bool
	for index := range definitions {
		if definitions[index].Name == firstName {
			found = true
			definitions[index].Name = "mutated"
			break
		}
	}
	if !found {
		t.Fatalf("应用快照中缺少 %q", firstName)
	}
	for _, definition := range ApplicationDefinitions() {
		if definition.Name == "mutated" {
			t.Fatal("应用快照不应暴露全局定义的可变引用")
		}
	}
}

func TestApplicationRegistryIsolatedPerApp(t *testing.T) {
	first := &App{}
	second := &App{}

	if err := first.RegisterController("Shared", &applicationDefinitionTestController{}); err != nil {
		t.Fatalf("第一个应用注册控制器失败: %v", err)
	}
	if err := second.RegisterController("Shared", &applicationDefinitionOtherController{}); err != nil {
		t.Fatalf("第二个应用注册控制器失败: %v", err)
	}

	firstLoader := RouteLoader(func(*App) error { return nil })
	secondLoader := RouteLoader(func(*App) error { return nil })
	if err := first.RegisterRouteLoader(firstLoader); err != nil {
		t.Fatalf("第一个应用注册路由失败: %v", err)
	}
	if err := second.RegisterRouteLoader(secondLoader); err != nil {
		t.Fatalf("第二个应用注册路由失败: %v", err)
	}

	firstHandler := middleware.Handler(func(request *frameworkcontext.Request, next func(*frameworkcontext.Request) *frameworkcontext.Response) *frameworkcontext.Response {
		return next(request)
	})
	secondHandler := middleware.Handler(func(request *frameworkcontext.Request, next func(*frameworkcontext.Request) *frameworkcontext.Response) *frameworkcontext.Response {
		return next(request)
	})
	if err := first.RegisterGlobalMiddleware(firstHandler); err != nil {
		t.Fatalf("第一个应用注册中间件失败: %v", err)
	}
	if err := second.RegisterGlobalMiddleware(secondHandler); err != nil {
		t.Fatalf("第二个应用注册中间件失败: %v", err)
	}

	firstControllers := first.snapshotControllerRegistry()
	secondControllers := second.snapshotControllerRegistry()
	if firstControllers["Shared"] != reflect.TypeOf(applicationDefinitionTestController{}) {
		t.Fatal("第一个应用的控制器注册不属于第一个应用")
	}
	if secondControllers["Shared"] != reflect.TypeOf(applicationDefinitionOtherController{}) {
		t.Fatal("第二个应用的控制器注册不属于第二个应用")
	}
	if len(first.snapshotRouteRegistry()) != 1 || len(second.snapshotRouteRegistry()) != 1 {
		t.Fatal("应用路由注册表不应相互共享")
	}
	if len(first.snapshotMiddlewareRegistry()) != 1 || len(second.snapshotMiddlewareRegistry()) != 1 {
		t.Fatal("应用中间件注册表不应相互共享")
	}
}

func TestApplicationRegistrySupportsConcurrentRegistrationAndSnapshots(t *testing.T) {
	const registrations = 64
	app := &App{}
	waitGroup := sync.WaitGroup{}
	errorsChannel := make(chan error, registrations)

	for index := 0; index < registrations; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			name := fmt.Sprintf("Concurrent%d", index)
			if err := app.RegisterController(name, &applicationDefinitionTestController{}); err != nil {
				errorsChannel <- err
			}
			_ = app.snapshotControllerRegistry()
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)

	for err := range errorsChannel {
		t.Errorf("并发注册应用组件失败: %v", err)
	}
	if len(app.snapshotControllerRegistry()) != registrations {
		t.Fatalf("并发注册后控制器数量错误: got %d, want %d", len(app.snapshotControllerRegistry()), registrations)
	}
}

func TestApplicationRegistryRejectsRegistrationAfterInitialization(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app, err := initializeTestApp(t, basePath)
	if err != nil {
		t.Fatalf("初始化测试应用失败: %v", err)
	}

	if err := app.RegisterController("LateController", &applicationDefinitionTestController{}); !errors.Is(err, ErrApplicationRegistrationClosed) {
		t.Fatalf("初始化后注册控制器应被拒绝，得到: %v", err)
	}
	if err := app.RegisterRouteLoader(func(*App) error { return nil }); !errors.Is(err, ErrApplicationRegistrationClosed) {
		t.Fatalf("初始化后注册路由加载器应被拒绝，得到: %v", err)
	}
	if err := app.RegisterGlobalMiddleware(func(request *frameworkcontext.Request, next func(*frameworkcontext.Request) *frameworkcontext.Response) *frameworkcontext.Response {
		return next(request)
	}); !errors.Is(err, ErrApplicationRegistrationClosed) {
		t.Fatalf("初始化后注册全局中间件应被拒绝，得到: %v", err)
	}
}
