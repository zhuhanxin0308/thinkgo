package framework

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveViewDriverConfigMatchesThinkPHPDefault 验证项目可以直接使用
// ThinkPHP 8 默认 view 配置，并按照相同的目录查找顺序选择模板根目录。
func TestResolveViewDriverConfigMatchesThinkPHPDefault(t *testing.T) {
	basePath := t.TempDir()
	appViewPath := filepath.Join(basePath, "app", "view")
	rootViewPath := filepath.Join(basePath, "view")
	if err := os.MkdirAll(appViewPath, 0o755); err != nil {
		t.Fatalf("创建应用视图目录失败: %v", err)
	}
	if err := os.MkdirAll(rootViewPath, 0o755); err != nil {
		t.Fatalf("创建根视图目录失败: %v", err)
	}

	app := NewAppUninitialized(basePath)
	configuration, err := app.resolveViewDriverConfig(map[string]interface{}{
		"type":          "Think",
		"auto_rule":     float64(1),
		"view_dir_name": "view",
		"view_suffix":   "html",
		"view_depr":     string(os.PathSeparator),
		"tpl_begin":     "{",
		"tpl_end":       "}",
		"taglib_begin":  "{",
		"taglib_end":    "}",
	})
	if err != nil {
		t.Fatalf("ThinkPHP 默认视图配置不应失败: %v", err)
	}
	if got := configuration["view_path"]; got != appViewPath {
		t.Fatalf("应优先使用 app/view，实际为 %#v", got)
	}
	if got := configuration["view_suffix"]; got != "html" {
		t.Fatalf("模板后缀错误: %#v", got)
	}
	if got := configuration["cache"]; got != true {
		t.Fatalf("ThinkPHP 默认应开启模板缓存: %#v", got)
	}
}

// TestResolveViewDriverConfigFallsBackToRootView 验证单应用没有 app/view 时，
// 与 ThinkPHP Think 驱动一致回退到项目根目录的 view。
func TestResolveViewDriverConfigFallsBackToRootView(t *testing.T) {
	basePath := t.TempDir()
	rootViewPath := filepath.Join(basePath, "view")
	if err := os.MkdirAll(rootViewPath, 0o755); err != nil {
		t.Fatalf("创建根视图目录失败: %v", err)
	}

	app := NewAppUninitialized(basePath)
	configuration, err := app.resolveViewDriverConfig(map[string]interface{}{
		"type":          "Think",
		"view_dir_name": "view",
		"view_suffix":   "html",
	})
	if err != nil {
		t.Fatalf("解析视图配置失败: %v", err)
	}
	if got := configuration["view_path"]; got != rootViewPath {
		t.Fatalf("根视图目录回退错误: %#v", got)
	}
}

// TestResolveViewDriverConfigMatchesNativeApplicationLookupOrder 验证原生多应用
// 依次查找 app/<应用>/view、app/view/<应用> 和 view/<应用>。
func TestResolveViewDriverConfigMatchesNativeApplicationLookupOrder(t *testing.T) {
	tests := []struct {
		name     string
		relative string
	}{
		{name: "application view", relative: "app/index/view"},
		{name: "shared app view", relative: "app/view/index"},
		{name: "root view", relative: "view/index"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			application := NewAppUninitialized(basePath)
			if err := application.RegisterApplications(
				func(*App) error { return nil },
				ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
			); err != nil {
				t.Fatalf("注册原生应用失败: %v", err)
			}
			expected := filepath.Join(basePath, filepath.FromSlash(test.relative))
			if err := os.MkdirAll(expected, 0o755); err != nil {
				t.Fatalf("创建视图目录失败: %v", err)
			}
			configuration, err := application.resolveViewDriverConfig(map[string]interface{}{
				"type":          "Think",
				"view_dir_name": "view",
				"view_suffix":   "html",
			})
			if err != nil {
				t.Fatalf("解析原生应用视图配置失败: %v", err)
			}
			if got := configuration["view_path"]; got != expected {
				t.Fatalf("原生应用视图查找错误: got=%#v want=%q", got, expected)
			}
		})
	}
}

// TestResolveViewDriverConfigDoesNotLeakGenericRootView 验证多应用找不到模板
// 目录时仍固定在当前应用目录，不能退回其它应用可能共用的无前缀 view。
func TestResolveViewDriverConfigDoesNotLeakGenericRootView(t *testing.T) {
	basePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(basePath, "view"), 0o755); err != nil {
		t.Fatalf("创建无前缀视图目录失败: %v", err)
	}
	application := NewAppUninitialized(basePath)
	if err := application.RegisterApplications(
		func(*App) error { return nil },
		ApplicationDefinition{Name: "index", Register: func(*App) error { return nil }},
	); err != nil {
		t.Fatalf("注册原生应用失败: %v", err)
	}
	configuration, err := application.resolveViewDriverConfig(map[string]interface{}{
		"type":          "Think",
		"view_dir_name": "view",
		"view_suffix":   "html",
	})
	if err != nil {
		t.Fatalf("解析原生应用视图配置失败: %v", err)
	}
	expected := filepath.Join(basePath, "app", "index", "view")
	if got := configuration["view_path"]; got != expected {
		t.Fatalf("原生应用不得回退到无前缀视图目录: got=%#v want=%q", got, expected)
	}
}

// TestResolveViewDriverConfigRejectsInvalidThinkPHPOptions 验证公开配置不会
// 因类型错误或未知字段静默改变行为。
func TestResolveViewDriverConfigRejectsInvalidThinkPHPOptions(t *testing.T) {
	app := NewAppUninitialized(t.TempDir())
	testCases := []map[string]interface{}{
		{"type": "unknown"},
		{"auto_rule": float64(4)},
		{"view_dir_name": "../view"},
		{"view_suffix": []interface{}{"html"}},
		{"view_depr": ""},
		{"unknown": true},
	}
	for _, configuration := range testCases {
		if resolved, err := app.resolveViewDriverConfig(configuration); err == nil {
			t.Fatalf("非法视图配置应失败: config=%#v resolved=%#v", configuration, resolved)
		}
	}
}
