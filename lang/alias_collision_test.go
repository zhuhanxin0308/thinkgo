package lang

import (
	"errors"
	"reflect"
	"testing"
)

// TestLanguageAliasNormalizationConflictsAreTransactional 验证大小写及空格碰撞失败时不发布部分配置。
func TestLanguageAliasNormalizationConflictsAreTransactional(t *testing.T) {
	manager := NewLang()
	manager.data["zh-cn"] = map[string]string{"hello": "你好"}
	manager.data["en-us"] = map[string]string{"hello": "hello"}
	if err := manager.Init(map[string]interface{}{
		"accept_language": map[string]interface{}{"zh-hans": "zh-cn"},
		"allow_lang_list": []interface{}{"zh-cn", "en-us"},
	}); err != nil {
		t.Fatal(err)
	}
	before := manager.DetectionConfig()
	for _, aliases := range []map[string]interface{}{
		{"ZH-hans": "zh-cn", "zh-Hans": "en-us"},
		{" zh-hans ": "zh-cn", "zh-hans": "en-us"},
		{"ZH-HANS": "en-us", "zh-hans": "zh-cn"},
		{"zh-Hans": "fr-fr", "zh-hans": "zh-cn"},
	} {
		if err := manager.Init(map[string]interface{}{
			"default_lang": "en-us", "detect_var": "changed", "accept_language": aliases,
		}); !errors.Is(err, ErrInvalidLangConfig) {
			t.Fatalf("规范化冲突未拒绝: aliases=%v err=%v", aliases, err)
		}
		if manager.GetDefaultLang() != "zh-cn" || !reflect.DeepEqual(manager.DetectionConfig(), before) {
			t.Fatal("失败初始化改变了默认语言或检测配置")
		}
		if manager.MatchLanguage("ZH-HANS") != "zh-cn" || manager.DetectLanguage("", "", "", "zh-hans") != "zh-cn" {
			t.Fatal("失败初始化改变了已发布的别名快照")
		}
	}
}

// TestLanguageAliasIdenticalTargetsCoalesce 保留同目标重复别名的兼容合并行为。
func TestLanguageAliasIdenticalTargetsCoalesce(t *testing.T) {
	aliases, err := configLanguageAliases(map[string]interface{}{"ZH-HANS": "ZH-CN", " zh-hans ": "zh-cn"})
	if err != nil || !reflect.DeepEqual(aliases, map[string]string{"zh-hans": "zh-cn"}) {
		t.Fatalf("同目标规范化重复项没有合并: aliases=%v err=%v", aliases, err)
	}
}
