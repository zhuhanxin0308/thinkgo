package gotool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPrepareEnvNormalizesGoWorkAndToolchain 验证 GOWORK=off 强制生效与 GOTOOLCHAIN 防降级规则。
func TestPrepareEnvNormalizesGoWorkAndToolchain(t *testing.T) {
	cases := []struct {
		name           string
		env            []string
		runtimeVersion string
		wantToolchain  string
		expectPresent  bool
	}{
		{
			name:           "老版本 +auto 在新工具链下收敛为 auto",
			env:            []string{"GOROOT=/opt/go1.27", "GOTOOLCHAIN=go1.26.5+auto", "GOWORK=/tmp/go.work"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "auto",
			expectPresent:  true,
		},
		{
			name:           "老版本 +path 在新工具链下收敛为 path",
			env:            []string{"GOROOT=/opt/go1.27", "GOTOOLCHAIN=go1.26.5+path"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "path",
			expectPresent:  true,
		},
		{
			name:           "local+auto 收敛为 auto",
			env:            []string{"GOTOOLCHAIN=local+auto"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "auto",
			expectPresent:  true,
		},
		{
			name:           "显式 local 保持不变",
			env:            []string{"GOROOT=/opt/go1.27", "GOTOOLCHAIN=local"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "local",
			expectPresent:  true,
		},
		{
			name:           "更高版本 +auto 保持不变",
			env:            []string{"GOROOT=/opt/go1.27", "GOTOOLCHAIN=go1.28.0+auto"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "go1.28.0+auto",
			expectPresent:  true,
		},
		{
			name:           "显式固定版本不带后缀保持不变",
			env:            []string{"GOROOT=/opt/go1.27", "GOTOOLCHAIN=go1.26.5"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "go1.26.5",
			expectPresent:  true,
		},
		{
			name:           "有 GOROOT 且未设 GOTOOLCHAIN 时补 auto",
			env:            []string{"GOROOT=/opt/go1.27"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "auto",
			expectPresent:  true,
		},
		{
			name:           "无 GOROOT 且未设 GOTOOLCHAIN 时不额外注入",
			env:            []string{"PATH=/usr/bin", "MALFORMED_ENTRY"},
			runtimeVersion: "go1.27.4",
			wantToolchain:  "",
			expectPresent:  false,
		},
		{
			name:           "非标准开发版本保持原样",
			env:            []string{"GOTOOLCHAIN=go1.26.5+auto"},
			runtimeVersion: "devel go1.27-abc",
			wantToolchain:  "go1.26.5+auto",
			expectPresent:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prepareEnvWithRuntime(tc.env, tc.runtimeVersion)
			if gowork, ok := lookupEnv(got, "GOWORK"); !ok || gowork != "off" {
				t.Fatalf("GOWORK 必须为 off: ok=%t got=%q", ok, gowork)
			}
			gotToolchain, exists := lookupEnv(got, "GOTOOLCHAIN")
			if exists != tc.expectPresent || gotToolchain != tc.wantToolchain {
				t.Fatalf("GOTOOLCHAIN 规范化不符: exists=%t got=%q wantExists=%t want=%q", exists, gotToolchain, tc.expectPresent, tc.wantToolchain)
			}
		})
	}
}

// TestCommandContextRunsOfflineWithActiveToolchain 验证 CommandContext 在低版本模块和 GOPROXY=off 下可直接运行。
func TestCommandContextRunsOfflineWithActiveToolchain(t *testing.T) {
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/offline\n\ngo 1.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := CommandContext(context.Background(), "list", "-m")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("离线执行 go list -m 失败: %v\n%s", err, output)
	}
	if got := string(output); got != "example.test/offline\n" {
		t.Fatalf("go list -m 输出不符: %q", got)
	}
	if len(PrepareEnv(nil)) == 0 {
		t.Fatal("PrepareEnv(nil) 不应为空")
	}
}
