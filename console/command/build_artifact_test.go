package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildArtifactResourcesAndReplacement 验证完整运行资源、平台隔离与重复构建覆盖。
func TestBuildArtifactResourcesAndReplacement(t *testing.T) {
	base := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/demo\n", "config/app.json": `{}`, "config/secret.txt": "omit",
		"app/lang/zh-cn.json": `{}`, "app/index/config/site.json": `{}`, "app/index/lang/en.json": `{}`,
		"app/index/controller/index.go": "omit", "app/index/view/index.html": "hello",
		"public/style.css": "body{}", ".env": "secret", "runtime/debug.log": "omit",
		"dist/linux-amd64/stale.txt": "old", "dist/windows-amd64/keep.txt": "keep",
	}
	for path, content := range files {
		writeArtifactFixture(t, base, path, content)
	}
	target, err := parseBuildTarget("linux/amd64", "")
	if err != nil {
		t.Fatal(err)
	}
	compiler := func(ctx context.Context, root, output string, target buildTarget) error {
		return os.WriteFile(output, []byte("binary"), 0755)
	}
	if err := buildDistribution(context.Background(), base, target, compiler); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"thinkgo-nocgo", "config/app.json", "app/lang/zh-cn.json", "app/index/config/site.json", "app/index/lang/en.json", "app/index/view/index.html", "public/style.css", "Dockerfile", "docker-compose.yml"} {
		if _, err := os.Stat(filepath.Join(base, "dist/linux-amd64", path)); err != nil {
			t.Errorf("发布包缺少 %s: %v", path, err)
		}
	}
	for _, path := range []string{"stale.txt", ".env", "runtime/debug.log", "app/index/controller/index.go", "config/secret.txt"} {
		if _, err := os.Stat(filepath.Join(base, "dist/linux-amd64", path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("发布包包含非运行资源 %s: %v", path, err)
		}
	}
	if content, err := os.ReadFile(filepath.Join(base, "dist/windows-amd64/keep.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("损坏其他平台: %s %v", content, err)
	}
	content, err := os.ReadFile(filepath.Join(base, "dist/linux-amd64/docker-compose.yml"))
	if err != nil || !strings.Contains(string(content), "linux/amd64") || !strings.Contains(string(content), "dockerfile: Dockerfile") {
		t.Fatalf("容器部署定义错误: %s %v", content, err)
	}
}

// TestBuildArtifactFailureKeepsPrevious 验证失败和取消不会破坏上一次发布包。
func TestBuildArtifactFailureKeepsPrevious(t *testing.T) {
	base := t.TempDir()
	writeArtifactFixture(t, base, "dist/linux-amd64/previous", "previous")
	target, _ := parseBuildTarget("linux/amd64", "")
	want := errors.New("编译失败")
	compiler := func(context.Context, string, string, buildTarget) error { return want }
	if err := buildDistribution(context.Background(), base, target, compiler); !errors.Is(err, want) {
		t.Fatalf("错误未传播: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := buildDistribution(ctx, base, target, compiler); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "dist/linux-amd64/previous")); err != nil {
		t.Fatal(err)
	}
}

// TestBuildTargets 验证平台与 ARM 版本白名单，禁止以参数穿越输出目录。
func TestBuildTargets(t *testing.T) {
	for _, item := range []struct{ target, arm, directory string }{
		{"linux/amd64", "", "linux-amd64"}, {"linux/arm", "", "linux-armv7"},
		{"linux/arm", "5", "linux-armv5"}, {"windows/arm64", "", "windows-arm64"}, {"darwin/amd64", "", "darwin-amd64"},
	} {
		target, err := parseBuildTarget(item.target, item.arm)
		if err != nil || target.directory() != item.directory {
			t.Errorf("目标 %s: %+v %v", item.target, target, err)
		}
	}
	for _, item := range []struct{ target, arm string }{{"../outside", ""}, {"linux/386", ""}, {"linux/arm", "8"}, {"linux/amd64", "7"}, {"linux/arm/7", ""}} {
		if _, err := parseBuildTarget(item.target, item.arm); err == nil {
			t.Errorf("应拒绝 %+v", item)
		}
	}
}

func writeArtifactFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
