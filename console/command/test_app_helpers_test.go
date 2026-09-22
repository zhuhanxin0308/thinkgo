package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
)

// buildConsoleTestApp 创建已完成初始化的控制台应用，供命令测试解析正式服务边界。
func buildConsoleTestApp(t *testing.T, basePath string) *framework.App {
	t.Helper()
	ensureConsoleTestConfigFiles(t, basePath)
	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建控制台测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// ensureConsoleTestConfigFiles 只补齐缺失配置，不覆盖命令测试显式提供的配置。
func ensureConsoleTestConfigFiles(t *testing.T, basePath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
		t.Fatalf("创建命令测试公共目录失败: %v", err)
	}
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建命令测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":     `{"app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`,
		"log.json":     `{"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`,
		"cache.json":   `{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`,
		"view.json":    `{"view_path":"app/view","view_suffix":"html","cache":false}`,
		"cookie.json":  `{}`,
		"session.json": `{"type":"memory","name":"TESTSESSID","expire":600}`,
	}
	for name, content := range configs {
		path := filepath.Join(configPath, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			t.Fatalf("检查命令测试配置 %q 失败: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写入命令测试配置 %q 失败: %v", name, err)
		}
	}
}
