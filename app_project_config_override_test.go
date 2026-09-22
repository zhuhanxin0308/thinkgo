package framework

import (
	"errors"
	"testing"
)

// TestApplyProjectApplicationOverridesPublishesOneSharedBaseline 验证受信运行参数
// 同时覆盖入口工作副本、HTTP 项目快照以及尚未初始化的原生子应用基线。
func TestApplyProjectApplicationOverridesPublishesOneSharedBaseline(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")

	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册项目配置覆盖测试应用失败: %v", err)
	}
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化项目配置覆盖测试应用失败: %v", err)
	}
	if err := project.BootProviders(); err != nil {
		t.Fatalf("启动项目配置覆盖测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = project.Close() })

	overrides := map[string]interface{}{
		"public_path": "custom-public",
		"server.host": "127.0.0.1",
		"server.port": 39091,
	}
	if err := project.ApplyProjectApplicationOverrides(overrides); err != nil {
		t.Fatalf("应用项目配置覆盖失败: %v", err)
	}
	if got := project.Config().GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("入口应用工作副本未更新: %q", got)
	}
	projectApplication := project.ProjectApplicationConfig()
	server, ok := projectApplication["server"].(map[string]interface{})
	if !ok || server["host"] != "127.0.0.1" || server["port"] != 39091 {
		t.Fatalf("项目 HTTP 快照未更新: %#v", projectApplication["server"])
	}

	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建覆盖后的原生应用失败: %v", err)
	}
	admin := applications["admin"]
	if admin == nil {
		t.Fatal("缺少 admin 原生应用")
	}
	if err := admin.Initialize(); err != nil {
		t.Fatalf("初始化覆盖后的 admin 应用失败: %v", err)
	}
	if got := admin.Config().GetString("app.server.host"); got != "127.0.0.1" {
		t.Fatalf("后初始化子应用未继承项目覆盖: %q", got)
	}
	if got := admin.Config().GetString("app.public_path"); got != "custom-public" {
		t.Fatalf("后初始化子应用未继承公共目录覆盖: %q", got)
	}
}

// TestApplyProjectApplicationOverridesRejectsInvalidOrRunningMutation 验证非法路径
// 不会发布部分配置，且运行租约建立后项目配置保持冻结。
func TestApplyProjectApplicationOverridesRejectsInvalidOrRunningMutation(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := mustBuildTestApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })

	hostBefore := app.Config().GetString("app.server.host")
	if err := app.ApplyProjectApplicationOverrides(map[string]interface{}{
		"server.host":      "127.0.0.1",
		" app.server.port": "39091",
	}); !errors.Is(err, ErrProjectConfigurationOverrideUnavailable) {
		t.Fatalf("非法相对路径应返回稳定错误: %v", err)
	}
	if got := app.Config().GetString("app.server.host"); got != hostBefore {
		t.Fatalf("非法批次不应发布部分配置: before=%q after=%q", hostBefore, got)
	}

	lease, err := app.AcquireRunLease()
	if err != nil {
		t.Fatalf("获取运行租约失败: %v", err)
	}
	defer lease.Release()
	if err := app.ApplyProjectApplicationOverrides(map[string]interface{}{
		"server.host": "127.0.0.1",
	}); !errors.Is(err, ErrProjectConfigurationOverrideUnavailable) || !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("运行期项目覆盖应被拒绝: %v", err)
	}
}
