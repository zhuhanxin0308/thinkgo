package lang

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if err := manager.Init(map[string]interface{}{"default_lang": "zh-cn"}); err != nil {
		t.Fatalf("初始化语言管理器失败: %v", err)
	}
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
	if err := manager.Init(map[string]interface{}{"default_lang": "zh-cn"}); err != nil {
		t.Fatalf("初始化语言管理器失败: %v", err)
	}

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

// TestLangInitRejectsInvalidConfig 验证语言标识、未知键和检测选项在启动期严格失败。
func TestLangInitRejectsInvalidConfig(t *testing.T) {
	tests := []map[string]interface{}{
		{"default_lang": "../en"},
		{"default_lang": "zh-cn", "unknown": true},
		{"default_lang": "zh-cn", "auto_detect_browser": "yes"},
		{"default_lang": "zh-cn", "allow_lang_list": []interface{}{"en-us", 1}},
		{"default_lang": "zh-cn", "allow_lang_list": []interface{}{"en-us"}},
		{"default_lang": "zh-cn", "header_var": "Bad\r\nHeader"},
	}
	for _, config := range tests {
		manager := NewLang()
		if err := manager.Init(config); !errors.Is(err, ErrInvalidLangConfig) {
			t.Fatalf("非法配置 %#v 应返回 ErrInvalidLangConfig，实际为 %v", config, err)
		}
	}
}

// TestLangLoadIsAtomicAndStrict 验证损坏文件、重复键、扁平键冲突和非字符串叶子不会留下部分语言包。
func TestLangLoadIsAtomicAndStrict(t *testing.T) {
	dir := t.TempDir()
	manager := NewLang()
	validFile := filepath.Join(dir, "zh-cn.json")
	if err := os.WriteFile(validFile, []byte(`{"auth":{"title":"登录"}}`), 0o600); err != nil {
		t.Fatalf("写入合法语言包失败: %v", err)
	}
	if err := manager.Load(validFile, "zh-cn"); err != nil {
		t.Fatalf("加载合法语言包失败: %v", err)
	}

	invalidCases := map[string]string{
		"broken":     `{"auth":`,
		"duplicate":  `{"auth":"first","auth":"second"}`,
		"collision":  `{"auth.title":"flat","auth":{"title":"nested"}}`,
		"non-string": `{"count":1}`,
	}
	for language, body := range invalidCases {
		file := filepath.Join(dir, language+".json")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatalf("写入非法语言包失败: %v", err)
		}
		if err := manager.Load(file, language); err == nil {
			t.Fatalf("非法语言包 %s 必须返回错误", language)
		}
		if manager.HasLang(language) {
			t.Fatalf("加载失败不得留下空语言包 %q", language)
		}
	}
	if got := manager.Get("auth.title", nil, "zh-cn"); got != "登录" {
		t.Fatalf("失败加载不得污染已有语言包，实际为 %q", got)
	}
}

// TestLangLoadAllCommitsAtomically 验证目录中任一文件失败时不会提交其它已解析语言。
func TestLangLoadAllCommitsAtomically(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zh-cn.json"), []byte(`{"title":"中文"}`), 0o600); err != nil {
		t.Fatalf("写入中文语言包失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "en-us.json"), []byte(`{"title":`), 0o600); err != nil {
		t.Fatalf("写入损坏语言包失败: %v", err)
	}
	manager := NewLang()
	if err := manager.LoadAll(dir); err == nil {
		t.Fatal("目录中存在损坏文件时 LoadAll 必须失败")
	}
	if manager.HasLang("zh-cn") || manager.HasLang("en-us") {
		t.Fatal("LoadAll 失败时不得提交部分语言包")
	}
}

