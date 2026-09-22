package framework

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/config"
)

type nativeApplicationController struct{}

// TestNativeApplicationsBuildFromOneAppInstance 验证一个 App 入口可以承载多个
// 独立应用；只有 index 时也使用完全相同的装配模型。
func TestNativeApplicationsBuildFromOneAppInstance(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")

	loaded := make(map[string][]string)
	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(current *App) error {
			name := current.CurrentApplicationName()
			loaded[name] = append(loaded[name], "global")
			return nil
		},
		ApplicationDefinition{Name: "admin", Register: nativeApplicationLoader(loaded, "admin")},
		ApplicationDefinition{Name: "index", Register: nativeApplicationLoader(loaded, "index")},
	); err != nil {
		t.Fatalf("注册原生多应用失败: %v", err)
	}

	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建原生多应用失败: %v", err)
	}
	t.Cleanup(func() { _ = project.Close() })
	if names := project.ApplicationNames(); !reflect.DeepEqual(names, []string{"admin", "index"}) {
		t.Fatalf("应用名称未稳定排序: %#v", names)
	}
	if len(applications) != 2 || applications["index"] != project {
		t.Fatalf("默认 index 应复用入口 App: %#v", applications)
	}

	for name, marker := range map[string]string{"index": "website", "admin": "backend"} {
		current := applications[name]
		if current == nil {
			t.Fatalf("应用 %s 未构建", name)
		}
		if err := current.Initialize(); err != nil {
			t.Fatalf("初始化应用 %s 失败: %v", name, err)
		}
		if got := current.GetAppPath(); got != filepath.Join(basePath, "app", name) {
			t.Errorf("应用 %s 路径错误: %q", name, got)
		}
		if got := current.GetRuntimePath(); got != filepath.Join(basePath, "runtime", name) {
			t.Errorf("应用 %s 运行目录错误: %q", name, got)
		}
		if got := current.GetNamespace(); got != "app\\"+name {
			t.Errorf("应用 %s 命名空间错误: %q", name, got)
		}
		if got := current.Config().GetString("app.application_marker"); got != marker {
			t.Errorf("应用 %s 配置未覆盖全局配置: %q", name, got)
		}
		if !reflect.DeepEqual(loaded[name], []string{"global", "application"}) {
			t.Errorf("应用 %s 加载顺序错误: %#v", name, loaded[name])
		}
	}
}

func nativeApplicationLoader(loaded map[string][]string, expectedName string) ApplicationLoader {
	return func(current *App) error {
		if current.CurrentApplicationName() != expectedName {
			return ErrInvalidApplicationDefinition
		}
		loaded[expectedName] = append(loaded[expectedName], "application")
		return current.RegisterController("Index", &nativeApplicationController{})
	}
}

func writeNativeApplicationConfig(t *testing.T, basePath, name, marker string) {
	t.Helper()
	writeNativeApplicationConfigValues(t, basePath, name, map[string]interface{}{
		"application_marker": marker,
	})
}

func writeNativeApplicationConfigValues(t *testing.T, basePath, name string, values map[string]interface{}) {
	t.Helper()
	directory := filepath.Join(basePath, "app", name, "config")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("创建应用 %s 配置目录失败: %v", name, err)
	}
	content, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		t.Fatalf("编码应用 %s 配置失败: %v", name, err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(filepath.Join(directory, "app.json"), content, 0o644); err != nil {
		t.Fatalf("写入应用 %s 配置失败: %v", name, err)
	}
}

func updateProjectApplicationConfig(t *testing.T, basePath string, values map[string]interface{}) {
	t.Helper()
	path := filepath.Join(basePath, "config", "app.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取项目应用配置失败: %v", err)
	}
	configuration := make(map[string]interface{})
	if err := json.Unmarshal(content, &configuration); err != nil {
		t.Fatalf("解析项目应用配置失败: %v", err)
	}
	for name, value := range values {
		configuration[name] = value
	}
	content, err = json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		t.Fatalf("编码项目应用配置失败: %v", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("更新项目应用配置失败: %v", err)
	}
}

