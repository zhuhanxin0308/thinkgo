package filesystem

import (
	"errors"
	"io/fs"
	"testing"
)

// TestLocalCopyIdentityValidatesPreconditions 确保幂等复制仍校验资源、源文件与选项。
func TestLocalCopyIdentityValidatesPreconditions(t *testing.T) {
	for _, test := range []struct {
		name        string
		source      string
		destination string
		options     map[string]interface{}
		closed      bool
		wantError   bool
		wantCause   error
	}{
		{name: "已有文件", source: "report.txt", destination: "report.txt"},
		{name: "规范化同路径", source: "./report.txt", destination: "report.txt"},
		{name: "缺失源", source: "missing.txt", destination: "missing.txt", wantError: true, wantCause: fs.ErrNotExist},
		{name: "目录", source: "folder", destination: "folder", wantError: true},
		{name: "关闭磁盘", source: "report.txt", destination: "report.txt", closed: true, wantError: true, wantCause: ErrFilesystemClosed},
		{name: "非法保留选项", source: "report.txt", destination: "report.txt", options: map[string]interface{}{"retain_visibility": "invalid"}, wantError: true, wantCause: ErrInvalidConfiguration},
		{name: "非法可见性", source: "report.txt", destination: "report.txt", options: map[string]interface{}{"visibility": "invalid"}, wantError: true, wantCause: ErrInvalidConfiguration},
	} {
		t.Run(test.name, func(t *testing.T) {
			disk, err := NewLocal(LocalConfig{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = disk.Close() })
			const content = "原始文件内容"
			if err = disk.Write("report.txt", content); err != nil {
				t.Fatal(err)
			}
			if err = disk.CreateDirectory("folder"); err != nil {
				t.Fatal(err)
			}
			if test.closed {
				if err = disk.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err = disk.Copy(test.source, test.destination, test.options)
			if (err != nil) != test.wantError || test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("复制错误不符合预期: err=%v wantError=%v wantCause=%v", err, test.wantError, test.wantCause)
			}
			if !test.closed {
				got, readErr := disk.Read("report.txt")
				if readErr != nil || got != content {
					t.Fatalf("同路径复制不得修改已有内容: got=%q err=%v", got, readErr)
				}
			}
		})
	}
}
