package command

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

func TestNativeConfigCacheReplaysOrderedIsolatedOverrideValues(t *testing.T) {
	basePath := t.TempDir()
	writeNativeConfigCacheSources(t, basePath, "source")
	writeNativeConfigCacheFile(t, filepath.Join(basePath, "app", "admin", "config", "app.json"),
		`{"deployment":{"cached":"remove","revive":"cached-value"}}`)
	generator, _ := newNativeConfigCacheProject(t, basePath, "")
	if err := generator.Initialize(); err != nil {
		t.Fatalf("初始化缓存生成应用失败: %v", err)
	}
	optimizeNativeConfigCaches(t, generator)
	if err := generator.Close(); err != nil {
		t.Fatalf("关闭缓存生成应用失败: %v", err)
	}

	project, applications := newNativeConfigCacheProject(t, basePath, "")
	if err := project.Initialize(); err != nil {
		t.Fatalf("初始化宿主应用失败: %v", err)
	}
	if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
		"deployment.revive": "old-leaf",
	}); err != nil {
		t.Fatalf("提交早期叶子覆盖失败: %v", err)
	}
	labels := []string{"kept"}
	parentValue := map[string]interface{}{"region": "first", "labels": labels}
	if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
		"DEPLOYMENT": parentValue,
	}); err != nil {
		t.Fatalf("提交父对象替换失败: %v", err)
	}
	// 调用方随后修改输入不能改写已提交的补丁，包括嵌套切片。
	parentValue["region"] = "caller-mutated"
	labels[0] = "caller-mutated"
	if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
		"deployment.region": "last",
	}); err != nil {
		t.Fatalf("提交后续叶子覆盖失败: %v", err)
	}
	admin := applications["admin"]
	if err := admin.Initialize(); err != nil {
		t.Fatalf("初始化缓存子应用失败: %v", err)
	}
	want := map[string]interface{}{"region": "last", "labels": []string{"kept"}}
	if got := admin.Config().GetMap("app.deployment"); !reflect.DeepEqual(got, want) {
		t.Fatalf("覆盖重放破坏顺序、父对象替换或输入隔离: got=%#v want=%#v", got, want)
	}
}

// 缓存是完整快照，显式宿主补丁在缓存后重放；无缓存保持子源文件的旧合并顺序。
func TestNativeConfigCachePreservesExplicitHostOverridesAndEnvironment(t *testing.T) {
	for _, cached := range []bool{false, true} {
		name := "source"
		if cached {
			name = "cache"
		}
		t.Run(name, func(t *testing.T) {
			basePath := t.TempDir()
			writeNativeConfigCacheSources(t, basePath, "source")
			writeNativeConfigCacheFile(t, filepath.Join(basePath, "app", "admin", "config", "app.json"),
				`{"server":{"host":"127.0.0.9","port":8081},"priority_marker":"child","limit":5,"child_only":"preserved"}`)
			t.Setenv("APP_LIMIT", "7")
			if cached {
				generator, _ := newNativeConfigCacheProject(t, basePath, "")
				if err := generator.Initialize(); err != nil {
					t.Fatalf("初始化缓存生成应用失败: %v", err)
				}
				optimizeNativeConfigCaches(t, generator)
				if err := generator.Close(); err != nil {
					t.Fatalf("关闭缓存生成应用失败: %v", err)
				}
			}

			t.Setenv("APP_LIMIT", "21")
			project, applications := newNativeConfigCacheProject(t, basePath, "")
			if err := project.Initialize(); err != nil {
				t.Fatalf("初始化宿主应用失败: %v", err)
			}
			for _, overrides := range []map[string]interface{}{
				{"server.host": "127.0.0.3", "priority_marker": "cli-first", "limit": 12},
				{"SERVER.HOST": "127.0.0.4", "priority_marker": "cli-last", "limit": 13},
			} {
				if err := project.ApplyProjectApplicationOverrides(overrides); err != nil {
					t.Fatalf("应用显式宿主覆盖失败: %v", err)
				}
			}
			if err := project.ApplyProjectApplicationOverrides(map[string]interface{}{
				"priority_marker": "must-not-publish",
				"zz..invalid":     "invalid",
			}); !errors.Is(err, framework.ErrProjectConfigurationOverrideUnavailable) {
				t.Fatalf("非法覆盖批次应完整拒绝: %v", err)
			}
			if got := project.Config().GetString("app.priority_marker"); got != "cli-last" {
				t.Fatalf("非法批次修改了入口工作副本: %q", got)
			}
			t.Setenv("APP_LIMIT", "99")
			admin := applications["admin"]
			if err := admin.Initialize(); err != nil {
				t.Fatalf("初始化后续子应用失败: %v", err)
			}
			wantHost, wantMarker := "127.0.0.9", "child"
			if cached {
				wantHost, wantMarker = "127.0.0.4", "cli-last"
			}
			if got := admin.Config().GetString("app.server.host"); got != wantHost {
				t.Errorf("同键覆盖优先级错误: got=%q want=%q", got, wantHost)
			}
			if got := admin.Config().GetString("app.priority_marker"); got != wantMarker {
				t.Errorf("连续覆盖顺序或非法批次隔离错误: got=%q want=%q", got, wantMarker)
			}
			if got := admin.Config().GetInt("app.server.port"); got != 8081 {
				t.Errorf("未显式覆盖的子应用端口被根快照覆盖: %d", got)
			}
			if got := admin.Config().GetString("app.child_only"); got != "preserved" {
				t.Errorf("未显式覆盖的子应用叶子丢失: %q", got)
			}
			if got := admin.Config().GetInt("app.limit"); got != 21 {
				t.Errorf("冻结环境未高于缓存和显式覆盖: %d", got)
			}
			projectConfig := admin.ProjectApplicationConfig()
			server := projectConfig["server"].(map[string]interface{})
			if server["host"] != "127.0.0.4" || projectConfig["priority_marker"] != "cli-last" || projectConfig["child_only"] != nil {
				t.Errorf("子应用修改了项目宿主快照: %#v", projectConfig)
			}
		})
	}
}
