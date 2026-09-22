package filesystem

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestLocalConfigurationMatchesThinkPHP 验证 Local 磁盘配置、权限与链接策略
// 的合法输入和错误边界，防止运行期静默采用错误权限。
func TestLocalConfigurationMatchesThinkPHP(t *testing.T) {
	configuration, err := parseLocalConfig(map[string]interface{}{
		"root":       t.TempDir(),
		"url":        "/storage",
		"visibility": VisibilityPublic,
		"links":      "skip",
		"permissions": map[string]interface{}{
			"file": map[string]interface{}{
				VisibilityPublic:  "0640",
				VisibilityPrivate: int64(0o600),
			},
			"dir": map[string]interface{}{
				VisibilityPublic:  float64(0o750),
				VisibilityPrivate: 0o700,
			},
		},
	})
	if err != nil {
		t.Fatalf("解析完整 Local 配置失败: %v", err)
	}
	if configuration.URL != "/storage" || configuration.Visibility != VisibilityPublic || configuration.linkHandling() != skipLinks {
		t.Fatalf("Local 基础配置错误: %#v", configuration)
	}
	expectedPermissions := Permissions{
		FilePublic:       fs.FileMode(0o640),
		FilePrivate:      fs.FileMode(0o600),
		DirectoryPublic:  fs.FileMode(0o750),
		DirectoryPrivate: fs.FileMode(0o700),
	}
	if configuration.Permissions != expectedPermissions {
		t.Fatalf("Local 权限配置错误: %#v", configuration.Permissions)
	}

	invalidConfigurations := []map[string]interface{}{
		{},
		{"root": 7},
		{"root": t.TempDir(), "url": 7},
		{"root": t.TempDir(), "visibility": 7},
		{"root": t.TempDir(), "links": true},
		{"root": t.TempDir(), "permissions": "0644"},
		{"root": t.TempDir(), "permissions": map[string]interface{}{"file": "0644"}},
		{"root": t.TempDir(), "permissions": map[string]interface{}{"dir": map[string]interface{}{"public": -1}}},
	}
	for index, invalid := range invalidConfigurations {
		if _, err = parseLocalConfig(invalid); !errors.Is(err, ErrInvalidConfiguration) {
			t.Errorf("第 %d 个非法 Local 配置必须返回 ErrInvalidConfiguration: %v", index+1, err)
		}
	}

	invalidModes := []interface{}{int64(-1), -0.5, 0.5, "not-mode", struct{}{}, 0, 0o1000}
	for _, mode := range invalidModes {
		if _, err = parseFileMode(mode); err == nil {
			t.Errorf("非法权限 %#v 不应通过解析", mode)
		}
	}
	if _, err = normalizeLocalConfig(LocalConfig{Root: t.TempDir(), Visibility: "shared"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("非法 visibility 必须返回 ErrInvalidConfiguration: %v", err)
	}
	if (LocalConfig{}).linkHandling() != disallowLinks {
		t.Fatal("Local 默认必须拒绝符号链接")
	}
}

// TestLocalDiskStreamChecksumVisibilityAndLifecycle 验证 ThinkPHP Filesystem
// 常用的流、目录、校验值、可见性、复制移动和关闭生命周期。
func TestLocalDiskStreamChecksumVisibilityAndLifecycle(t *testing.T) {
	rootPath := filepath.Join(t.TempDir(), "storage")
	disk, err := NewLocal(LocalConfig{Root: rootPath, URL: "https://cdn.example.com/storage", Visibility: VisibilityPrivate})
	if err != nil {
		t.Fatalf("创建 Local 磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	if err = disk.CreateDirectory("documents/private", map[string]interface{}{"visibility": VisibilityPrivate}); err != nil {
		t.Fatalf("创建私有目录失败: %v", err)
	}
	if exists, checkErr := disk.Has("documents/private"); checkErr != nil || !exists {
		t.Fatalf("Has 未识别目录: exists=%t err=%v", exists, checkErr)
	}
	if exists, checkErr := disk.Has("documents/missing"); checkErr != nil || exists {
		t.Fatalf("Has 对缺失路径判断错误: exists=%t err=%v", exists, checkErr)
	}

	if err = disk.WriteBytes("documents/private/binary.dat", []byte{0, 1, 2, 3}, map[string]interface{}{"visibility": VisibilityPrivate}); err != nil {
		t.Fatalf("WriteBytes 写入失败: %v", err)
	}
	stream := strings.NewReader("prefix-stream-content")
	if _, err = stream.Seek(int64(len("prefix-")), io.SeekStart); err != nil {
		t.Fatalf("移动测试流失败: %v", err)
	}
	if err = disk.WriteStream("documents/stream.txt", stream, map[string]interface{}{"directory_visibility": VisibilityPublic}); err != nil {
		t.Fatalf("WriteStream 写入失败: %v", err)
	}
	content, err := disk.Read("documents/stream.txt")
	if err != nil || content != "prefix-stream-content" {
		t.Fatalf("可定位流必须从起点写入: content=%q err=%v", content, err)
	}
	opened, err := disk.ReadStream("documents/stream.txt")
	if err != nil {
		t.Fatalf("ReadStream 打开失败: %v", err)
	}
	streamContent, readErr := io.ReadAll(opened)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil || string(streamContent) != content {
		t.Fatalf("ReadStream 内容错误: content=%q read_err=%v close_err=%v", streamContent, readErr, closeErr)
	}
	if err = disk.WriteStream("documents/nil.txt", nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil 写入流必须返回 ErrInvalidConfiguration: %v", err)
	}

	checksums := map[string]string{
		"md5":    digestHex(md5.New(), content),
		"sha1":   digestHex(sha1.New(), content),
		"sha256": digestHex(sha256.New(), content),
		"sha384": digestHex(sha512.New384(), content),
		"sha512": digestHex(sha512.New(), content),
	}
	for algorithm, expected := range checksums {
		options := map[string]interface{}{"checksum_algo": algorithm}
		if algorithm == "md5" {
			options = nil
		}
		actual, checksumErr := disk.Checksum("documents/stream.txt", options)
		if checksumErr != nil || actual != expected {
			t.Errorf("%s 校验值错误: actual=%q expected=%q err=%v", algorithm, actual, expected, checksumErr)
		}
	}
	if _, err = disk.Checksum("documents/stream.txt", map[string]interface{}{"checksum_algo": 7}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("非字符串 checksum_algo 必须拒绝: %v", err)
	}
	if _, err = disk.Checksum("documents/stream.txt", map[string]interface{}{"checksum_algo": "crc32"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("不支持的 checksum_algo 必须拒绝: %v", err)
	}

	if err = disk.Copy("documents/stream.txt", "copies/private.txt", map[string]interface{}{"visibility": VisibilityPrivate}); err != nil {
		t.Fatalf("显式可见性复制失败: %v", err)
	}
	if visibility, visibilityErr := disk.Visibility("copies/private.txt"); visibilityErr != nil || visibility != VisibilityPrivate {
		t.Fatalf("复制目标可见性错误: visibility=%q err=%v", visibility, visibilityErr)
	}
	if err = disk.Copy("documents/stream.txt", "documents/stream.txt"); err != nil {
		t.Fatalf("同路径复制必须幂等成功: %v", err)
	}
	if err = disk.Copy("documents", "copies/directory"); err == nil {
		t.Fatal("目录不能通过 Copy 文件 API 复制")
	}
	if err = disk.Copy("documents/stream.txt", "copies/invalid.txt", map[string]interface{}{"retain_visibility": "yes"}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("非法 retain_visibility 必须拒绝: %v", err)
	}
	if err = disk.Copy("documents/stream.txt", "copies/default.txt", map[string]interface{}{"retain_visibility": false}); err != nil {
		t.Fatalf("关闭可见性保留的复制失败: %v", err)
	}

	if err = disk.SetVisibility("documents", VisibilityPublic); err != nil {
		t.Fatalf("设置目录可见性失败: %v", err)
	}
	if err = disk.Move("documents/private", "moved/private", map[string]interface{}{"visibility": VisibilityPublic}); err != nil {
		t.Fatalf("移动目录失败: %v", err)
	}
	if visibility, visibilityErr := disk.Visibility("moved/private"); visibilityErr != nil || visibility != VisibilityPublic {
		t.Fatalf("移动目录可见性错误: visibility=%q err=%v", visibility, visibilityErr)
	}

	shallow, err := disk.ListContents("", false)
	if err != nil || !containsFilesystemEntry(shallow, "copies") || containsFilesystemEntry(shallow, "copies/private.txt") {
		t.Fatalf("浅层列表结果错误: entries=%#v err=%v", shallow, err)
	}
	if empty, listErr := disk.ListContents("missing", true); listErr != nil || len(empty) != 0 {
		t.Fatalf("缺失目录应返回空列表: entries=%#v err=%v", empty, listErr)
	}
	if listedFile, listErr := disk.ListContents("documents/stream.txt", false); listErr != nil || len(listedFile) != 0 {
		t.Fatalf("文件路径不能被当作目录列出: entries=%#v err=%v", listedFile, listErr)
	}

	if root, pathErr := disk.Path(""); pathErr != nil || root != rootPath {
		t.Fatalf("磁盘根路径错误: root=%q err=%v", root, pathErr)
	}
	if publicURL, urlErr := disk.URL("documents/stream.txt"); urlErr != nil || publicURL != "https://cdn.example.com/storage/documents/stream.txt" {
		t.Fatalf("文件 URL 错误: url=%q err=%v", publicURL, urlErr)
	}
	if err = disk.Delete("missing.txt"); err != nil {
		t.Fatalf("删除不存在文件必须幂等成功: %v", err)
	}
	if err = disk.Delete("documents"); err == nil {
		t.Fatal("Delete 文件 API 不得删除目录")
	}
	if err = disk.DeleteDirectory("documents/stream.txt"); err != nil {
		t.Fatalf("DeleteDirectory 遇到文件应保持幂等: %v", err)
	}
	if err = disk.DeleteDirectory("missing"); err != nil {
		t.Fatalf("删除不存在目录必须幂等成功: %v", err)
	}

	if err = disk.Close(); err != nil {
		t.Fatalf("关闭 Local 磁盘失败: %v", err)
	}
	if err = disk.Close(); err != nil {
		t.Fatalf("重复关闭 Local 磁盘必须幂等: %v", err)
	}
	if _, err = disk.Read("documents/stream.txt"); !errors.Is(err, ErrFilesystemClosed) {
		t.Fatalf("关闭后读取必须返回 ErrFilesystemClosed: %v", err)
	}
}

// TestLocalDiskUploadMethodsMatchThinkPHP 验证 putFile、putFileAs、内置哈希
// 算法和三种 Go 自定义命名回调的完整调用方式。
func TestLocalDiskUploadMethodsMatchThinkPHP(t *testing.T) {
	disk, err := NewLocal(LocalConfig{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("创建上传磁盘失败: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	file := newUploadFileHeader(t, "avatar.txt", []byte("avatar-content"))

	hashedPath, err := disk.PutFile("uploads", file, "sha256", map[string]interface{}{"visibility": VisibilityPublic})
	if err != nil {
		t.Fatalf("按 sha256 保存上传文件失败: %v", err)
	}
	expectedHash := sha256.Sum256([]byte("avatar-content"))
	expectedName := hex.EncodeToString(expectedHash[:])
	expectedPath := "uploads/" + expectedName[:2] + "/" + expectedName[2:] + ".txt"
	if hashedPath != expectedPath {
		t.Fatalf("上传哈希路径错误: actual=%q expected=%q", hashedPath, expectedPath)
	}
	if stored, readErr := disk.Read(hashedPath); readErr != nil || stored != "avatar-content" {
		t.Fatalf("上传内容错误: content=%q err=%v", stored, readErr)
	}

	generatedPath, err := disk.PutFile("uploads", file)
	if err != nil || !strings.HasSuffix(generatedPath, ".txt") || !strings.HasPrefix(generatedPath, "uploads/") {
		t.Fatalf("默认上传命名错误: path=%q err=%v", generatedPath, err)
	}
	if path, putErr := disk.PutFile("uploads", file, HashNameRule(func(*multipart.FileHeader) (string, error) {
		return "typed/rule", nil
	})); putErr != nil || path != "uploads/typed/rule.txt" {
		t.Fatalf("HashNameRule 上传错误: path=%q err=%v", path, putErr)
	}
	if path, putErr := disk.PutFile("uploads", file, func(*multipart.FileHeader) (string, error) {
		return "error_signature/rule", nil
	}); putErr != nil || path != "uploads/error_signature/rule.txt" {
		t.Fatalf("error 回调上传错误: path=%q err=%v", path, putErr)
	}
	if path, putErr := disk.PutFile("uploads", file, func(*multipart.FileHeader) string {
		return "string_signature/rule"
	}); putErr != nil || path != "uploads/string_signature/rule.txt" {
		t.Fatalf("string 回调上传错误: path=%q err=%v", path, putErr)
	}
	if path, putErr := disk.PutFileAs("uploads", file, "fixed.txt"); putErr != nil || path != "uploads/fixed.txt" {
		t.Fatalf("PutFileAs 上传错误: path=%q err=%v", path, putErr)
	}

	invalidCalls := []func() error{
		func() error { _, callErr := disk.PutFile("uploads", nil); return callErr },
		func() error { _, callErr := disk.PutFile("uploads", file, "sha256", nil, nil); return callErr },
		func() error { _, callErr := disk.PutFile("uploads", file, "sha256", "public"); return callErr },
		func() error { _, callErr := disk.PutFile("uploads", file, "unknown"); return callErr },
		func() error { _, callErr := disk.PutFile("uploads", file, 7); return callErr },
		func() error {
			_, callErr := disk.PutFile("uploads", file, func(*multipart.FileHeader) string { return "" })
			return callErr
		},
		func() error {
			_, callErr := disk.PutFile("uploads", file, HashNameRule(func(*multipart.FileHeader) (string, error) { return "", errors.New("rule failed") }))
			return callErr
		},
		func() error { _, callErr := disk.PutFileAs("uploads", nil, "file.txt"); return callErr },
		func() error { _, callErr := disk.PutFileAs("", file, "../outside.txt"); return callErr },
		func() error { _, callErr := disk.PutFileAs("uploads", file, "file.txt", nil, nil); return callErr },
		func() error {
			_, callErr := disk.PutFileAs("uploads", &multipart.FileHeader{Filename: "missing.txt"}, "missing.txt")
			return callErr
		},
	}
	for index, call := range invalidCalls {
		if callErr := call(); callErr == nil {
			t.Errorf("第 %d 个非法上传调用必须返回错误", index+1)
		}
	}
}

// TestFilesystemManagerExtensionForgetAndClose 验证驱动扩展、实例缓存、
// forgetDriver 重建、配置防御性副本和管理器关闭后的失败语义。
func TestFilesystemManagerExtensionForgetAndClose(t *testing.T) {
	configuration := map[string]interface{}{
		"default":  "custom",
		"metadata": []interface{}{map[string]interface{}{"name": "original"}},
		"disks": map[string]interface{}{
			"custom": map[string]interface{}{"type": "memory", "nested": map[string]interface{}{"region": "local"}},
		},
	}
	manager := New(configuration)
	configuration["default"] = "changed"
	var created atomic.Int32
	var closed atomic.Int32
	if err := manager.Extend(" MEMORY ", func(received map[string]interface{}) (Driver, error) {
		created.Add(1)
		if nested, ok := received["nested"].(map[string]interface{}); !ok || nested["region"] != "local" {
			return nil, errors.New("驱动配置副本错误")
		}
		return &countingFilesystemDriver{closed: &closed}, nil
	}); err != nil {
		t.Fatalf("注册自定义驱动失败: %v", err)
	}
	first, err := manager.Disk()
	if err != nil {
		t.Fatalf("创建默认自定义磁盘失败: %v", err)
	}
	second, err := manager.Disk("custom")
	if err != nil || second != first || created.Load() != 1 {
		t.Fatalf("驱动实例缓存错误: first=%p second=%p created=%d err=%v", first, second, created.Load(), err)
	}
	if manager.ForgetDriver() != manager || closed.Load() != 1 {
		t.Fatalf("ForgetDriver 应关闭默认实例并支持链式调用: closed=%d", closed.Load())
	}
	third, err := manager.Disk()
	if err != nil || third == first || created.Load() != 2 {
		t.Fatalf("ForgetDriver 后应重建实例: third=%p created=%d err=%v", third, created.Load(), err)
	}

	configSnapshot := manager.GetConfig().(map[string]interface{})
	configSnapshot["default"] = "mutated"
	metadata := configSnapshot["metadata"].([]interface{})
	metadata[0].(map[string]interface{})["name"] = "mutated"
	if manager.GetDefaultDriver() != "custom" {
		t.Fatalf("外部配置或返回快照不得修改管理器配置: %q", manager.GetDefaultDriver())
	}
	if nested, getErr := manager.GetDiskConfig("custom", "nested.region"); getErr != nil || nested != "local" {
		t.Fatalf("嵌套磁盘配置读取错误: value=%#v err=%v", nested, getErr)
	}
	if value := manager.GetConfig(7, "fallback"); value != "fallback" {
		t.Fatalf("非字符串配置名必须返回默认值: %#v", value)
	}

	if err = manager.Close(); err != nil || closed.Load() != 2 {
		t.Fatalf("关闭管理器失败: closed=%d err=%v", closed.Load(), err)
	}
	if err = manager.Close(); err != nil {
		t.Fatalf("重复关闭管理器必须幂等: %v", err)
	}
	if _, err = manager.Disk(); !errors.Is(err, ErrFilesystemClosed) {
		t.Fatalf("关闭后 Disk 必须返回 ErrFilesystemClosed: %v", err)
	}
	if err = manager.Extend("other", func(map[string]interface{}) (Driver, error) { return &countingFilesystemDriver{}, nil }); !errors.Is(err, ErrFilesystemClosed) {
		t.Fatalf("关闭后 Extend 必须返回 ErrFilesystemClosed: %v", err)
	}

	invalidManager := New(map[string]interface{}{
		"default": "invalid",
		"disks": map[string]interface{}{
			"invalid": map[string]interface{}{"type": 7},
			"empty":   map[string]interface{}{"type": "empty"},
		},
	})
	if _, err = invalidManager.Disk("first", "second"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("Disk 多名称必须拒绝: %v", err)
	}
	if _, err = invalidManager.Disk(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("非字符串驱动类型必须拒绝: %v", err)
	}
	if err = invalidManager.Extend("empty", func(map[string]interface{}) (Driver, error) { return nil, nil }); err != nil {
		t.Fatalf("注册空实例测试驱动失败: %v", err)
	}
	if _, err = invalidManager.Disk("empty"); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("返回 nil 的驱动工厂必须拒绝: %v", err)
	}
	if err = invalidManager.Extend("", func(map[string]interface{}) (Driver, error) { return &countingFilesystemDriver{}, nil }); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("空驱动类型必须拒绝: %v", err)
	}
	if err = invalidManager.Extend("valid", nil); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("nil 驱动工厂必须拒绝: %v", err)
	}

	var nilManager *Filesystem
	if _, err = nilManager.Disk(); !errors.Is(err, ErrFilesystemClosed) || nilManager.GetDefaultDriver() != "" || nilManager.GetConfig() != nil {
		t.Fatalf("nil 管理器 API 必须安全失败: disk_err=%v", err)
	}
	if err = nilManager.Close(); err != nil {
		t.Fatalf("nil 管理器 Close 必须幂等: %v", err)
	}
}

type countingFilesystemDriver struct {
	Driver
	closed *atomic.Int32
}

func (driver *countingFilesystemDriver) Close() error {
	if driver != nil && driver.closed != nil {
		driver.closed.Add(1)
	}
	return nil
}

func digestHex(digest interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}, content string) string {
	_, _ = digest.Write([]byte(content))
	return hex.EncodeToString(digest.Sum(nil))
}

func newUploadFileHeader(t *testing.T, name string, content []byte) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("创建 multipart 文件字段失败: %v", err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatalf("写入 multipart 文件失败: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.com/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err = request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("解析 multipart 表单失败: %v", err)
	}
	t.Cleanup(func() {
		if request.MultipartForm != nil {
			_ = request.MultipartForm.RemoveAll()
		}
	})
	return request.MultipartForm.File["file"][0]
}
