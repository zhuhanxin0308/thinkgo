package launcher

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

// TestIssue606LauncherSelfHealsWhenConfigCacheIsCorrupted 验证 runtime/config.json 损坏时，
// launcher 执行 clear 与 optimize:config 不会被损坏缓存锁死，能够完成清理与缓存重建。
func TestIssue606LauncherSelfHealsWhenConfigCacheIsCorrupted(t *testing.T) {
	basePath := t.TempDir()
	for _, dir := range []string{
		filepath.Join(basePath, "config"),
		filepath.Join(basePath, "app", "index"),
		filepath.Join(basePath, "runtime", "index"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(basePath, "config", "app.json"), []byte(`{"app_name":"recovered-v2","app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "config", "cache.json"), []byte(`{"default":"file","stores":{"file":{"type":"file","path":"runtime/cache"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "config", "log.json"), []byte(`{"close":true,"default":"file","channels":{"file":{"type":"file","path":"runtime/log"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "config", "cookie.json"), []byte(`{"secret":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	register := func(app *framework.App) error {
		noop := func(*framework.App) error { return nil }
		return app.RegisterApplications(noop, framework.ApplicationDefinition{Name: "index", Register: noop})
	}

	// 写入损坏的根与子应用配置缓存；普通业务命令 config:dump 必须快速失败不静默忽略。
	for _, broken := range []string{
		filepath.Join(basePath, "runtime", "config.json"),
		filepath.Join(basePath, "runtime", "index", "config.json"),
	} {
		if err := os.WriteFile(broken, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(context.Background(), basePath, []string{"config:dump"}, &bytes.Buffer{}, &bytes.Buffer{}, register); err == nil {
		t.Fatal("常规命令遇到损坏配置缓存仍应报错阻断")
	}

	// clear 与 optimize:config 必须跳过损坏缓存完成自愈。
	var clearOut bytes.Buffer
	if err := Run(context.Background(), basePath, []string{"clear"}, &clearOut, &bytes.Buffer{}, register); err != nil {
		t.Fatalf("clear 自愈执行失败: %v", err)
	}
	if !strings.Contains(clearOut.String(), "Clear Successed") {
		t.Fatalf("clear 输出不符: %q", clearOut.String())
	}

	var optOut bytes.Buffer
	if err := Run(context.Background(), basePath, []string{"optimize:config"}, &optOut, &bytes.Buffer{}, register); err != nil {
		t.Fatalf("optimize:config 自愈重建损坏缓存失败: %v", err)
	}
	rebuilt, err := os.ReadFile(filepath.Join(basePath, "runtime", "config.json"))
	if err != nil || !strings.Contains(string(rebuilt), `"recovered-v2"`) {
		t.Fatalf("重建后的配置缓存未包含最新源配置: err=%v content=%s", err, rebuilt)
	}

	// 重建后常规命令恢复正常。
	var dumpOut bytes.Buffer
	if err := Run(context.Background(), basePath, []string{"config:dump"}, &dumpOut, &bytes.Buffer{}, register); err != nil {
		t.Fatalf("缓存重建后 config:dump 应恢复正常: %v", err)
	}
}
