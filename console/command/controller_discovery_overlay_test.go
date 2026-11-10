package command

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
)

func TestDiscoveryOverlayCreatesOnlyIsolatedSources(t *testing.T) {
	base := t.TempDir()
	writeGeneratorTestModule(t, base)
	for path, source := range nativeApplicationBuildSources("index") {
		writeDiscoveryFixture(t, base, path, source)
	}
	app := framework.NewAppUninitialized(base)
	t.Cleanup(func() { _ = app.Close() })
	directory := filepath.Join(t.TempDir(), "overlay")
	if err := writeControllerDiscoveryOverlay(app, directory); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(directory, discoveryOverlayFilename))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Replace map[string]string }
	if err := json.Unmarshal(content, &manifest); err != nil || len(manifest.Replace) < 2 {
		t.Fatalf("overlay 清单缺失: %v %v", manifest, err)
	}
	for logical, backing := range manifest.Replace {
		if !filepath.IsAbs(logical) || !filepath.IsAbs(backing) {
			t.Fatal("overlay 必须使用绝对路径")
		}
		if _, err := os.Stat(logical); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("生成操作写回了业务目录: %s %v", logical, err)
		}
		if data, err := os.ReadFile(backing); err != nil || len(data) == 0 {
			t.Fatalf("隔离源文件缺失: %s %v", backing, err)
		}
	}
	if err := writeControllerDiscoveryOverlay(app, directory); !errors.Is(err, os.ErrExist) {
		t.Fatalf("不得覆盖已有 overlay 目录: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(directory, discoveryOverlayFilename))
	if err != nil || string(after) != string(content) {
		t.Fatalf("拒绝重复目录时破坏已有内容: %v", err)
	}
}