// TestNativeApplicationDebugDoesNotLeakFromPrimaryOverlay 验证入口应用先完成
// 初始化后再构建子应用时，入口覆盖层开启的 Debug 不会粘滞到其它应用。
func TestNativeApplicationDebugDoesNotLeakFromPrimaryOverlay(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfigValues(t, basePath, "index", map[string]interface{}{
		"app_debug":          true,
		"application_marker": "website",
	})
	writeNativeApplicationConfigValues(t, basePath, "admin", map[string]interface{}{
		"app_debug":          false,
		"application_marker": "backend",
	})

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册 Debug 隔离测试应用失败: %v", err)
	}
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化入口应用失败: %v", err)
	}
	if !project.IsDebug() {
		t.Fatal("index 覆盖层应开启入口应用 Debug")
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建 Debug 隔离测试应用失败: %v", err)
	}
	if err := applications["admin"].Initialize(); err != nil {
		t.Fatalf("初始化 admin 应用失败: %v", err)
	}
	if applications["admin"].IsDebug() {
		t.Fatal("index 的 Debug 状态不得泄漏到显式关闭 Debug 的 admin")
	}
}

// TestExplicitDebugOverrideStaysApplicationLocal 验证入口 App 的显式 Debug API
// 只作用于入口实例，不会作为构造选项复制到其它业务应用。
func TestExplicitDebugOverrideStaysApplicationLocal(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfigValues(t, basePath, "index", map[string]interface{}{
		"app_debug": false,
	})
	writeNativeApplicationConfigValues(t, basePath, "admin", map[string]interface{}{
		"app_debug": false,
	})

	project := NewConsoleAppUninitialized(basePath).Debug(true)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册显式 Debug 隔离测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建显式 Debug 隔离测试应用失败: %v", err)
	}
	for _, name := range []string{"index", "admin"} {
		if err := applications[name].Initialize(); err != nil {
			t.Fatalf("初始化应用 %s 失败: %v", name, err)
		}
	}
	if !applications["index"].IsDebug() {
		t.Fatal("入口应用应保留显式 Debug(true)")
	}
	if applications["admin"].IsDebug() {
		t.Fatal("入口应用的显式 Debug 覆盖不得复制到 admin")
	}
}

// TestNativeApplicationsShareFrozenProjectConfiguration 验证无论哪个应用先
// 初始化，项目配置文件与进程环境都只读取一次，后续应用只能克隆同一项目基线。
func TestNativeApplicationsShareFrozenProjectConfiguration(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	updateProjectApplicationConfig(t, basePath, map[string]interface{}{
		"project_marker":     "initial-file",
		"environment_marker": "default",
	})
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")
	if err := os.WriteFile(filepath.Join(basePath, ".env"), []byte("PROJECT_RAW_ENVIRONMENT=initial-env-file\n"), 0o600); err != nil {
		t.Fatalf("写入初始项目环境文件失败: %v", err)
	}
	t.Setenv("APP_ENVIRONMENT_MARKER", "initial-environment")

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(current *App) error {
			return current.Config().Set("app.application_local", "admin")
		}},
		ApplicationDefinition{Name: "index", Register: func(current *App) error {
			return current.Config().Set("app.application_local", "index")
		}},
	); err != nil {
		t.Fatalf("注册项目快照测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建项目快照测试应用失败: %v", err)
	}
	if err := applications["admin"].Initialize(); err != nil {
		t.Fatalf("初始化首个 admin 应用失败: %v", err)
	}

	updateProjectApplicationConfig(t, basePath, map[string]interface{}{
		"project_marker": "changed-file",
	})
	if err := os.WriteFile(filepath.Join(basePath, ".env"), []byte("PROJECT_RAW_ENVIRONMENT=changed-env-file\n"), 0o600); err != nil {
		t.Fatalf("更新项目环境文件失败: %v", err)
	}
	t.Setenv("APP_ENVIRONMENT_MARKER", "changed-environment")
	if err := applications["index"].Initialize(); err != nil {
		t.Fatalf("初始化后续 index 应用失败: %v", err)
	}

	for _, name := range []string{"admin", "index"} {
		current := applications[name]
		if got := current.Config().GetString("app.project_marker"); got != "initial-file" {
			t.Errorf("应用 %s 重新读取了变化后的项目文件: %q", name, got)
		}
		if got := current.Config().GetString("app.environment_marker"); got != "initial-environment" {
			t.Errorf("应用 %s 重新读取了变化后的进程环境: %q", name, got)
		}
		if got := current.Env().Get("project_raw_environment"); got != "initial-env-file" {
			t.Errorf("应用 %s 重新读取了变化后的环境文件: %q", name, got)
		}
		if got := current.Config().GetString("app.application_local"); got != name {
			t.Errorf("应用 %s 的本地配置被其它应用污染: %q", name, got)
		}
	}
	if got := applications["admin"].ProjectConfig()["app"].(map[string]interface{})["project_marker"]; got != "initial-file" {
		t.Fatalf("项目快照被后续应用修改: %#v", got)
	}
}

