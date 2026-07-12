package driver

import (
	"errors"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type typedNilTemplateWriter struct{}

func (*typedNilTemplateWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

// TestGoTemplateRejectsTraversalTemplateName 验证包含路径穿越片段的模板名会被拒绝，
// 而不是被清理后映射到视图目录内的其它模板。
func TestGoTemplateRejectsTraversalTemplateName(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.html"), []byte(`safe`), 0o644); err != nil {
		t.Fatalf("写入测试模板失败: %v", err)
	}

	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{
		"view_path":   dir,
		"view_suffix": "html",
	}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}

	if exists, err := driver.Exists("../safe"); exists || !errors.Is(err, ErrUnsafeTemplatePath) {
		t.Fatal("包含 .. 的模板名应被拒绝，不应被清理后访问 safe.html")
	}
	if _, err := driver.Fetch("../safe", nil); !errors.Is(err, ErrUnsafeTemplatePath) {
		t.Fatalf("包含 .. 的模板名应返回非法模板错误，实际错误为 %v", err)
	}
	if exists, err := driver.Exists("safe"); err != nil || !exists {
		t.Fatal("普通模板名仍应可访问")
	}
}

// TestGoTemplateRejectsInvalidConfig 验证缺失根目录、未知键和危险后缀会在启动期失败。
func TestGoTemplateRejectsInvalidConfig(t *testing.T) {
	driver := NewGoTemplate()
	for _, config := range []map[string]interface{}{
		nil,
		{"view_path": ""},
		{"view_path": t.TempDir(), "view_suffix": "../html"},
		{"view_path": t.TempDir(), "unknown": true},
	} {
		if err := driver.Config(config); !errors.Is(err, ErrInvalidViewConfig) {
			t.Fatalf("非法配置 %#v 应返回 ErrInvalidViewConfig，实际为 %v", config, err)
		}
	}
}

// TestGoTemplateRenderingCacheAndFunctionReset 验证 HTML 自动转义、缓存复用以及函数变更后的原子失效。
func TestGoTemplateRenderingCacheAndFunctionReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte(`{{ upper .Name }}`), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": dir, "view_suffix": "html"}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}
	if err := driver.SetFuncMap(map[string]interface{}{"upper": strings.ToUpper}); err != nil {
		t.Fatalf("注册模板函数失败: %v", err)
	}
	first, err := driver.Fetch("index", map[string]interface{}{"Name": "<b>"})
	if err != nil || first != "&lt;B&gt;" {
		t.Fatalf("模板渲染或自动转义错误: content=%q err=%v", first, err)
	}
	if err := os.WriteFile(path, []byte(`changed`), 0o600); err != nil {
		t.Fatalf("修改模板失败: %v", err)
	}
	second, err := driver.Fetch("index", map[string]interface{}{"Name": "<b>"})
	if err != nil || second != first {
		t.Fatalf("缓存模板应保持首次解析结果: content=%q err=%v", second, err)
	}
	if err := driver.SetFuncMap(map[string]interface{}{"upper": strings.ToLower}); err != nil {
		t.Fatalf("更新模板函数失败: %v", err)
	}
	third, err := driver.Fetch("index", nil)
	if err != nil || third != "changed" {
		t.Fatalf("函数更新后应失效旧模板缓存: content=%q err=%v", third, err)
	}
	if err := driver.SetFuncMap(map[string]interface{}{"bad": 1}); !errors.Is(err, ErrInvalidTemplateFunction) {
		t.Fatalf("非函数模板变量必须被拒绝，实际为 %v", err)
	}
}

