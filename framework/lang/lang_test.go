package lang

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLangGetFallsBackToDefaultLang 验证当前语言缺少 key 时回退到默认语言包。
func TestLangGetFallsBackToDefaultLang(t *testing.T) {
	dir := t.TempDir()
	zhFile := filepath.Join(dir, "zh-cn.json")
	enFile := filepath.Join(dir, "en-us.json")

	if err := os.WriteFile(zhFile, []byte(`{"auth":{"login_success":"登录成功","logout":"退出成功"}}`), 0o644); err != nil {
		t.Fatalf("写入默认语言包失败: %v", err)
	}
	if err := os.WriteFile(enFile, []byte(`{"auth":{"logout":"Signed out"}}`), 0o644); err != nil {
		t.Fatalf("写入英文语言包失败: %v", err)
	}

	manager := NewLang()
	manager.Init(map[string]interface{}{"default_lang": "zh-cn"})
	if err := manager.Load(zhFile, "zh-cn"); err != nil {
		t.Fatalf("加载默认语言包失败: %v", err)
	}
	if err := manager.Load(enFile, "en-us"); err != nil {
		t.Fatalf("加载英文语言包失败: %v", err)
	}

	if got := manager.Get("auth.login_success", nil, "en-us"); got != "登录成功" {
		t.Fatalf("英文语言包缺少 key 时应回退默认语言，实际为 %q", got)
	}
	if got := manager.Get("auth.logout", nil, "en-us"); got != "Signed out" {
		t.Fatalf("当前语言已有 key 时应优先使用当前语言，实际为 %q", got)
	}
}

// TestLangGetFallsBackToKeyWhenNoLanguageHasValue 验证所有语言包都缺失时仍返回原 key。
func TestLangGetFallsBackToKeyWhenNoLanguageHasValue(t *testing.T) {
	manager := NewLang()
	manager.Init(map[string]interface{}{"default_lang": "zh-cn"})

	if got := manager.Get("missing.key", nil, "en-us"); got != "missing.key" {
		t.Fatalf("所有语言包都缺失时应返回原 key，实际为 %q", got)
	}
}

// TestLangLoadAllReturnsFileError 验证批量加载语言包时不会吞掉损坏文件错误。
func TestLangLoadAllReturnsFileError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zh-cn.json"), []byte(`{"auth":`), 0o644); err != nil {
		t.Fatalf("写入损坏语言包失败: %v", err)
	}

	manager := NewLang()
	if err := manager.LoadAll(dir); err == nil {
		t.Fatal("损坏语言包应返回错误")
	}
}

// TestLangLoadAllIgnoresMissingDirectory 验证未配置语言包目录时不会阻断应用启动。
func TestLangLoadAllIgnoresMissingDirectory(t *testing.T) {
	manager := NewLang()
	if err := manager.LoadAll(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("不存在的语言目录应被视为未配置，实际错误: %v", err)
	}
}
