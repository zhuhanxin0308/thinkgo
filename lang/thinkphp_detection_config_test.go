package lang

import (
	"errors"
	"reflect"
	"testing"
)

// TestRequestDetectionConfigAndLanguageAliases 验证请求热路径配置与
// accept_language 别名使用 Init 的同一份不可变配置快照。
func TestRequestDetectionConfigAndLanguageAliases(t *testing.T) {
	manager := NewLang()
	err := manager.Init(map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": false,
		"allow_lang_list":     []interface{}{"zh-cn", "en-us"},
		"detect_var":          "locale",
		"use_cookie":          false,
		"cookie_var":          "site_lang",
		"header_var":          "x-site-lang",
		"accept_language": map[string]interface{}{
			"zh-hans": "zh-cn",
			"en":      "en-us",
		},
	})
	if err != nil {
		t.Fatalf("初始化请求语言检测配置失败: %v", err)
	}
	expected := RequestDetectionConfig{
		AutoDetectBrowser: false,
		DetectVariable:    "locale",
		UseCookie:         false,
		CookieVariable:    "site_lang",
		HeaderVariable:    "X-Site-Lang",
	}
	if actual := manager.RequestDetectionConfig(); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("请求语言检测配置错误: actual=%#v expected=%#v", actual, expected)
	}
	if actual := (*Lang)(nil).RequestDetectionConfig(); !reflect.DeepEqual(actual, RequestDetectionConfig{}) {
		t.Fatalf("nil Lang 必须返回空请求检测配置: %#v", actual)
	}
}

// TestLanguageAliasConfigurationRejectsInvalidMappings 验证别名配置不会接受
// 非对象、非法语言标识或非字符串目标。
func TestLanguageAliasConfigurationRejectsInvalidMappings(t *testing.T) {
	invalidValues := []interface{}{
		"zh-cn",
		map[string]interface{}{"bad tag!": "zh-cn"},
		map[string]interface{}{"zh-hans": 7},
		map[string]interface{}{"zh-hans": "bad tag!"},
	}
	for _, value := range invalidValues {
		if _, err := configLanguageAliases(value); !errors.Is(err, ErrInvalidLangConfig) {
			t.Errorf("非法语言别名 %#v 必须返回 ErrInvalidLangConfig: %v", value, err)
		}
	}
	aliases, err := configLanguageAliases(nil)
	if err != nil || len(aliases) != 0 {
		t.Fatalf("缺省语言别名必须返回空映射: aliases=%#v err=%v", aliases, err)
	}
}
