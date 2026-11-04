package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/config"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestOptimizeNativeConfigCachesAreConsumedAfterRestart 从正式命令生成快照，
// 关闭后重新初始化两个应用，验证消费、环境优先级与项目快照隔离。
func TestOptimizeNativeConfigCachesAreConsumedAfterRestart(t *testing.T) {
	basePath := t.TempDir()
	writeNativeConfigCacheSources(t, basePath, "before")
	t.Setenv("FEATURE_PROCESS_VALUE", "process-before")
	t.Setenv("FEATURE_APPLICATION_VALUE", "application-before")
	writeNativeConfigCacheFile(t, filepath.Join(basePath, ".env"), "FEATURE_FILE_VALUE=file-before\n")

	project, _ := newNativeConfigCacheProject(t, basePath, "")
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化缓存生成应用失败: %v", err)
	}
	optimizeNativeConfigCaches(t, project)
	if err := project.Close(); err != nil {
		t.Fatalf("关闭缓存生成应用失败: %v", err)
	}

	writeNativeConfigCacheSources(t, basePath, "after")
	for _, name := range []string{"index", "admin"} {
		// 有效缓存必须跳过该应用源目录，包括后来损坏的源文件。
		writeNativeConfigCacheFile(t, filepath.Join(basePath, "app", name, "config", "broken.json"), "{")
	}
	t.Setenv("FEATURE_PROCESS_VALUE", "process-restarted")
	t.Setenv("FEATURE_APPLICATION_VALUE", "application-restarted")
	writeNativeConfigCacheFile(t, filepath.Join(basePath, ".env"), "FEATURE_FILE_VALUE=file-restarted\n")

	restarted, applications := newNativeConfigCacheProject(t, basePath, "")
	if err := applications["admin"].Initialize(); err != nil {
		t.Fatalf("优先初始化子应用失败: %v", err)
	}
	// 第一个应用冻结整个项目的环境，后续应用不能重新读取外部环境。
	t.Setenv("FEATURE_PROCESS_VALUE", "process-too-late")
	t.Setenv("FEATURE_APPLICATION_VALUE", "application-too-late")
	writeNativeConfigCacheFile(t, filepath.Join(basePath, ".env"), "FEATURE_FILE_VALUE=file-too-late\n")
	if err := applications["index"].Initialize(); err != nil {
		t.Fatalf("初始化入口应用失败: %v", err)
	}
	assertNativeConfigCacheValues(t, applications, "before", "restarted")
	if err := applications["admin"].Config().Set("feature.marker", "admin-mutated"); err != nil {
		t.Fatalf("修改应用工作副本失败: %v", err)
	}
	if got := applications["index"].Config().GetString("feature.marker"); got != "index-before" {
		t.Fatalf("admin 工作副本污染了 index: %q", got)
	}
	if err := restarted.Close(); err != nil {
		t.Fatalf("关闭重启应用失败: %v", err)
	}

	// 删除优化产物后，新项目重新读取源文件；缺失缓存不是启动错误。
	for _, name := range []string{"", "index", "admin"} {
		if err := os.Remove(filepath.Join(basePath, "runtime", name, "config.json")); err != nil {
			t.Fatalf("删除应用 %q 配置缓存失败: %v", name, err)
		}
		if name != "" {
			if err := os.Remove(filepath.Join(basePath, "app", name, "config", "broken.json")); err != nil {
				t.Fatalf("恢复应用 %q 配置源失败: %v", name, err)
			}
		}
	}
	t.Setenv("FEATURE_PROCESS_VALUE", "process-cleared")
	t.Setenv("FEATURE_APPLICATION_VALUE", "application-cleared")
	writeNativeConfigCacheFile(t, filepath.Join(basePath, ".env"), "FEATURE_FILE_VALUE=file-cleared\n")
	_, uncached := newNativeConfigCacheProject(t, basePath, "")
	for _, name := range []string{"index", "admin"} {
		if err := uncached[name].Initialize(); err != nil {
			t.Fatalf("清理缓存后初始化应用 %s 失败: %v", name, err)
		}
	}
	assertNativeConfigCacheValues(t, uncached, "after", "cleared")
}

