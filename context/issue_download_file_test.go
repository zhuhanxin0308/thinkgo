package context

import (
	stdcontext "context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadEntrypointsKeepRegularFileAndErrorContracts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("report-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, safe := range []bool{false, true} {
		name := "trusted"
		if safe {
			name = "safe"
		}
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				path   string
				status int
				want   error
			}{
				{path: "report.txt", status: http.StatusOK},
				{path: "directory", status: http.StatusNotFound, want: ErrDownloadNotRegular},
				{path: "missing", status: http.StatusNotFound, want: os.ErrNotExist},
			} {
				t.Run(test.path, func(t *testing.T) {
					response := NewResponse()
					if safe {
						response.DownloadSafe(root, test.path, "report.txt")
					} else {
						response.Download(filepath.Join(root, test.path), "report.txt")
					}
					recorder := httptest.NewRecorder()
					err := response.Send(recorder)
					if !errors.Is(err, test.want) || recorder.Code != test.status {
						t.Fatalf("下载结果错误: status=%d err=%v; want=%d %v", recorder.Code, err, test.status, test.want)
					}
					if test.want == nil {
						if recorder.Body.String() != "report-body" || recorder.Header().Get("Content-Length") != "11" {
							t.Fatalf("普通文件下载不完整: body=%q header=%v", recorder.Body.String(), recorder.Header())
						}
					} else if recorder.Header().Get("Content-Disposition") != "" {
						t.Fatalf("拒绝文件后残留下载响应头: %v", recorder.Header())
					}
				})
			}
		})
	}
}

// 写入器返回取消错误时，也必须收尾已打开的文件；这不把 Open 变成可取消 I/O。
func TestOpenedDownloadClosesOnCompletionAndWriteCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "canceled", err: stdcontext.Canceled},
		{name: "write-error", err: errors.New("download write failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.txt")
			if err := os.WriteFile(path, []byte("report-body"), 0o600); err != nil {
				t.Fatal(err)
			}
			response := NewResponse().Download(path, "report.txt")
			file, info, err := response.openDownloadFile()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			recorder := httptest.NewRecorder()
			var writer http.ResponseWriter = recorder
			if test.err != nil {
				writer = &issue152FailedDownloadWriter{ResponseRecorder: recorder, err: test.err}
			}
			err = response.sendOpenedDownload(writer, file, info)
			if !errors.Is(err, test.err) || IsResponseTransmissionError(err) != (test.err != nil) {
				t.Errorf("发送未保留写入或取消错误: got=%v want=%v", err, test.err)
			}
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Errorf("发送返回后文件句柄未关闭: %v", err)
			}
		})
	}
}

func TestDownloadSafeRechecksLinkAfterResponseConfiguration(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	for _, path := range []string{inside, outside} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}
	response := NewResponse().DownloadSafe(root, "link.txt", "report.txt")
	if err := response.Error(); err != nil {
		t.Fatalf("根内链接应可配置下载: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := response.Send(recorder); !errors.Is(err, ErrUnsafeDownloadPath) {
		t.Fatalf("发送前应重新检查链接根归属: %v", err)
	}
	if recorder.Code != http.StatusForbidden || recorder.Body.String() != http.StatusText(http.StatusForbidden) || recorder.Header().Get("Content-Disposition") != "" {
		t.Fatalf("拒绝响应错误: status=%d body=%q header=%v", recorder.Code, recorder.Body.String(), recorder.Header())
	}
}

type issue152FailedDownloadWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (writer *issue152FailedDownloadWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
