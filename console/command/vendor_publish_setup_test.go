package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func TestVendorPublishDoesNotHideDirectoryPreparationFailure(t *testing.T) {
	base := t.TempDir()
	writeArtifactFixture(t, base, "vendor/composer/installed.json", `[{"name":"acme/demo","extra":{"think":{"config":{"sample":"sample.json"}}}}]`)
	writeArtifactFixture(t, base, "vendor/acme/demo/sample.json", `{"fixture":true}`)
	if err := os.Symlink("missing-directory", filepath.Join(base, "config")); err != nil {
		t.Skipf("当前文件系统不能创建测试符号链接: %v", err)
	}
	app := framework.NewAppUninitialized(base)
	defer app.Close()
	output := &bytes.Buffer{}
	command := &VendorPublish{Command: console.Command{App: app}}
	err := command.Execute(console.NewInput(), console.NewOutputWithWriters(output, &bytes.Buffer{}, false))
	if err == nil {
		t.Fatalf("目录准备失败被当作成功跳过: %s", output.String())
	}
	if strings.Contains(output.String(), "Succeed") || strings.Contains(output.String(), "exist!") {
		t.Fatalf("未执行发布却报告文件碰撞或成功: %s", output.String())
	}
}