// TestFailedNativeApplicationCannotPolluteSiblingInitialization 验证前一个应用
// 在加载器中修改配置后失败，不会把半成品工作副本带入后续应用或重复构建结果。
func TestFailedNativeApplicationCannotPolluteSiblingInitialization(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	updateProjectApplicationConfig(t, basePath, map[string]interface{}{
		"project_marker": "project-baseline",
	})
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")
	wantErr := errors.New("admin application failed")

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(current *App) error {
			if err := current.Config().Set("app.project_marker", "admin-partial"); err != nil {
				return err
			}
			return wantErr
		}},
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册失败隔离测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建失败隔离测试应用失败: %v", err)
	}
	if err := applications["admin"].Initialize(); !errors.Is(err, wantErr) {
		t.Fatalf("admin 初始化应保留加载器错误: %v", err)
	}
	repeated, err := project.BuildApplications()
	if err != nil || repeated["index"] != applications["index"] {
		t.Fatalf("重复构建应返回稳定的应用实例: applications=%#v err=%v", repeated, err)
	}
	if err := repeated["index"].Initialize(); err != nil {
		t.Fatalf("admin 失败后 index 仍应独立初始化: %v", err)
	}
	if got := repeated["index"].Config().GetString("app.project_marker"); got != "project-baseline" {
		t.Fatalf("admin 半初始化配置污染了 index: %q", got)
	}
}

// TestNativeApplicationsInitializeSharedSnapshotConcurrently 验证根应用和子应用
// 并发初始化时只执行一次项目解析，且各自仍取得独立配置工作副本。
func TestNativeApplicationsInitializeSharedSnapshotConcurrently(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	updateProjectApplicationConfig(t, basePath, map[string]interface{}{
		"project_marker": "concurrent-baseline",
	})
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册并发快照测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建并发快照测试应用失败: %v", err)
	}

	start := make(chan struct{})
	errorsByName := make(map[string]error)
	var errorsMu sync.Mutex
	var wait sync.WaitGroup
	for _, name := range []string{"admin", "index"} {
		name := name
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := applications[name].Initialize()
			errorsMu.Lock()
			errorsByName[name] = err
			errorsMu.Unlock()
		}()
	}
	close(start)
	wait.Wait()
	for _, name := range []string{"admin", "index"} {
		if err := errorsByName[name]; err != nil {
			t.Fatalf("并发初始化应用 %s 失败: %v", name, err)
		}
		if got := applications[name].Config().GetString("app.project_marker"); got != "concurrent-baseline" {
			t.Fatalf("应用 %s 未取得共享项目基线: %q", name, got)
		}
	}
	if err := applications["admin"].Config().Set("app.project_marker", "admin-only"); err != nil {
		t.Fatalf("修改 admin 工作副本失败: %v", err)
	}
	if got := applications["index"].Config().GetString("app.project_marker"); got != "concurrent-baseline" {
		t.Fatalf("admin 工作副本修改污染了 index: %q", got)
	}
}

