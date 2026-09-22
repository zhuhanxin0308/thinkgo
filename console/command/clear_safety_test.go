package command

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/cache"
	cacheDriver "github.com/zhuhanxin0308/thinkgo/framework/cache/driver"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// TestClearDefaultPreservesRuntimeData 验证默认清理只删除业务缓存，保留运行数据和活动锁。
func TestClearDefaultPreservesRuntimeData(t *testing.T) {
	root := t.TempDir()
	app := buildConsoleTestApp(t, root)
	if err := app.Cache().Set("business", "cached"); err != nil {
		t.Fatal(err)
	}
	lock, err := app.Cache().Lock("running-job", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := lock.Acquire(); err != nil || !ok {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = lock.Release() })
	files := []string{"runtime/log/audit.log", "runtime/session/login", "runtime/cert.pem", "runtime/data.sqlite", "runtime/cache/keep.txt", "runtime/cache/unknown.cache", "runtime/.gitignore"}
	for _, path := range files {
		writeClearTestFile(t, root, path, "必须保留")
	}
	command := &Clear{Command: console.Command{App: app}}
	if err := command.Execute(console.NewInput(), console.NewOutput()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := app.Cache().Get("business"); err != nil || found {
		t.Fatalf("默认后端未清理: %t %v", found, err)
	}
	for _, path := range files {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || string(content) != "必须保留" {
			t.Errorf("非缓存数据被修改: %s %v", path, err)
		}
	}
	if ok, err := lock.Renew(time.Minute); err != nil || !ok {
		t.Fatalf("活动锁被清理: %t %v", ok, err)
	}
}

type failingClearBackend struct {
	*cacheDriver.Memory
	err error
}

func (backend *failingClearBackend) ClearPrefixIfContext(context.Context, string, func(string) bool, func(interface{}) (bool, error)) error {
	return backend.err
}

// TestClearBackendFailureStopsLocalCleanup 验证后端失败时不继续删除本地文件。
func TestClearBackendFailureStopsLocalCleanup(t *testing.T) {
	root := t.TempDir()
	app := buildConsoleTestApp(t, root)
	wantErr := errors.New("测试缓存清理失败")
	backend := &failingClearBackend{Memory: cacheDriver.NewMemory(), err: wantErr}
	if err := app.Instance(string(framework.ServiceCache), cache.NewCache(nil, backend)); err != nil {
		t.Fatal(err)
	}
	writeClearTestFile(t, root, "runtime/cache/local.txt", "保留")
	command := &Clear{Command: console.Command{App: app}}
	if err := command.Execute(console.NewInput(), console.NewOutput()); !errors.Is(err, wantErr) {
		t.Fatalf("后端失败没有向上传播: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime/cache/local.txt")); err != nil {
		t.Fatalf("后端失败后继续删除本地文件: %v", err)
	}
}

// TestClearAllowsCacheBackedSessions 验证共用默认缓存的会话按已确认契约随缓存清理。
func TestClearAllowsCacheBackedSessions(t *testing.T) {
	root := t.TempDir()
	writeClearTestFile(t, root, "config/session.json", `{"type":"cache","name":"CACHESESSID","expire":600}`)
	app := buildConsoleTestApp(t, root)
	recorder := httptest.NewRecorder()
	current, err := app.Session().NewRequestSession(httptest.NewRequest(http.MethodGet, "/", nil), recorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Set("uid", 7); err != nil {
		t.Fatal(err)
	}
	if err := current.Save(); err != nil {
		t.Fatal(err)
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("会话 Cookie 数量错误: %d", len(cookies))
	}
	readSession := func() bool {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(cookies[0])
		loaded, err := app.Session().NewRequestSession(request, httptest.NewRecorder())
		if err != nil {
			t.Fatal(err)
		}
		return loaded.Has("uid")
	}
	if !readSession() {
		t.Fatal("清理前会话没有持久化")
	}
	command := &Clear{Command: console.Command{App: app}}
	if err := command.Execute(console.NewInput(), console.NewOutput()); err != nil {
		t.Fatal(err)
	}
	if readSession() {
		t.Fatal("cache 类型的会话没有随缓存清理")
	}
}
