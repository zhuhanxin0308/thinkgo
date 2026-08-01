package command

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework"
)

func TestApplicationSourceRegistrationIsStableAndIdempotent(t *testing.T) {
	basePath := t.TempDir()
	app := &framework.App{
		BasePath:        basePath,
		ApplicationName: "admin",
		ApplicationPath: filepath.Join(basePath, "app", "admin"),
		RuntimePath:     filepath.Join(basePath, "runtime", "admin"),
	}

	if err := updateApplicationRegistration(app, applicationRegistrationController, "AccountController"); err != nil {
		t.Fatalf("首次更新应用注册源文件失败: %v", err)
	}
	if err := updateApplicationRegistration(app, applicationRegistrationController, "AccountController"); err != nil {
		t.Fatalf("重复更新应用注册源文件失败: %v", err)
	}
	if err := updateApplicationRegistration(app, applicationRegistrationMiddleware, "AuditMiddleware"); err != nil {
		t.Fatalf("更新中间件注册源文件失败: %v", err)
	}

	content := readApplicationSourceForTest(t, app)
	if strings.Count(content, "AccountController") != 2 {
		t.Fatalf("同名控制器不应重复注册: %s", content)
	}
	if !strings.Contains(content, "thinkgo/app/admin/controller") || !strings.Contains(content, "thinkgo/app/admin/middleware") {
		t.Fatalf("注册源文件缺少目标应用导入: %s", content)
	}
	if strings.Index(content, "thinkgo/app/admin/controller") > strings.Index(content, "thinkgo/app/admin/middleware") {
		t.Fatalf("注册源文件导入未按路径稳定排序: %s", content)
	}
	if strings.Count(content, "RegisterController") != 1 || strings.Count(content, "RegisterGlobalMiddleware") != 1 {
		t.Fatalf("注册调用数量错误: %s", content)
	}
}

// TestApplicationSourceEventRegistrationIsStableAndIdempotent 验证事件注册闭包可重复生成且不会重复追加。
func TestApplicationSourceEventRegistrationIsStableAndIdempotent(t *testing.T) {
	basePath := t.TempDir()
	app := &framework.App{
		BasePath:        basePath,
		ApplicationName: "admin",
		ApplicationPath: filepath.Join(basePath, "app", "admin"),
		RuntimePath:     filepath.Join(basePath, "runtime", "admin"),
	}

	for index := 0; index < 2; index++ {
		if err := updateApplicationRegistration(app, applicationRegistrationListener, "AuditListener"); err != nil {
			t.Fatalf("更新监听器注册源文件失败: %v", err)
		}
		if err := updateApplicationRegistration(app, applicationRegistrationSubscriber, "AuditSubscriber"); err != nil {
			t.Fatalf("更新订阅者注册源文件失败: %v", err)
		}
	}

	content := readApplicationSourceForTest(t, app)
	if strings.Count(content, "eventDispatcher.Listen") != 1 {
		t.Fatalf("监听器注册不应重复追加: %s", content)
	}
	if strings.Count(content, "eventDispatcher.Subscribe") != 1 {
		t.Fatalf("订阅者注册不应重复追加: %s", content)
	}
	if !strings.Contains(content, "framework.ResolveServiceAs") || !strings.Contains(content, "framework.ServiceEvent") {
		t.Fatalf("事件注册应通过服务解析边界获取 Dispatcher: %s", content)
	}
}

func TestApplicationSourceRegistrationPreservesMalformedSource(t *testing.T) {
	basePath := t.TempDir()
	appPath := filepath.Join(basePath, "app", "admin")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatalf("创建应用目录失败: %v", err)
	}
	applicationPath := filepath.Join(appPath, "application.go")
	original := []byte("package admin\n\nfunc Register( {\n")
	if err := os.WriteFile(applicationPath, original, 0o600); err != nil {
		t.Fatalf("写入损坏注册源文件失败: %v", err)
	}
	app := &framework.App{BasePath: basePath, ApplicationName: "admin", ApplicationPath: appPath}
	if err := updateApplicationRegistration(app, applicationRegistrationController, "AccountController"); err == nil {
		t.Fatal("损坏的注册源文件必须返回解析错误")
	}
	after, err := os.ReadFile(applicationPath)
	if err != nil {
		t.Fatalf("读取注册源文件失败: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("注册更新失败时不应破坏原文件: %q", after)
	}
}

// TestGeneratedSourceRollsBackWhenRegistrationFails 验证注册入口损坏时不会遗留孤立生成文件。
func TestGeneratedSourceRollsBackWhenRegistrationFails(t *testing.T) {
	basePath := t.TempDir()
	appPath := filepath.Join(basePath, "app", "admin")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatalf("创建应用目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appPath, "application.go"), []byte("package admin\n\nfunc Register( {\n"), 0o600); err != nil {
		t.Fatalf("写入损坏注册源文件失败: %v", err)
	}
	app := &framework.App{BasePath: basePath, ApplicationName: "admin", ApplicationPath: appPath}
	err := writeAndRegisterGeneratedAppSource(app, "controller", "accountcontroller.go", []byte("package controller\n\ntype AccountController struct{}\n"), applicationRegistrationController, "AccountController")
	if err == nil {
		t.Fatal("注册入口损坏时应返回错误")
	}
	if _, statErr := os.Stat(filepath.Join(appPath, "controller", "accountcontroller.go")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("注册失败后不应遗留生成文件: %v", statErr)
	}
}

func TestApplicationSourceRegistrationSerializesConcurrentWriters(t *testing.T) {
	basePath := t.TempDir()
	appPath := filepath.Join(basePath, "app", "admin")
	app := &framework.App{BasePath: basePath, ApplicationName: "admin", ApplicationPath: appPath}
	registrations := []struct {
		kind     applicationRegistrationKind
		typeName string
	}{
		{kind: applicationRegistrationController, typeName: "AccountController"},
		{kind: applicationRegistrationMiddleware, typeName: "AuditMiddleware"},
		{kind: applicationRegistrationProvider, typeName: "AuditService"},
		{kind: applicationRegistrationListener, typeName: "AuditListener"},
		{kind: applicationRegistrationSubscriber, typeName: "AuditSubscriber"},
	}

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, len(registrations))
	for _, registration := range registrations {
		registration := registration
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := updateApplicationRegistration(app, registration.kind, registration.typeName); err != nil {
				errorsChannel <- err
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Errorf("并发更新应用注册源文件失败: %v", err)
	}

	content := readApplicationSourceForTest(t, app)
	for _, registration := range registrations {
		if !strings.Contains(content, registration.typeName) {
			t.Fatalf("并发更新后缺少组件 %q: %s", registration.typeName, content)
		}
	}
}

func readApplicationSourceForTest(t *testing.T, app *framework.App) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(app.ApplicationPath, "application.go"))
	if err != nil {
		t.Fatalf("读取应用注册源文件失败: %v", err)
	}
	return string(content)
}