// TestNativeApplicationEnvironmentOverridesApplicationConfig 验证应用目录新增的
// 配置叶子仍服从环境最高优先级，不能被后加载的 app/<name>/config 反向覆盖。
func TestNativeApplicationEnvironmentOverridesApplicationConfig(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "application-file")
	t.Setenv("APP_APPLICATION_MARKER", "environment")

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册环境优先级测试应用失败: %v", err)
	}
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化环境优先级测试应用失败: %v", err)
	}
	if got := project.Config().GetString("app.application_marker"); got != "environment" {
		t.Fatalf("应用配置覆盖了环境变量: got=%q want=environment", got)
	}
}

// TestNativeApplicationRejectsInvalidEnvironmentForOverlayField 验证应用覆盖层
// 新声明字段的环境类型错误会阻断初始化，而不是静默使用文件值。
func TestNativeApplicationRejectsInvalidEnvironmentForOverlayField(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	applicationConfigPath := filepath.Join(basePath, "app", "index", "config")
	if err := os.MkdirAll(applicationConfigPath, 0o755); err != nil {
		t.Fatalf("创建应用配置目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(applicationConfigPath, "app.json"), []byte(`{"application_limit":5}`), 0o644); err != nil {
		t.Fatalf("写入应用配置失败: %v", err)
	}
	t.Setenv("APP_APPLICATION_LIMIT", "invalid-number")

	project := NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册非法环境测试应用失败: %v", err)
	}
	if err := project.Initialize(); !errors.Is(err, config.ErrConfigEnvironmentType) {
		t.Fatalf("应用覆盖层的非法环境值必须阻断初始化: %v", err)
	}
}

// TestNativeApplicationsIsolateAndOverlayLanguagePacks 验证每个业务应用拥有独立
// 语言服务，并按 ThinkPHP 顺序以应用翻译覆盖根 app/lang 的同名翻译。
func TestNativeApplicationsIsolateAndOverlayLanguagePacks(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")
	writeNativeLanguagePack(t, filepath.Join(basePath, "app", "lang"), map[string]string{
		"shared":      "global",
		"global_only": "global",
	})
	writeNativeLanguagePack(t, filepath.Join(basePath, "app", "index", "lang"), map[string]string{
		"shared":     "index",
		"index_only": "index",
	})
	writeNativeLanguagePack(t, filepath.Join(basePath, "app", "admin", "lang"), map[string]string{
		"shared":     "admin",
		"admin_only": "admin",
	})

	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册语言隔离测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建语言隔离测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = project.Close() })
	for _, name := range []string{"index", "admin"} {
		if err := applications[name].Initialize(); err != nil {
			t.Fatalf("初始化应用 %s 失败: %v", name, err)
		}
		if got := applications[name].Lang().Get("shared", nil, "zh-cn"); got != name {
			t.Errorf("应用 %s 未覆盖全局翻译: %q", name, got)
		}
		if got := applications[name].Lang().Get("global_only", nil, "zh-cn"); got != "global" {
			t.Errorf("应用 %s 未继承全局翻译: %q", name, got)
		}
	}
	if got := applications["index"].Lang().Get("admin_only", nil, "zh-cn"); got != "admin_only" {
		t.Fatalf("index 不得读取 admin 独有翻译: %q", got)
	}
	if got := applications["admin"].Lang().Get("index_only", nil, "zh-cn"); got != "index_only" {
		t.Fatalf("admin 不得读取 index 独有翻译: %q", got)
	}
}

func writeNativeLanguagePack(t *testing.T, directory string, translations map[string]string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("创建语言目录失败: %v", err)
	}
	content, err := json.MarshalIndent(translations, "", "  ")
	if err != nil {
		t.Fatalf("编码语言包失败: %v", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(filepath.Join(directory, "zh-cn.json"), content, 0o644); err != nil {
		t.Fatalf("写入语言包失败: %v", err)
	}
}

// TestRegisterApplicationsRejectsInvalidDefinitions 验证生成代码无法把重复、非法
// 或缺少加载器的应用定义带入运行时。
func TestRegisterApplicationsRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name        string
		definitions []ApplicationDefinition
	}{
		{name: "empty", definitions: nil},
		{name: "invalid name", definitions: []ApplicationDefinition{{Name: "../admin", Register: func(*App) error { return nil }}}},
		{name: "nil loader", definitions: []ApplicationDefinition{{Name: "index"}}},
		{name: "duplicate", definitions: []ApplicationDefinition{
			{Name: "index", Register: func(*App) error { return nil }},
			{Name: "index", Register: func(*App) error { return nil }},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := NewConsoleAppUninitialized(t.TempDir())
			err := application.RegisterApplications(func(*App) error { return nil }, test.definitions...)
			if err == nil {
				t.Fatal("非法应用定义必须返回错误")
			}
			_ = application.Close()
		})
	}
}

