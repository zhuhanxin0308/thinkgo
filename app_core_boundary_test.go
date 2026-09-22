package framework

import (
	"errors"
	"testing"
)

type appCoreBoundaryService struct{ Value int }

// TestAppServiceMutationAndScopeBoundaries 验证应用级绑定、工厂、作用域和删除都遵守同一容器契约。
func TestAppServiceMutationAndScopeBoundaries(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	t.Cleanup(func() { _ = app.Close() })

	singleton := &appCoreBoundaryService{Value: 1}
	app.Bind("boundary.singleton", singleton)
	app.BindFactory("boundary.factory", func() *appCoreBoundaryService { return &appCoreBoundaryService{Value: 2} })
	app.BindScoped("boundary.scoped", func() *appCoreBoundaryService { return &appCoreBoundaryService{Value: 3} })
	app.Instance("boundary.instance", singleton)

	resolved, err := app.Make("boundary.singleton")
	if err != nil || resolved != singleton {
		t.Fatalf("单例绑定解析错误: value=%#v err=%v", resolved, err)
	}
	firstFactory, err := app.Make("boundary.factory")
	if err != nil {
		t.Fatalf("工厂绑定首次解析失败: %v", err)
	}
	secondFactory, err := app.Make("boundary.factory")
	if err != nil || firstFactory == secondFactory {
		t.Fatalf("工厂绑定没有保持每次新建: first=%p second=%p err=%v", firstFactory, secondFactory, err)
	}
	if !app.Has("boundary.instance") {
		t.Fatal("显式实例绑定没有出现在 Has 快照中")
	}

	scope, err := app.NewScope()
	if err != nil {
		t.Fatalf("创建应用作用域失败: %v", err)
	}
	firstScoped, err := scope.Make("boundary.scoped")
	if err != nil {
		t.Fatalf("作用域服务首次解析失败: %v", err)
	}
	secondScoped, err := scope.Make("boundary.scoped")
	if err != nil || firstScoped != secondScoped {
		t.Fatalf("同一作用域没有复用 Scoped 服务: first=%p second=%p err=%v", firstScoped, secondScoped, err)
	}
	if err := scope.Close(); err != nil {
		t.Fatalf("关闭应用作用域失败: %v", err)
	}
	if _, err := scope.Make("boundary.scoped"); !errors.Is(err, ErrContainerScopeClosed) {
		t.Fatalf("关闭作用域后仍允许解析服务: %v", err)
	}

	app.Delete("boundary.singleton")
	if app.Has("boundary.singleton") {
		t.Fatal("删除绑定后 Has 仍返回 true")
	}
}
