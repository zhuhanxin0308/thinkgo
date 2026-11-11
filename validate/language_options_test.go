package validate

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/lang"
)

func validationTestLanguage(t *testing.T) *lang.Lang {
	t.Helper()
	manager := lang.NewLang()
	for locale, content := range map[string]string{
		"zh-cn": `{"validate":{"default":{"required":"{:field}不能为空"}},"custom":"{:field}必填，规则{:rule}"}`,
		"en-us": `{"validate":{"default":{"required":"{:field} is required in English"}},"custom":"{:field} needed by {:rule}"}`,
	} {
		path := filepath.Join(t.TempDir(), locale+".json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := manager.Load(path, locale); err != nil {
			t.Fatal(err)
		}
	}
	return manager
}

// TestValidationLanguageOptionsAreCallScoped 验证共享规则计划及语言服务不被并发请求污染。
func TestValidationLanguageOptionsAreCallScoped(t *testing.T) {
	language := validationTestLanguage(t)
	validator := NewValidator().SetRules(map[string]string{"email|邮箱": "required|email"})
	validator.AddScene("create", []string{"email"})
	var workers sync.WaitGroup
	const calls = 32
	for index := range calls {
		workers.Go(func() {
			locale, expected := "zh-cn", "邮箱不能为空"
			if index%2 == 1 {
				locale, expected = "en-us", "邮箱 is required in English"
			}
			result, err := validator.Validate(nil, WithScene("create"), WithLang(language), WithLocale(locale))
			if err != nil || result.FirstError() != expected {
				t.Errorf("请求语言串扰: locale=%s result=%v err=%v", locale, result.Errors(), err)
			}
			result, err = ValidateRules(nil, map[string]string{"email|邮箱": "required"}, WithLang(language), WithLocale(locale))
			if err != nil || result.FirstError() != expected {
				t.Errorf("共享规则缓存污染语言: locale=%s result=%v err=%v", locale, result.Errors(), err)
			}
		})
	}
	workers.Wait()
	if language.GetLang() != "zh-cn" {
		t.Fatal("调用选项改变了共享语言服务")
	}
	result, err := validator.Validate(nil)
	if err != nil || result.FirstError() != "邮箱 is required" {
		t.Fatalf("本次语言选项污染后续默认配置: result=%v err=%v", result.Errors(), err)
	}
}

// TestValidationLanguageMessagesAndFallback 验证默认管理器、自定义消息、别名和缺失翻译的回退。
func TestValidationLanguageMessagesAndFallback(t *testing.T) {
	language := validationTestLanguage(t)
	validator := NewValidator().SetRules(map[string]string{"email|邮箱": "required"}).SetLang(language)
	for _, test := range []struct {
		message string
		locale  string
		want    string
	}{
		{"", "", "邮箱不能为空"},
		{"custom", "en-us", "邮箱 needed by required"},
		{"{:field}: {:rule}/{:param}", "en-us", "邮箱: required/"},
		{"missing.key", "en-us", "missing.key"},
		{"", "fr-fr", "邮箱不能为空"},
	} {
		validator.SetMessages(map[string]string{"email.required": test.message})
		result, err := validator.Validate(nil, WithLocale(test.locale))
		if err != nil || result.FirstError() != test.want {
			t.Errorf("消息回退错误: test=%+v result=%v err=%v", test, result.Errors(), err)
		}
	}
	result, err := validator.Validate(nil, WithLang(lang.NewLang()), WithLocale("en-us"))
	if err != nil || result.FirstError() != "邮箱 is required" {
		t.Fatalf("调用语言服务没有覆盖默认服务: result=%v err=%v", result.Errors(), err)
	}
	result, err = validator.Validate(nil)
	if err != nil || result.FirstError() != "邮箱不能为空" {
		t.Fatalf("调用语言服务污染默认服务: result=%v err=%v", result.Errors(), err)
	}
}

// TestValidationLanguageOptionErrors 验证冲突选项是配置错误，并允许重复相同选项。
func TestValidationLanguageOptionErrors(t *testing.T) {
	language := validationTestLanguage(t)
	validator := NewValidator().SetRules(map[string]string{"name": "required"})
	for _, options := range [][]Option{
		{WithLang(nil)}, {WithLang(language), WithLang(lang.NewLang())},
		{WithLocale("zh-cn"), WithLocale("en-us")}, {WithLocale(""), WithLocale("en-us")},
	} {
		if result, err := validator.Validate(nil, options...); !errors.Is(err, ErrInvalidOption) || !result.Valid() {
			t.Fatalf("语言选项错误被当作输入违规: result=%v err=%v", result.Errors(), err)
		}
	}
	if _, err := validator.Validate(nil, WithLang(language), WithLang(language), WithLocale(" EN-US "), WithLocale("en-us")); err != nil {
		t.Fatalf("相同语言选项应允许重复: %v", err)
	}
}