// TestNativeApplicationsHonorConfiguredRuntimeRoot 验证开发者显式设置项目运行时
// 根目录后，多应用仍按 ThinkPHP 规则追加各自应用名。
func TestNativeApplicationsHonorConfiguredRuntimeRoot(t *testing.T) {
	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	writeNativeApplicationConfig(t, basePath, "index", "website")
	writeNativeApplicationConfig(t, basePath, "admin", "backend")
	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册运行时路径测试应用失败: %v", err)
	}
	customRuntimeRoot := filepath.Join(basePath, "storage")
	if err := project.SetRuntimePath(customRuntimeRoot); err != nil {
		t.Fatalf("设置项目运行时根目录失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建运行时路径测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = project.Close() })
	for _, name := range []string{"index", "admin"} {
		if err := applications[name].Initialize(); err != nil {
			t.Fatalf("初始化应用 %s 失败: %v", name, err)
		}
		expected := filepath.Join(customRuntimeRoot, name)
		if got := applications[name].GetRuntimePath(); got != expected {
			t.Errorf("应用 %s 运行时目录错误: got=%q want=%q", name, got, expected)
		}
	}
}

// TestConfigureApplicationPathRejectsUnknownAndLateBindings 验证 Http.Path 的
// 底层绑定只能指向已编译应用，并且应用实例构建后不能再改变目录。
func TestConfigureApplicationPathRejectsUnknownAndLateBindings(t *testing.T) {
	basePath := t.TempDir()
	for _, name := range []string{"index", "admin"} {
		if err := os.MkdirAll(filepath.Join(basePath, "app", name), 0o755); err != nil {
			t.Fatalf("创建应用目录失败: %v", err)
		}
	}
	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册路径绑定测试应用失败: %v", err)
	}
	if err := project.ConfigureApplicationPath("missing", filepath.Join(basePath, "missing")); err == nil {
		t.Fatal("未知应用路径绑定必须失败")
	}
	customAdminPath := filepath.Join(basePath, "applications", "admin")
	if err := os.MkdirAll(customAdminPath, 0o755); err != nil {
		t.Fatalf("创建自定义 admin 目录失败: %v", err)
	}
	if err := project.ConfigureApplicationPath("admin", customAdminPath); err != nil {
		t.Fatalf("绑定自定义 admin 目录失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建路径绑定测试应用失败: %v", err)
	}
	if got := applications["admin"].GetRoutePath(); got != filepath.Join(customAdminPath, "route") {
		t.Fatalf("自定义应用路由目录错误: %q", got)
	}
	if err := project.ConfigureApplicationPath("admin", filepath.Join(basePath, "late")); err == nil {
		t.Fatal("应用构建后不得改变路径绑定")
	}
	_ = project.Close()
}

// TestBuildApplicationsFailsClosedForMissingDirectory 验证编译清单与磁盘目录
// 不一致时返回稳定错误，不能静默丢弃缺失应用后继续启动宿主。
func TestBuildApplicationsFailsClosedForMissingDirectory(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "app", "index"), 0o755); err != nil {
		t.Fatalf("创建 index 目录失败: %v", err)
	}
	project := NewConsoleAppUninitialized(basePath)
	if err := project.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
		ApplicationDefinition{Name: "admin", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册缺失目录测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err == nil || applications["admin"] != nil || applications["index"] == nil {
		t.Fatalf("缺失应用目录未按清单失败: applications=%#v err=%v", applications, err)
	}
	again, repeatedErr := project.BuildApplications()
	if repeatedErr == nil || again["admin"] != nil || again["index"] == nil {
		t.Fatalf("重复构建必须返回相同失败状态: applications=%#v err=%v", again, repeatedErr)
	}
	_ = project.Close()
}
