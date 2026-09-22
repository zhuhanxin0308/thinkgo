package framework_test

import (
	"io"
	"os"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/version"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// TestModuleReleaseArchive 按 Go 官方归档规则校验实际发布文件，避免本地编译通过但下游无法下载。
func TestModuleReleaseArchive(t *testing.T) {
	content, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	identity := module.Version{
		Path:    modfile.ModulePath(content),
		Version: "v" + version.Number,
	}
	if err := modzip.CreateFromDir(io.Discard, identity, "."); err != nil {
		t.Fatalf("框架无法生成合法 Go 模块归档: %v", err)
	}
}