// TestLangMatchingIsDeterministicAndAllowed 验证主语言匹配遵守允许列表顺序且不会返回未授权语言。
func TestLangMatchingIsDeterministicAndAllowed(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"zh-cn": `{"title":"中文"}`,
		"en-us": `{"title":"US"}`,
		"en-gb": `{"title":"GB"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatalf("写入语言包 %s 失败: %v", name, err)
		}
	}
	manager := NewLang()
	if err := manager.Init(map[string]interface{}{
		"default_lang":    "zh-cn",
		"allow_lang_list": []interface{}{"zh-cn", "en-gb", "en-us"},
	}); err != nil {
		t.Fatalf("初始化语言允许列表失败: %v", err)
	}
	if err := manager.LoadAll(dir); err != nil {
		t.Fatalf("加载语言包失败: %v", err)
	}
	if matched := manager.MatchLanguage("en"); matched != "en-gb" {
		t.Fatalf("主语言应按允许列表稳定匹配 en-gb，实际为 %q", matched)
	}
	if matched := manager.MatchLanguage("ja-jp"); matched != "" {
		t.Fatalf("未加载或未允许语言不得匹配，实际为 %q", matched)
	}
}

type panickingLangValue struct{}

func (panickingLangValue) String() string {
	panic("不得调用不受信对象的 String 方法")
}

// TestLangVariablesOnlyFormatSafeScalars 验证变量替换不执行不受信对象方法，并保留无法安全格式化的占位符。
func TestLangVariablesOnlyFormatSafeScalars(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "zh-cn.json")
	if err := os.WriteFile(file, []byte(`{"message":"id={:id}, value={:value}"}`), 0o600); err != nil {
		t.Fatalf("写入语言包失败: %v", err)
	}
	manager := NewLang()
	if err := manager.Load(file, "zh-cn"); err != nil {
		t.Fatalf("加载语言包失败: %v", err)
	}
	message := manager.Get("message", map[string]interface{}{
		"id":    18,
		"value": panickingLangValue{},
	}, "zh-cn")
	if message != "id=18, value={:value}" {
		t.Fatalf("变量替换安全边界错误: %q", message)
	}
	if strings.Contains(message, "panic") {
		t.Fatal("变量格式化不得泄露内部 panic")
	}
}

// TestLangPublicLifecycleAndConfigSnapshot 验证语言切换、存在性、解析接口和检测配置快照不会泄露内部状态。
func TestLangPublicLifecycleAndConfigSnapshot(t *testing.T) {
	directory := t.TempDir()
	zhFile := filepath.Join(directory, "zh-cn.json")
	enFile := filepath.Join(directory, "en-us.json")
	jaFile := filepath.Join(directory, "ja-jp.json")
	if err := os.WriteFile(zhFile, []byte(`{"title":"中文","only_default":"默认"}`), 0o600); err != nil {
		t.Fatalf("写入中文语言包失败: %v", err)
	}
	if err := os.WriteFile(enFile, []byte(`{"title":"English"}`), 0o600); err != nil {
		t.Fatalf("写入英文语言包失败: %v", err)
	}
	if err := os.WriteFile(jaFile, []byte(`{"title":"日本語"}`), 0o600); err != nil {
		t.Fatalf("写入日文语言包失败: %v", err)
	}

	manager := NewLang()
	if err := manager.Init(map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": false,
		"allow_lang_list":     []interface{}{"zh-cn", "en-us"},
		"detect_var":          "locale",
		"use_cookie":          false,
		"cookie_var":          "locale_cookie",
		"header_var":          "X-App-Lang",
	}); err != nil {
		t.Fatalf("初始化语言配置失败: %v", err)
	}
	config := manager.DetectionConfig()
	if config.AutoDetectBrowser || config.UseCookie || config.DetectVariable != "locale" || config.HeaderVariable != "X-App-Lang" {
		t.Fatalf("检测配置快照错误: %#v", config)
	}
	config.AllowedLanguages[0] = "mutated"
	if manager.DetectionConfig().AllowedLanguages[0] != "zh-cn" {
		t.Fatal("调用方修改检测配置快照不得污染语言管理器")
	}
	if manager.GetDefaultLang() != "zh-cn" || manager.GetLang() != "zh-cn" {
		t.Fatalf("默认语言或当前语言错误: default=%q current=%q", manager.GetDefaultLang(), manager.GetLang())
	}

	parsed, err := manager.Parse(zhFile)
	if err != nil || parsed["title"] != "中文" {
		t.Fatalf("独立解析语言包失败: parsed=%#v err=%v", parsed, err)
	}
	if manager.HasLang("zh-cn") {
		t.Fatal("Parse 不得隐式提交语言包")
	}
	if err := manager.Load(zhFile, "zh-cn"); err != nil {
		t.Fatalf("加载中文语言包失败: %v", err)
	}
	if err := manager.Load(enFile, "en-us"); err != nil {
		t.Fatalf("加载英文语言包失败: %v", err)
	}
	if err := manager.Load(jaFile, "ja-jp"); err != nil {
		t.Fatalf("加载未允许语言包失败: %v", err)
	}
	if !manager.Has("title", "en-us") || manager.Has("missing", "en-us") {
		t.Fatal("翻译键存在性判断错误")
	}
	if manager.FindLangByPrefix("en") != "en-us" {
		t.Fatal("主语言前缀应稳定匹配已允许语言")
	}
	if err := manager.SetLang("en-US"); err != nil || manager.GetLang() != "en-us" {
		t.Fatalf("切换当前语言失败: current=%q err=%v", manager.GetLang(), err)
	}
	if got := manager.Get("only_default", nil, ""); got != "默认" {
		t.Fatalf("空语言参数应使用当前语言并回退默认语言，实际为 %q", got)
	}
	if err := manager.SetLang("ja-jp"); !errors.Is(err, ErrLanguageNotLoaded) {
		t.Fatalf("切换到未加载语言应失败，实际为 %v", err)
	}
	if manager.Has("title", "ja-jp") || manager.Get("title", nil, "ja-jp") != "中文" {
		t.Fatal("显式读取也必须遵守语言允许列表并回退默认语言")
	}

	zeroValue := &Lang{}
	if err := zeroValue.Init(map[string]interface{}{"default_lang": "zh-cn"}); err != nil {
		t.Fatalf("零值语言管理器初始化失败: %v", err)
	}
	if err := zeroValue.Load(zhFile, "zh-cn"); err != nil || zeroValue.Get("title", nil, "zh-cn") != "中文" {
		t.Fatalf("零值语言管理器加载失败: value=%q err=%v", zeroValue.Get("title", nil, "zh-cn"), err)
	}
}

// TestFormatLanguageVariableSupportsOnlyFiniteScalars 验证所有受支持标量都能稳定格式化，伪造 JSON 数字和非有限浮点会被拒绝。
func TestFormatLanguageVariableSupportsOnlyFiniteScalars(t *testing.T) {
	valid := []interface{}{
		"text", true,
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
		float32(1.25), float64(2.5), json.Number("3.75"),
	}
	for _, value := range valid {
		if formatted, ok := formatLanguageVariable(value); !ok || formatted == "" {
			t.Fatalf("合法标量 %#v 格式化失败: value=%q ok=%t", value, formatted, ok)
		}
	}
	invalid := []interface{}{math.NaN(), math.Inf(1), float32(math.Inf(-1)), json.Number("true"), json.Number(""), struct{}{}}
	for _, value := range invalid {
		if formatted, ok := formatLanguageVariable(value); ok || formatted != "" {
			t.Fatalf("非法变量 %#v 不得被格式化: value=%q ok=%t", value, formatted, ok)
		}
	}
}

// TestLangLoadAllReplacesStalePacks 验证目录重载成功后会移除已删除语言包，避免继续提供过期翻译。
func TestLangLoadAllReplacesStalePacks(t *testing.T) {
	directory := t.TempDir()
	zhFile := filepath.Join(directory, "zh-cn.json")
	enFile := filepath.Join(directory, "en-us.json")
	if err := os.WriteFile(zhFile, []byte(`{"title":"旧中文"}`), 0o600); err != nil {
		t.Fatalf("写入中文语言包失败: %v", err)
	}
	if err := os.WriteFile(enFile, []byte(`{"title":"Old English"}`), 0o600); err != nil {
		t.Fatalf("写入英文语言包失败: %v", err)
	}
	manager := NewLang()
	if err := manager.LoadAll(directory); err != nil {
		t.Fatalf("首次加载语言目录失败: %v", err)
	}
	if err := os.Remove(enFile); err != nil {
		t.Fatalf("删除过期英文语言包失败: %v", err)
	}
	if err := os.WriteFile(zhFile, []byte(`{"title":"新中文"}`), 0o600); err != nil {
		t.Fatalf("更新中文语言包失败: %v", err)
	}
	if err := manager.LoadAll(directory); err != nil {
		t.Fatalf("重载语言目录失败: %v", err)
	}
	if manager.HasLang("en-us") || manager.Get("title", nil, "zh-cn") != "新中文" {
		t.Fatalf("目录重载仍保留过期状态: en=%t zh=%q", manager.HasLang("en-us"), manager.Get("title", nil, "zh-cn"))
	}
}

// TestLangRejectsInvalidUTF8 验证语言文件不能依赖 JSON 解码器的替换字符静默修复损坏文本。
func TestLangRejectsInvalidUTF8(t *testing.T) {
	file := filepath.Join(t.TempDir(), "zh-cn.json")
	content := []byte{'{', '"', 'k', '"', ':', '"', 0xff, '"', '}'}
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatalf("写入损坏 UTF-8 语言包失败: %v", err)
	}
	if err := NewLang().Load(file, "zh-cn"); !errors.Is(err, ErrInvalidLanguageFile) {
		t.Fatalf("损坏 UTF-8 语言包应返回 ErrInvalidLanguageFile，实际为 %v", err)
	}
}

// TestLangLoadAllSupportsTrustedSymlinkRoot 验证显式传入的语言根目录符号链接会按真实目录加载，而不是静默得到空语言集。
func TestLangLoadAllSupportsTrustedSymlinkRoot(t *testing.T) {
	workspace := t.TempDir()
	realRoot := filepath.Join(workspace, "real-lang")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatalf("创建真实语言目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "zh-cn.json"), []byte(`{"title":"中文"}`), 0o600); err != nil {
		t.Fatalf("写入语言包失败: %v", err)
	}
	linkedRoot := filepath.Join(workspace, "linked-lang")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Skipf("当前环境不允许创建目录符号链接: %v", err)
	}
	manager := NewLang()
	if err := manager.LoadAll(linkedRoot); err != nil {
		t.Fatalf("通过受信任符号链接根目录加载失败: %v", err)
	}
	if !manager.HasLang("zh-cn") {
		t.Fatal("符号链接根目录中的语言包未被加载")
	}
}

func newDetectLanguageFixture(t *testing.T, config map[string]interface{}) *Lang {
	t.Helper()
	directory := t.TempDir()
	for name, body := range map[string]string{
		"zh-cn": `{"title":"中文"}`,
		"en-gb": `{"title":"英国"}`,
		"en-us": `{"title":"美国"}`,
		"ja-jp": `{"title":"日本語"}`,
	} {
		if err := os.WriteFile(filepath.Join(directory, name+".json"), []byte(body), 0o600); err != nil {
			t.Fatalf("写入检测语言包 %s 失败: %v", name, err)
		}
	}
	manager := NewLang()
	if err := manager.Init(config); err != nil {
		t.Fatalf("初始化检测配置失败: %v", err)
	}
	if err := manager.LoadAll(directory); err != nil {
		t.Fatalf("加载检测语言包失败: %v", err)
	}
	return manager
}

// TestDetectLanguage 验证一次性语言检测入口的来源优先级、前缀匹配和 qvalue 兼容语义。
func TestDetectLanguage(t *testing.T) {
	manager := newDetectLanguageFixture(t, map[string]interface{}{
		"default_lang":        "zh-cn",
		"auto_detect_browser": true,
		"allow_lang_list":     []interface{}{"zh-cn", "en-gb", "en-us", "ja-jp"},
		"use_cookie":          true,
	})
	tests := []struct {
		name                                  string
		query, cookie, header, acceptLanguage string
		expected                              string
	}{
		{name: "query", query: "en", cookie: "ja-jp", header: "en-us", acceptLanguage: "ja-jp", expected: "en-gb"},
		{name: "cookie", cookie: "en-us", header: "ja-jp", acceptLanguage: "ja-jp", expected: "en-us"},
		{name: "header", header: "ja-jp", acceptLanguage: "en-us", expected: "ja-jp"},
		{name: "accept-language", acceptLanguage: "en-US;q=0.4, ja-JP;q=1", expected: "ja-jp"},
		{name: "default", query: "unsupported", cookie: "unsupported", header: "unsupported", acceptLanguage: "xx;q=1", expected: "zh-cn"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := manager.DetectLanguage(testCase.query, testCase.cookie, testCase.header, testCase.acceptLanguage); got != testCase.expected {
				t.Fatalf("语言检测结果错误: got=%q expected=%q", got, testCase.expected)
			}
		})
	}

	managerNoCookie := newDetectLanguageFixture(t, map[string]interface{}{
		"default_lang": "zh-cn",
		"use_cookie":   false,
	})
	if got := managerNoCookie.DetectLanguage("", "en-us", "ja-jp", ""); got != "ja-jp" {
		t.Fatalf("关闭 Cookie 检测后应跳过 Cookie 来源，实际为 %q", got)
	}
}

// TestDetectLanguageSnapshotConcurrency 验证检测配置并发发布和读取不会混用半套配置。
func TestDetectLanguageSnapshotConcurrency(t *testing.T) {
	manager := newDetectLanguageFixture(t, map[string]interface{}{"default_lang": "zh-cn", "use_cookie": true})
	var wait sync.WaitGroup
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for round := 0; round < 100; round++ {
				if index%2 == 0 {
					_ = manager.Init(map[string]interface{}{"default_lang": "zh-cn", "use_cookie": true})
				} else {
					_ = manager.Init(map[string]interface{}{"default_lang": "en-us", "use_cookie": false})
				}
				selected := manager.DetectLanguage("", "en-us", "ja-jp", "")
				if selected != "zh-cn" && selected != "en-us" && selected != "ja-jp" {
					t.Errorf("检测快照返回未知语言: %q", selected)
					return
				}
			}
		}(index)
	}
	wait.Wait()
}

// BenchmarkDetectLanguage 记录请求语言检测的时间和分配基线。
func BenchmarkDetectLanguage(b *testing.B) {
	manager := NewLang()
	if err := manager.Init(map[string]interface{}{"default_lang": "zh-cn", "allow_lang_list": []interface{}{"zh-cn", "en-us", "ja-jp"}}); err != nil {
		b.Fatal(err)
	}
	directory := b.TempDir()
	for name := range map[string]bool{"zh-cn": true, "en-us": true, "ja-jp": true} {
		if err := os.WriteFile(filepath.Join(directory, name+".json"), []byte(`{"title":"ok"}`), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	if err := manager.LoadAll(directory); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for index := 0; index < b.N; index++ {
		_ = manager.DetectLanguage("", "", "", "en-US;q=0.8,ja-JP;q=1")
	}
}
