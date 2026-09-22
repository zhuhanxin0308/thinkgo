package command

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestVendorPublishUsesThinkPHPComposerMetadata 验证发布命令遵循父框架
// installed.json 中 extra.think.config 的来源与覆盖规则。
func TestVendorPublishUsesThinkPHPComposerMetadata(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	manifestPath := filepath.Join(basePath, "vendor", "composer")
	packagePath := filepath.Join(basePath, "vendor", "acme", "sample", "resources")
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatalf("创建 Composer 元数据目录失败: %v", err)
	}
	if err := os.MkdirAll(packagePath, 0o755); err != nil {
		t.Fatalf("创建供应商包目录失败: %v", err)
	}
	manifest := `{"packages":[{"name":"acme/sample","extra":{"think":{"config":{"sample":"resources/sample.json"}}}}]}`
	if err := os.WriteFile(filepath.Join(manifestPath, "installed.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写入 Composer 元数据失败: %v", err)
	}
	source := filepath.Join(packagePath, "sample.json")
	if err := os.WriteFile(source, []byte(`{"enabled":true}`), 0o644); err != nil {
		t.Fatalf("写入待发布配置失败: %v", err)
	}
	target := filepath.Join(basePath, "config", "sample.json")
	if err := os.WriteFile(target, []byte(`{"enabled":false}`), 0o644); err != nil {
		t.Fatalf("写入既有配置失败: %v", err)
	}

	application := buildConsoleTestApp(t, basePath)
	stdout := &bytes.Buffer{}
	command := &VendorPublish{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("执行 vendor:publish 失败: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取发布目标失败: %v", err)
	}
	if string(content) != `{"enabled":false}` {
		t.Fatalf("未指定 force 时不应覆盖既有配置: %s", content)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("exist")) {
		t.Fatalf("跳过既有文件时缺少 ThinkPHP 风格提示: %q", stdout.String())
	}

	forceInput := console.NewInput("--force")
	command.Configure()
	if err := forceInput.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析 force 选项失败: %v", err)
	}
	if err := command.Execute(forceInput, console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("强制执行 vendor:publish 失败: %v", err)
	}
	content, err = os.ReadFile(target)
	if err != nil {
		t.Fatalf("读取强制发布目标失败: %v", err)
	}
	if string(content) != `{"enabled":true}` {
		t.Fatalf("指定 force 后没有覆盖目标: %s", content)
	}
}

// TestVendorPublishDefinitionMatchesThinkPHP 验证公开命令和 -f/--force 契约。
func TestVendorPublishDefinitionMatchesThinkPHP(t *testing.T) {
	command := &VendorPublish{}
	command.Configure()
	if command.GetSignature() != "vendor:publish" {
		t.Fatalf("命令签名错误: %q", command.GetSignature())
	}
	options := command.GetOptionDefinitions()
	if len(options) != 1 || options[0].Name != "force" || options[0].Short != "f" || !options[0].Bool {
		t.Fatalf("force 选项定义错误: %v", options)
	}
}

// TestVendorPublishRejectsEscapingPackagePaths 验证供应商元数据不能借路径穿越
// 读取包外文件或覆盖 config 目录外的目标。
func TestVendorPublishRejectsEscapingPackagePaths(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	manifestPath := filepath.Join(basePath, "vendor", "composer")
	if err := os.MkdirAll(manifestPath, 0o755); err != nil {
		t.Fatalf("创建 Composer 元数据目录失败: %v", err)
	}
	manifest := `[{"name":"acme/sample","extra":{"think":{"config":{"../escaped":"../../outside.json"}}}}]`
	if err := os.WriteFile(filepath.Join(manifestPath, "installed.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("写入恶意 Composer 元数据失败: %v", err)
	}
	application := buildConsoleTestApp(t, basePath)
	command := &VendorPublish{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err == nil {
		t.Fatal("路径穿越元数据应返回错误")
	}
}
