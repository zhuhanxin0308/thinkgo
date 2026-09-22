package context

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDownloadSafeRejectsTraversal 验证 DownloadSafe 拒绝越出根目录的路径。
func TestDownloadSafeRejectsTraversal(t *testing.T) {
	baseDir := t.TempDir()

	for _, p := range []string{"../secret.txt", "..\\secret.txt", "sub/../../secret.txt"} {
		resp := NewResponse().DownloadSafe(baseDir, p, "")
		if resp.GetStatus() != http.StatusForbidden {
			t.Fatalf("穿越路径 %q 应返回 403，实际 %d", p, resp.GetStatus())
		}
		if resp.filePath != "" {
			t.Fatalf("穿越路径 %q 不应设置下载文件路径", p)
		}
	}
}

// TestDownloadSafeAllowsWithinBase 验证 DownloadSafe 允许根目录内的文件并解析到正确路径。
func TestDownloadSafeAllowsWithinBase(t *testing.T) {
	baseDir := t.TempDir()
	subDir := filepath.Join(baseDir, "files")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("创建子目录失败: %v", err)
	}

	resp := NewResponse().DownloadSafe(baseDir, "files/report.pdf", "")
	if resp.GetStatus() == http.StatusForbidden {
		t.Fatal("根目录内的路径不应被拒绝")
	}

	wantAbs, _ := filepath.Abs(filepath.Join(baseDir, "files", "report.pdf"))
	if resp.filePath != wantAbs {
		t.Fatalf("解析路径不正确，期望 %q，实际 %q", wantAbs, resp.filePath)
	}
	// 文件名为空时应回退为解析后的末段名。
	if cd := resp.Headers().Get("Content-Disposition"); !strings.Contains(cd, `filename="report.pdf"`) {
		t.Fatalf("Content-Disposition 文件名不正确: %q", cd)
	}
}

// TestDownloadSanitizesFilename 验证下载文件名净化能阻断头注入与路径成分。
func TestDownloadSanitizesFilename(t *testing.T) {
	resp := NewResponse().Download("/tmp/data.bin", "evil\"\r\nSet-Cookie: x=1/../../report.pdf")

	cd := resp.Headers().Get("Content-Disposition")
	if strings.ContainsAny(cd, "\r\n") {
		t.Fatalf("Content-Disposition 不应包含换行: %q", cd)
	}
	if strings.Contains(cd, "Set-Cookie") {
		t.Fatalf("净化后不应残留注入内容: %q", cd)
	}
	// 仅保留末段文件名，且去除引号。
	if !strings.Contains(cd, `filename="report.pdf"`) {
		t.Fatalf("净化后的文件名不正确: %q", cd)
	}
}

// TestDownloadEmptyFilenameFallback 验证空/非法文件名回退为默认名。
func TestDownloadEmptyFilenameFallback(t *testing.T) {
	resp := NewResponse().Download("/tmp/data.bin", "../")
	cd := resp.Headers().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="download"`) {
		t.Fatalf("非法文件名应回退为 download，实际: %q", cd)
	}
}

// TestDownloadSafeRejectsSymlinkEscape 验证安全下载会按真实路径拒绝指向根目录外部的符号链接。
func TestDownloadSafeRejectsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	baseDir := filepath.Join(workspace, "downloads")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatalf("创建下载根目录失败: %v", err)
	}
	secretPath := filepath.Join(workspace, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("private-secret"), 0o600); err != nil {
		t.Fatalf("写入根目录外文件失败: %v", err)
	}
	linkPath := filepath.Join(baseDir, "linked.txt")
	if err := os.Symlink(secretPath, linkPath); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}

	recorder := httptest.NewRecorder()
	err := NewResponse().DownloadSafe(baseDir, "linked.txt", "linked.txt").Send(recorder)
	if !errors.Is(err, ErrUnsafeDownloadPath) {
		t.Fatalf("符号链接越界应返回 ErrUnsafeDownloadPath，实际为 %v", err)
	}
	if recorder.Code != http.StatusForbidden || strings.Contains(recorder.Body.String(), "private-secret") {
		t.Fatalf("符号链接越界必须返回 403 且不得泄露文件，status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

// TestDownloadSendValidatesRegularFile 验证文件在写响应头前完成预检，并为正常下载写出准确长度。
func TestDownloadSendValidatesRegularFile(t *testing.T) {
	baseDir := t.TempDir()
	filePath := filepath.Join(baseDir, "report.txt")
	if err := os.WriteFile(filePath, []byte("report-body"), 0o600); err != nil {
		t.Fatalf("写入下载文件失败: %v", err)
	}

	recorder := httptest.NewRecorder()
	if err := NewResponse().DownloadSafe(baseDir, "report.txt", "report.txt").Send(recorder); err != nil {
		t.Fatalf("发送安全下载失败: %v", err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "report-body" || recorder.Header().Get("Content-Length") != "11" {
		t.Fatalf("下载响应错误，status=%d length=%q body=%q", recorder.Code, recorder.Header().Get("Content-Length"), recorder.Body.String())
	}

	directoryRecorder := httptest.NewRecorder()
	err := NewResponse().Download(baseDir, "directory").Send(directoryRecorder)
	if err == nil || directoryRecorder.Code != http.StatusNotFound {
		t.Fatalf("目录不得作为下载文件，status=%d err=%v", directoryRecorder.Code, err)
	}
	if directoryRecorder.Header().Get("Content-Disposition") != "" {
		t.Fatal("文件预检失败时不得残留下载响应头")
	}
}
