//go:build unix

package http

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIssue130StaticFIFODoesNotBlockAdmission(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.MkdirAll(public, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(public, "asset")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("asset", filepath.Join(public, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "normal.txt"), []byte("normal"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := newTestHTTPApp(t, root, map[string]interface{}{"enable": false})
	if err := mustHTTPConfig(t, app).Set("app.server.max_outstanding_requests", 1); err != nil {
		t.Fatal(err)
	}
	host := newTestHTTPHandler(t, app)
	for _, path := range []string{"/asset", "/linked"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+path, func(t *testing.T) {
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					recorder := httptest.NewRecorder()
					host.ServeHTTP(recorder, httptest.NewRequest(method, "http://127.0.0.1"+path, nil))
					done <- recorder
				}()
				select {
				case recorder := <-done:
					if recorder.Code != http.StatusNotFound {
						t.Fatalf("FIFO 必须被拒绝: status=%d", recorder.Code)
					}
				case <-time.After(2 * time.Second):
					// 旧实现会等待写端。失败前释放读者，避免遗留阻塞 goroutine。
					release, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
					if err != nil {
						t.Fatalf("FIFO 探测阻塞，释放夹具失败: %v", err)
					}
					defer release.Close()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Fatal("FIFO 探测在释放夹具后仍阻塞")
					}
					t.Fatal("无写者 FIFO 阻塞了静态请求")
				}
				normal := httptest.NewRecorder()
				host.ServeHTTP(normal, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/normal.txt", nil))
				if normal.Code != http.StatusOK || normal.Body.String() != "normal" {
					t.Fatalf("FIFO 请求后容量未释放或普通文件失效: status=%d body=%q", normal.Code, normal.Body.String())
				}
			})
		}
	}
}
