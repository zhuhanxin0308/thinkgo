package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

func TestVendorPublishPreservesFileCreatedAfterPrecheck(t *testing.T) {
	base := t.TempDir()
	app := buildConsoleTestApp(t, base)
	source := filepath.Join(base, "vendor", "acme", "demo", "defaults.json")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("vendor"), 0644); err != nil {
		t.Fatal(err)
	}
	original := publishVendorFile
	t.Cleanup(func() { publishVendorFile = original })
	var publishedInfo os.FileInfo
	publishVendorFile = func(app *framework.App, target string, content []byte, force bool) error {
		// 首次不存在检查已经完成，另一个合法写入者在实际发布前抢先创建。
		if err := os.WriteFile(target, []byte("external-editor"), 0644); err != nil {
			return err
		}
		var err error
		publishedInfo, err = os.Stat(target)
		if err != nil {
			return err
		}
		return original(app, target, content, force)
	}
	output := &bytes.Buffer{}
	err := publishPackageConfigFile(app, "acme/demo", "raced", "defaults.json", false,
		console.NewOutputWithWriters(output, &bytes.Buffer{}, false))
	if err != nil && !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	target := filepath.Join(app.GetConfigPath(), "raced"+app.GetConfigExt())
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "external-editor" {
		t.Fatalf("无 force 发布覆盖了外部文件: %q %v", content, err)
	}
	actual, err := os.Stat(target)
	if err != nil || !os.SameFile(publishedInfo, actual) {
		t.Fatal("竞争失败后外部文件身份发生变化")
	}
	leftovers, err := filepath.Glob(target + ".tmp-*")
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("残留发布临时文件: %v %v", leftovers, err)
	}
}

// TestExclusivePublicationChecksAtCommit 验证临时文件完成后，竞争写入仍由原子发布拒绝。
func TestExclusivePublicationChecksAtCommit(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	target := filepath.Join(app.GetConfigPath(), "raced.json")
	err := writeProjectFileWithPublication(app, target, []byte("complete-vendor-output"), 0,
		func(root *os.Root, temporary, relative string) (bool, error) {
			staged, err := root.ReadFile(temporary)
			if err != nil || string(staged) != "complete-vendor-output" {
				t.Errorf("发布前临时文件未完整落盘: %q %v", staged, err)
			}
			if err := root.WriteFile(relative, []byte("winner"), 0644); err != nil {
				return false, err
			}
			return false, root.Link(temporary, relative)
		})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("已有文件必须由提交操作原子拒绝: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "winner" {
		t.Fatalf("先发布者被覆盖: %q %v", content, err)
	}
	leftovers, err := filepath.Glob(target + ".tmp-*")
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("竞争失败残留临时文件: %v %v", leftovers, err)
	}
}
