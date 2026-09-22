package main

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// TestInstalledEntrypointHelp 验证安装入口在没有项目时可以显示 create 帮助。
func TestInstalledEntrypointHelp(t *testing.T) {
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"thinkgo", "create", "--help"}
	t.Chdir(t.TempDir())
	main()
}

// TestInstalledEntrypointRejectsUnsafeCreate 验证发布命令行返回失败状态且不向 stdout 假报成功。
func TestInstalledEntrypointRejectsUnsafeCreate(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if status := run(context.Background(), []string{"create", "../unsafe"}, &stdout, &stderr); status != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("错误退出状态: %d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
}