func TestNativeConfigCachesUseConfiguredRuntimeBoundary(t *testing.T) {
	basePath := t.TempDir()
	writeNativeConfigCacheSources(t, basePath, "cached")
	project, _ := newNativeConfigCacheProject(t, basePath, "")
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化缓存生成应用失败: %v", err)
	}
	optimizeNativeConfigCaches(t, project)
	if err := project.Close(); err != nil {
		t.Fatalf("关闭缓存生成应用失败: %v", err)
	}

	runtimePath := filepath.Join(basePath, "custom-runtime")
	for _, name := range []string{"", "index", "admin"} {
		defaultPath := filepath.Join(basePath, "runtime", name, "config.json")
		content, err := os.ReadFile(defaultPath)
		if err != nil {
			t.Fatalf("读取应用 %q 配置缓存失败: %v", name, err)
		}
		writeNativeConfigCacheFile(t, filepath.Join(runtimePath, name, "config.json"), string(content))
		// 默认运行目录不属于本次启动，误用它必须使测试失败。
		writeNativeConfigCacheFile(t, defaultPath, "{")
	}
	writeNativeConfigCacheSources(t, basePath, "source")
	_, applications := newNativeConfigCacheProject(t, basePath, runtimePath)
	for _, name := range []string{"admin", "index"} {
		current := applications[name]
		if err := current.Initialize(); err != nil {
			t.Fatalf("从自定义运行目录初始化应用 %s 失败: %v", name, err)
		}
		if got := current.GetRuntimePath(); got != filepath.Join(runtimePath, name) {
			t.Fatalf("应用 %s 使用了错误运行目录: %q", name, got)
		}
		if got := current.Config().GetString("feature.marker"); got != name+"-cached" {
			t.Fatalf("应用 %s 未消费自己的配置缓存: %q", name, got)
		}
		if got := current.ProjectConfig()["feature"].(map[string]interface{})["marker"]; got != "root-cached" {
			t.Fatalf("应用 %s 未保留 owner 的根缓存: %#v", name, got)
		}
	}
}

func TestNativeConfigCacheAndSourceDirectoryMayBothBeAbsent(t *testing.T) {
	basePath := t.TempDir()
	writeNativeConfigCacheSources(t, basePath, "source")
	if err := os.RemoveAll(filepath.Join(basePath, "app", "admin", "config")); err != nil {
		t.Fatalf("删除可选应用配置目录失败: %v", err)
	}
	_, applications := newNativeConfigCacheProject(t, basePath, "")
	if err := applications["admin"].Initialize(); err != nil {
		t.Fatalf("应用缓存与可选配置目录均缺失时应使用项目配置: %v", err)
	}
	if got := applications["admin"].Config().GetString("feature.marker"); got != "root-source" {
		t.Fatalf("缺省应用配置未继承项目基线: %q", got)
	}
}

func TestNativeConfigCacheErrorsDoNotFallBackOrPolluteSiblings(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		wantErr error
	}{
		{
			name: "invalid-json",
			prepare: func(t *testing.T, path string) {
				writeNativeConfigCacheFile(t, path, `{"feature":{"marker":"partial"},`)
			},
		},
		{
			name: "directory",
			prepare: func(t *testing.T, path string) {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatalf("创建目录缓存失败: %v", err)
				}
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, path string) {
				target := filepath.Join(t.TempDir(), "config.json")
				writeNativeConfigCacheFile(t, target, `{"feature":{"marker":"linked"}}`)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("创建运行目录失败: %v", err)
				}
				if err := os.Symlink(target, path); err != nil {
					if runtime.GOOS == "windows" {
						t.Skipf("当前 Windows 环境不能创建符号链接: %v", err)
					}
					t.Fatalf("创建符号链接缓存失败: %v", err)
				}
			},
		},
		{
			name: "writable-file",
			prepare: func(t *testing.T, path string) {
				if runtime.GOOS == "windows" {
					t.Skip("Windows 不使用 POSIX 配置文件权限校验")
				}
				writeNativeConfigCacheFile(t, path, `{"feature":{"marker":"unsafe"}}`)
				if err := os.Chmod(path, 0o602); err != nil {
					t.Fatalf("修改缓存权限失败: %v", err)
				}
			},
			wantErr: config.ErrConfigPermissionsTooOpen,
		},
		{
			name: "runtime-is-file",
			prepare: func(t *testing.T, path string) {
				writeNativeConfigCacheFile(t, filepath.Dir(path), "not a directory")
				if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
					t.Skip("当前文件系统将非目录祖先报告为路径缺失，不能构造独立的缓存检查错误")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			writeNativeConfigCacheSources(t, basePath, "source")
			test.prepare(t, filepath.Join(basePath, "runtime", "admin", "config.json"))
			_, applications := newNativeConfigCacheProject(t, basePath, "")
			admin := applications["admin"]
			err := admin.Initialize()
			if err == nil || !strings.Contains(err.Error(), `加载应用 "admin" 配置缓存失败`) {
				t.Fatalf("非法应用缓存应明确阻断启动而不回源: %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("缓存错误身份丢失: got=%v want=%v", err, test.wantErr)
			}
			if got := admin.Config().GetString("feature.marker"); got != "root-source" {
				t.Fatalf("缓存失败发布了部分数据或回源配置: %q", got)
			}
			if err := applications["index"].Initialize(); err != nil {
				t.Fatalf("admin 缓存失败不应阻止 index 初始化: %v", err)
			}
			if got := applications["index"].Config().GetString("feature.marker"); got != "index-source" {
				t.Fatalf("admin 失败污染了 index 配置: %q", got)
			}
			if got := applications["index"].ProjectConfig()["feature"].(map[string]interface{})["marker"]; got != "root-source" {
				t.Fatalf("admin 失败污染了项目快照: %#v", got)
			}
		})
	}
}

