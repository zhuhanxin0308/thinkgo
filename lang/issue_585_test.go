package lang

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/config"
)

// TestIssue585StrongTypedConfigCollections 验证 Config.Set 保留的强类型集合可直接用于 Lang.Init。
func TestIssue585StrongTypedConfigCollections(t *testing.T) {
	cfg := config.NewConfig()
	if err := cfg.Set("lang.default_lang", "zh-cn"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("lang.allow_lang_list", []string{"zh-cn", "zh-tw", "en-us"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("lang.accept_language", map[string]string{
		"zh-hans": "zh-cn",
		"zh-hant": "zh-tw",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("lang.extend_list", []string{}); err != nil {
		t.Fatal(err)
	}

	manager := NewLang()
	if err := manager.Init(cfg.GetMap("lang")); err != nil {
		t.Fatalf("强类型配置初始化 Lang.Init 失败: %v", err)
	}

	for _, emptyExtend := range []any{
		[]string{},
		map[string]string{},
		map[string][]string{},
		map[string]any{},
	} {
		if err := NewLang().Init(map[string]any{"extend_list": emptyExtend}); err != nil {
			t.Fatalf("空 extend_list (%T) 应兼容通过: %v", emptyExtend, err)
		}
	}

	for _, nonEmptyExtend := range []any{
		[]string{"extra.json"},
		map[string][]string{"zh-cn": {"extra.json"}},
		"invalid",
	} {
		if err := NewLang().Init(map[string]any{"extend_list": nonEmptyExtend}); !errors.Is(err, ErrInvalidLangConfig) {
			t.Fatalf("非空或非法 extend_list (%#v) 应返回 ErrInvalidLangConfig，实际为 %v", nonEmptyExtend, err)
		}
	}

	if err := NewLang().Init(map[string]any{
		"accept_language": map[string]string{"zh-hans": "zh-cn", "ZH-HANS": "zh-tw"},
	}); !errors.Is(err, ErrInvalidLangConfig) {
		t.Fatalf("强类型 accept_language 别名目标冲突应返回 ErrInvalidLangConfig，实际为 %v", err)
	}
}

// TestIssue585GetHasHonorAliasesAndRFC9110AcceptLanguage 验证 Get/Has 遵守别名与主语言回退，
// 且 Accept-Language 遵守 q=0 显式拒绝与精确匹配优先于降级主语言前缀匹配。
func TestIssue585GetHasHonorAliasesAndRFC9110AcceptLanguage(t *testing.T) {
	dir := t.TempDir()
	packs := map[string]string{
		"en-us": `{"hello":"Hello","only_default":"Default EN"}`,
		"zh-cn": `{"hello":"你好简体"}`,
		"zh-tw": `{"hello":"你好繁體"}`,
	}
	for code, content := range packs {
		if err := os.WriteFile(filepath.Join(dir, code+".json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewLang()
	if err := manager.Init(map[string]any{
		"default_lang":    "en-us",
		"allow_lang_list": []string{"zh-cn", "zh-tw", "en-us"},
		"accept_language": map[string]string{
			"zh-hans": "zh-cn",
			"zh-hant": "zh-tw",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadAll(dir); err != nil {
		t.Fatal(err)
	}

	// 1. Get / Has 显式语言码走别名与主语言前缀解析
	for _, tc := range []struct {
		code string
		want string
	}{
		{"zh-hans", "你好简体"},
		{"ZH-HANS", "你好简体"},
		{"zh-hant", "你好繁體"},
		{"zh", "你好简体"},
		{"en", "Hello"},
	} {
		if matched := manager.MatchLanguage(tc.code); matched == "" {
			t.Fatalf("MatchLanguage(%q) 不应为空", tc.code)
		}
		if !manager.Has("hello", tc.code) {
			t.Fatalf("Has(\"hello\", %q) 应返回 true", tc.code)
		}
		if got := manager.Get("hello", nil, tc.code); got != tc.want {
			t.Fatalf("Get(\"hello\", nil, %q) = %q, want %q", tc.code, got, tc.want)
		}
	}
	if got := manager.Get("only_default", nil, "zh-hans"); got != "Default EN" {
		t.Fatalf("别名语言包缺失翻译键时应回退默认语言: got=%q", got)
	}

	// 2. Accept-Language: 精确繁体包 zh-TW;q=0.9 不得被首项 zh-HK 的主语言前缀回退遮蔽
	if got := manager.DetectLanguage("", "", "", "zh-HK, zh-TW;q=0.9, en-US;q=0.8"); got != "zh-tw" {
		t.Fatalf("zh-HK 不应遮蔽显式精确包 zh-TW: got=%q want=\"zh-tw\"", got)
	}
	if got := manager.DetectLanguage("", "", "", "zh-HK;q=0.9, zh-Hant;q=0.9"); got != "zh-tw" {
		t.Fatalf("同品质下别名精确匹配应优先于前缀降级匹配: got=%q want=\"zh-tw\"", got)
	}

	// 3. Accept-Language: q=0 显式拉黑默认语言、别名或主语言前缀
	zhDefault := NewLang()
	if err := zhDefault.Init(map[string]any{
		"default_lang":    "zh-cn",
		"accept_language": map[string]string{"zh-hans": "zh-cn"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := zhDefault.LoadAll(dir); err != nil {
		t.Fatal(err)
	}
	qZeroCases := []struct {
		header string
		want   string
	}{
		{"zh-cn;q=0, *;q=0.8", "en-us"},
		{"*;q=0.8, zh-cn;q=0", "en-us"},
		{"zh;q=0, *;q=0.8", "en-us"},
		{"zh-hans;q=0, *;q=0.8", "en-us"},
		{"zh-cn;q=0, zh-HK;q=0.9", "zh-tw"},
		{"zh-cn;q=0", "en-us"},
	}
	for _, tc := range qZeroCases {
		if got := zhDefault.DetectLanguage("", "", "", tc.header); got != tc.want {
			t.Fatalf("Accept-Language %q 违反 q=0 拒绝语义: got=%q want=%q", tc.header, got, tc.want)
		}
	}
}

// TestIssue585ReadLanguageFileRejectsNonRegularPath 验证语言文件读取拒绝目录等非普通文件。
func TestIssue585ReadLanguageFileRejectsNonRegularPath(t *testing.T) {
	dir := t.TempDir()
	if err := NewLang().Load(dir, "zh-cn"); !errors.Is(err, ErrInvalidLanguageFile) {
		t.Fatalf("读取目录作为语言文件应返回 ErrInvalidLanguageFile，实际为 %v", err)
	}
}
