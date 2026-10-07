package filesystem_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/filesystem"
)

func ExampleLocal_Path_serverOnly() {
	root, err := os.MkdirTemp("", "thinkgo-path-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	disk, err := filesystem.NewLocal(filesystem.LocalConfig{Root: root, URL: "/storage"})
	if err != nil {
		panic(err)
	}
	defer disk.Close()
	internalPath, err := disk.Path("reports/a b.txt")
	if err != nil {
		panic(err)
	}
	publicURL, err := disk.URL("reports/a b.txt")
	if err != nil {
		panic(err)
	}
	fmt.Println("server path is absolute:", filepath.IsAbs(internalPath))
	fmt.Println("public reference:", publicURL)
	// URL 只构造引用，不会将这个临时磁盘发布为静态文件服务。
	// Output:
	// server path is absolute: true
	// public reference: /storage/reports/a%20b.txt
}

func TestIssue52UploadKeysAndPublicURLsKeepServerPathInternal(t *testing.T) {
	root := t.TempDir()
	disk, err := filesystem.NewLocal(filesystem.LocalConfig{Root: root, URL: "/storage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := disk.Close(); err != nil {
			t.Error(err)
		}
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "client-name.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("report contents")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&body, writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := form.RemoveAll(); err != nil {
			t.Error(err)
		}
	})
	file := form.File["file"][0]
	for _, name := range []string{"report.txt", "a b#%.txt", "资料.txt"} {
		key, err := disk.PutFileAs("reports", file, name)
		if err != nil {
			t.Fatal(err)
		}
		if key != "reports/"+name || filepath.IsAbs(key) {
			t.Fatalf("upload no longer returns a relative key: %q", key)
		}
		internalPath, err := disk.Path(key)
		if err != nil || internalPath != filepath.Join(root, filepath.FromSlash(key)) || !filepath.IsAbs(internalPath) {
			t.Fatalf("server Path contract changed: %q %v", internalPath, err)
		}
		if contents, err := disk.Read(key); err != nil || contents != "report contents" {
			t.Fatalf("relative key cannot read the stored file: %q %v", contents, err)
		}
		publicURL, err := disk.URL(key)
		if err != nil {
			t.Fatal(err)
		}
		response, err := json.Marshal(struct {
			Key string `json:"key"`
			URL string `json:"url"`
		}{key, publicURL})
		if err != nil || !strings.HasPrefix(publicURL, "/storage/reports/") {
			t.Fatalf("public reference serialization: %q %v", response, err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(response, &decoded); err != nil {
			t.Fatal(err)
		}
		for field, value := range decoded {
			// 解码后检查，避免 Windows 路径在 JSON 中的反斜杠转义掩盖泄漏。
			if strings.Contains(value, root) || strings.Contains(value, internalPath) {
				t.Fatalf("public field %s leaked deployment path: %q", field, value)
			}
		}
	}
	// ? 在 Windows 上不是合法磁盘文件名；在纯 URL 构造路径验证它的编码，不尝试落盘。
	if publicURL, err := disk.URL("reports/a b?#%.txt"); err != nil || publicURL != "/storage/reports/a%20b%3F%23%25.txt" {
		t.Fatalf("URL metacharacters not encoded: %q %v", publicURL, err)
	}
	for _, name := range []string{"../../outside.txt", "bad\x00name"} {
		if key, err := disk.PutFileAs("reports", file, name); key != "" || !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Fatalf("invalid upload key: %q %v", key, err)
		}
	}
}

func TestIssue52PrivateDiskDoesNotInventPublicURL(t *testing.T) {
	disk, err := filesystem.NewLocal(filesystem.LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := disk.Close(); err != nil {
			t.Error(err)
		}
	})
	if publicURL, err := disk.URL("report.txt"); publicURL != "" || !errors.Is(err, filesystem.ErrURLNotSupported) {
		t.Fatalf("private disk invented a public URL: %q %v", publicURL, err)
	}
	if internalPath, err := disk.Path("report.txt"); err != nil || !filepath.IsAbs(internalPath) {
		t.Fatalf("private disk lost its server-side Path: %q %v", internalPath, err)
	}
}