// TestGoTemplateRejectsSymlinkEscapeAndNonRegularFiles 验证真实路径不能逃逸根目录，目录也不能伪装成模板。
func TestGoTemplateRejectsSymlinkEscapeAndNonRegularFiles(t *testing.T) {
	workspace := t.TempDir()
	viewRoot := filepath.Join(workspace, "views")
	if err := os.MkdirAll(viewRoot, 0o755); err != nil {
		t.Fatalf("创建视图目录失败: %v", err)
	}
	secret := filepath.Join(workspace, "secret.html")
	if err := os.WriteFile(secret, []byte("private-template"), 0o600); err != nil {
		t.Fatalf("写入根目录外模板失败: %v", err)
	}
	link := filepath.Join(viewRoot, "linked.html")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("当前环境不允许创建符号链接: %v", err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": viewRoot}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}
	if exists, err := driver.Exists("linked"); exists || !errors.Is(err, ErrUnsafeTemplatePath) {
		t.Fatalf("越界符号链接必须被拒绝: exists=%v err=%v", exists, err)
	}
	if _, err := driver.Fetch("linked", nil); !errors.Is(err, ErrUnsafeTemplatePath) {
		t.Fatalf("读取越界符号链接应返回 ErrUnsafeTemplatePath，实际为 %v", err)
	}
	if err := os.Mkdir(filepath.Join(viewRoot, "directory.html"), 0o755); err != nil {
		t.Fatalf("创建伪模板目录失败: %v", err)
	}
	if exists, err := driver.Exists("directory"); exists || !errors.Is(err, ErrTemplateNotRegular) {
		t.Fatalf("目录不得作为模板: exists=%v err=%v", exists, err)
	}
}

// TestGoTemplateConcurrentFetchBuildsSingleCacheEntry 验证并发首次解析共享同一缓存结果且不会污染缓存表。
func TestGoTemplateConcurrentFetchBuildsSingleCacheEntry(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(`hello {{.Name}}`), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": dir}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := driver.Fetch("index", map[string]interface{}{"Name": "ThinkGo"})
			errorsChannel <- err
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("并发渲染失败: %v", err)
		}
	}
	driver.mutex.RLock()
	cacheEntries := len(driver.cache)
	activeLoads := len(driver.loads)
	driver.mutex.RUnlock()
	if cacheEntries != 1 {
		t.Fatalf("同一模板只能生成一个缓存条目，实际为 %d", cacheEntries)
	}
	if activeLoads != 0 {
		t.Fatalf("并发模板解析完成后不得残留加载任务，实际为 %d", activeLoads)
	}
}

// TestGoTemplateRejectsTypedNilWriterAndFunction 验证接口中的空写入器和空函数不会延迟到模板执行阶段才 panic。
func TestGoTemplateRejectsTypedNilWriterAndFunction(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`{{ optional "value" }}`), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": directory}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}
	var writer *typedNilTemplateWriter
	if err := driver.Display(writer, "index", nil); !errors.Is(err, ErrInvalidTemplateWriter) {
		t.Fatalf("类型化空写入器应返回 ErrInvalidTemplateWriter，实际为 %v", err)
	}
	var function func(string) string
	if err := driver.SetFuncMap(map[string]interface{}{"optional": function}); !errors.Is(err, ErrInvalidTemplateFunction) {
		t.Fatalf("类型化空模板函数应返回 ErrInvalidTemplateFunction，实际为 %v", err)
	}
}

// TestGoTemplateMissingRootIsConfigurationError 验证视图根目录失效与普通模板不存在具有可区分的错误语义。
func TestGoTemplateMissingRootIsConfigurationError(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": missingRoot}); err != nil {
		t.Fatalf("延迟配置不存在的视图目录失败: %v", err)
	}
	if exists, err := driver.Exists("index"); exists || !errors.Is(err, ErrInvalidViewConfig) {
		t.Fatalf("失效视图根目录应返回 ErrInvalidViewConfig: exists=%t err=%v", exists, err)
	}
}

// TestGoTemplateCacheIsBounded 验证大量可访问模板不会让进程内模板缓存无限增长。
func TestGoTemplateCacheIsBounded(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`bounded`), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	driver := NewGoTemplate()
	if err := driver.Config(map[string]interface{}{"view_path": directory}); err != nil {
		t.Fatalf("配置模板驱动失败: %v", err)
	}
	driver.mutex.Lock()
	for index := 0; index < maxCachedTemplates; index++ {
		driver.cache[filepath.Join(directory, "synthetic-"+strconv.Itoa(index)+".html")] = template.New("synthetic")
	}
	driver.mutex.Unlock()
	content, err := driver.Fetch("index", nil)
	if err != nil || content != "bounded" {
		t.Fatalf("缓存达到上限后仍应正常渲染: content=%q err=%v", content, err)
	}
	driver.mutex.RLock()
	entries := len(driver.cache)
	driver.mutex.RUnlock()
	if entries != maxCachedTemplates {
		t.Fatalf("模板缓存不得超过上限，实际为 %d", entries)
	}
}

// TestGoTemplateZeroValueCanBeConfigured 验证公开驱动类型的零值在显式配置后具备完整渲染能力。
func TestGoTemplateZeroValueCanBeConfigured(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(`zero-value`), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	driver := &GoTemplate{}
	if err := driver.Config(map[string]interface{}{"view_path": directory, "cache": true}); err != nil {
		t.Fatalf("配置零值模板驱动失败: %v", err)
	}
	content, err := driver.Fetch("index", nil)
	if err != nil || content != "zero-value" {
		t.Fatalf("零值模板驱动渲染失败: content=%q err=%v", content, err)
	}
}