func newNativeConfigCacheProject(t *testing.T, basePath, runtimePath string) (*framework.App, map[string]*framework.App) {
	t.Helper()
	project := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = project.Close() })
	if runtimePath != "" {
		if err := project.SetRuntimePath(runtimePath); err != nil {
			t.Fatalf("设置运行目录失败: %v", err)
		}
	}
	loader := func(*framework.App) error { return nil }
	if err := project.RegisterApplications(loader,
		framework.ApplicationDefinition{Name: "index", Register: loader},
		framework.ApplicationDefinition{Name: "admin", Register: loader},
	); err != nil {
		t.Fatalf("注册配置缓存测试应用失败: %v", err)
	}
	applications, err := project.BuildApplications()
	if err != nil {
		t.Fatalf("构建配置缓存测试应用失败: %v", err)
	}
	return project, applications
}

func optimizeNativeConfigCaches(t *testing.T, project *framework.App) {
	t.Helper()
	command := &OptimizeConfig{Command: console.Command{App: project}}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("生成原生应用配置缓存失败: %v", err)
	}
}

func writeNativeConfigCacheSources(t *testing.T, basePath, suffix string) {
	t.Helper()
	ensureConsoleTestConfigFiles(t, basePath)
	writeNativeConfigCacheFile(t, filepath.Join(basePath, "config", "feature.json"),
		`{"marker":"root-`+suffix+`","process_value":"root-default","file_value":"root-default"}`)
	for _, name := range []string{"index", "admin"} {
		writeNativeConfigCacheFile(t, filepath.Join(basePath, "app", name, "config", "feature.json"),
			`{"marker":"`+name+`-`+suffix+`","application_value":"application-default","`+name+`_only":true}`)
	}
}

func writeNativeConfigCacheFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建配置文件目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入配置文件失败: %v", err)
	}
}

func assertNativeConfigCacheValues(t *testing.T, applications map[string]*framework.App, sourceSuffix, environmentSuffix string) {
	t.Helper()
	for _, name := range []string{"index", "admin"} {
		current := applications[name]
		for key, want := range map[string]string{
			"marker":            name + "-" + sourceSuffix,
			"process_value":     "process-" + environmentSuffix,
			"file_value":        "file-" + environmentSuffix,
			"application_value": "application-" + environmentSuffix,
		} {
			if got := current.Config().GetString("feature." + key); got != want {
				t.Errorf("应用 %s 配置 %s: got=%q want=%q", name, key, got, want)
			}
		}
		other := "admin"
		if name == "admin" {
			other = "index"
		}
		if !current.Config().GetBool("feature."+name+"_only") || current.Config().Has("feature."+other+"_only") {
			t.Errorf("应用 %s 的缓存未保持应用隔离: %#v", name, current.Config().GetMap("feature"))
		}
		projectFeature := current.ProjectConfig()["feature"].(map[string]interface{})
		if projectFeature["marker"] != "root-"+sourceSuffix || projectFeature["application_value"] != nil ||
			projectFeature["index_only"] != nil || projectFeature["admin_only"] != nil {
			t.Errorf("应用 %s 的缓存污染了项目快照: %#v", name, projectFeature)
		}
	}
}
