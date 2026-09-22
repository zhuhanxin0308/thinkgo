package command

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestOptimizeConfigBuildsStartupCache 验证配置优化结果会被下一次应用启动优先读取，
// 而不是只生成一个无人消费的占位文件。
func TestOptimizeConfigBuildsStartupCache(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	appConfigPath := filepath.Join(basePath, "config", "app.json")
	if err := os.WriteFile(appConfigPath, []byte(`{"app_name":"cached-name","app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`), 0o644); err != nil {
		t.Fatalf("写入初始应用配置失败: %v", err)
	}

	application := buildConsoleTestApp(t, basePath)
	outputBuffer := &bytes.Buffer{}
	output := console.NewOutputWithWriters(outputBuffer, &bytes.Buffer{}, false)
	command := &OptimizeConfig{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行 optimize:config 失败: %v", err)
	}
	cachePath := filepath.Join(basePath, "runtime", "config.json")
	cacheContent, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("读取配置缓存失败: %v", err)
	}
	var cache map[string]interface{}
	if err := json.Unmarshal(cacheContent, &cache); err != nil {
		t.Fatalf("配置缓存不是合法 JSON: %v", err)
	}
	if !strings.Contains(outputBuffer.String(), "Succeed!") {
		t.Fatalf("配置优化输出缺少成功信息: %q", outputBuffer.String())
	}

	if err := os.WriteFile(appConfigPath, []byte(`{"app_name":"source-changed","app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`), 0o644); err != nil {
		t.Fatalf("修改源应用配置失败: %v", err)
	}
	restarted := buildConsoleTestApp(t, basePath)
	if restarted.Config().GetString("app.app_name") != "cached-name" {
		t.Fatalf("应用重启未优先读取配置缓存: %q", restarted.Config().GetString("app.app_name"))
	}
}

// TestOptimizeRouteBuildsDeterministicManifest 验证路由优化会加载实际路由、
// 校验处理器并生成稳定清单，供部署产物和变更检查复用。
func TestOptimizeRouteBuildsDeterministicManifest(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	application := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = application.Close() })
	if err := application.RegisterRouteLoader(func(current *framework.App) error {
		current.Route().Get("hello/:name", "Index/hello").Name("hello")
		return nil
	}); err != nil {
		t.Fatalf("注册测试路由加载器失败: %v", err)
	}
	if err := application.Initialize(); err != nil {
		t.Fatalf("初始化测试应用失败: %v", err)
	}
	command := &RouteExport{Command: console.Command{App: application}}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行 route:export 失败: %v", err)
	}
	cachePath := filepath.Join(basePath, "runtime", "route.json")
	first, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("读取路由缓存失败: %v", err)
	}
	if !bytes.Contains(first, []byte(`"path": "/hello/:name"`)) || !bytes.Contains(first, []byte(`"handler": "Index/hello"`)) {
		t.Fatalf("路由缓存缺少真实路由信息: %s", first)
	}
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("重复执行 route:export 失败: %v", err)
	}
	second, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatalf("再次读取路由缓存失败: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("相同路由生成的缓存不稳定:\nfirst=%s\nsecond=%s", first, second)
	}
}

// TestOptimizeDefinitionsMatchThinkPHP 验证四个优化命令的公开名称、参数和选项。
func TestOptimizeDefinitionsMatchThinkPHP(t *testing.T) {
	testCases := []struct {
		command     console.ICommand
		signature   string
		arguments   []string
		optionNames []string
	}{
		{command: &Optimize{}, signature: "optimize"},
		{command: &OptimizeConfig{}, signature: "optimize:config", arguments: []string{"dir"}},
		{command: &RouteExport{}, signature: "route:export", arguments: []string{"dir"}},
		{command: &SchemaValidate{}, signature: "schema:validate", arguments: []string{"dir"}, optionNames: []string{"connection", "table"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.signature, func(t *testing.T) {
			testCase.command.Configure()
			if testCase.command.GetSignature() != testCase.signature {
				t.Fatalf("命令签名错误: %q", testCase.command.GetSignature())
			}
			arguments := testCase.command.GetArgumentDefinitions()
			if len(arguments) != len(testCase.arguments) {
				t.Fatalf("位置参数数量错误: %v", arguments)
			}
			for index, name := range testCase.arguments {
				if arguments[index].Name != name || arguments[index].Required {
					t.Fatalf("位置参数定义错误: %v", arguments[index])
				}
			}
			options := testCase.command.GetOptionDefinitions()
			if len(options) != len(testCase.optionNames) {
				t.Fatalf("选项数量错误: %v", options)
			}
			for index, name := range testCase.optionNames {
				if options[index].Name != name {
					t.Fatalf("选项定义错误: %v", options[index])
				}
			}
		})
	}
}
