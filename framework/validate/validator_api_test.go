package validate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thinkgo/framework/lang"
)

// TestValidateReturnsIsolatedConcurrentResults 验证共享验证器的每次调用都返回独立结果。
func TestValidateReturnsIsolatedConcurrentResults(t *testing.T) {
	validator := NewValidator().
		SetRules(map[string]string{
			"email|邮箱": "required|email",
			"name|姓名":  "required|length:2,8",
		}).
		SetMessages(map[string]string{
			"email.required": "{:field}不能为空",
			"name.length":    "{:field}长度必须为{:param}",
		})

	const workers = 64
	start := make(chan struct{})
	errCh := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			data := map[string]interface{}{"email": "valid@example.com", "name": "张三"}
			expectedField := "email"
			if index%2 == 0 {
				data["email"] = "invalid"
			} else {
				data["name"] = "张"
				expectedField = "name"
			}
			result, err := validator.Validate(data)
			if err != nil {
				errCh <- err
				return
			}
			violations := result.Violations()
			if result.Valid() || len(violations) != 1 || violations[0].Field != expectedField {
				errCh <- fmt.Errorf("调用 %d 的结果串扰: %#v", index, violations)
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestValidationResultReturnsDefensiveCopies 验证调用方不能修改结果内部错误。
func TestValidationResultReturnsDefensiveCopies(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{"name": "required"})
	result, err := validator.Validate(map[string]interface{}{})
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	errorsCopy := result.Errors()
	violationsCopy := result.Violations()
	errorsCopy[0] = "mutated"
	violationsCopy[0].Message = "mutated"
	if result.FirstError() == "mutated" || result.Violations()[0].Message == "mutated" {
		t.Fatal("结果访问器必须返回防御性副本")
	}
}

// TestValidatorConfigurationSettersCopyInputs 验证配置 setter 不保留调用方 map/slice 引用。
func TestValidatorConfigurationSettersCopyInputs(t *testing.T) {
	rules := map[string]string{"name": "required"}
	messages := map[string]string{"name.required": "原始消息"}
	scenes := map[string][]string{"create": {"name"}}
	validator := NewValidator().SetRules(rules).SetMessages(messages).SetScenes(scenes)

	rules["name"] = "email"
	messages["name.required"] = "污染消息"
	scenes["create"][0] = "missing"
	result, err := validator.Validate(map[string]interface{}{}, WithScene("create"))
	if err != nil {
		t.Fatalf("验证失败: %v", err)
	}
	if result.FirstError() != "原始消息" {
		t.Fatalf("验证器配置应保持 setter 调用时快照，实际为 %q", result.FirstError())
	}
}

// TestValidateSeparatesConfigurationErrorsFromViolations 验证规则/场景错误通过 error 返回。
func TestValidateSeparatesConfigurationErrorsFromViolations(t *testing.T) {
	testCases := []struct {
		name      string
		rules     map[string]string
		scenes    map[string][]string
		options   []Option
		targetErr error
	}{
		{name: "未知规则", rules: map[string]string{"name": "requried"}, targetErr: ErrUnknownRule},
		{name: "缺少参数", rules: map[string]string{"name": "length:"}, targetErr: ErrInvalidRule},
		{name: "禁止多余参数", rules: map[string]string{"name": "required:oops"}, targetErr: ErrInvalidRule},
		{name: "边界倒置", rules: map[string]string{"age": "between:10,1"}, targetErr: ErrInvalidRule},
		{name: "负数长度", rules: map[string]string{"name": "length:-1"}, targetErr: ErrInvalidRule},
		{name: "非法正则", rules: map[string]string{"name": `regex:"["`}, targetErr: ErrInvalidRule},
		{name: "条件参数不足", rules: map[string]string{"name": "requireIf:role"}, targetErr: ErrInvalidRule},
		{name: "关联字段为空", rules: map[string]string{"name": "requireWith:"}, targetErr: ErrInvalidRule},
		{name: "目标字段为空", rules: map[string]string{"name": "different:"}, targetErr: ErrInvalidRule},
		{name: "日期格式为空", rules: map[string]string{"name": "dateFormat:"}, targetErr: ErrInvalidRule},
		{name: "正则斜杠未闭合", rules: map[string]string{"name": "regex:/abc"}, targetErr: ErrInvalidRule},
		{name: "空规则", rules: map[string]string{"name": ""}, targetErr: ErrInvalidRule},
		{name: "无效字段别名", rules: map[string]string{"|姓名": "required"}, targetErr: ErrInvalidRule},
		{name: "重复实际字段", rules: map[string]string{"name|姓名": "required", "name": "email"}, targetErr: ErrInvalidRule},
		{name: "未知场景", rules: map[string]string{"name": "required"}, options: []Option{WithScene("missing")}, targetErr: ErrUnknownScene},
		{name: "场景引用未知字段", rules: map[string]string{"name": "required"}, scenes: map[string][]string{"create": {"missing"}}, options: []Option{WithScene("create")}, targetErr: ErrInvalidScene},
		{name: "空场景", rules: map[string]string{"name": "required"}, scenes: map[string][]string{"create": {}}, options: []Option{WithScene("create")}, targetErr: ErrInvalidScene},
		{name: "场景字段重复", rules: map[string]string{"name": "required"}, scenes: map[string][]string{"create": {"name", "name"}}, options: []Option{WithScene("create")}, targetErr: ErrInvalidScene},
		{name: "未选中的场景也必须有效", rules: map[string]string{"name": "required"}, scenes: map[string][]string{"broken": {"missing"}}, targetErr: ErrInvalidScene},
		{name: "场景名不能包含首尾空白", rules: map[string]string{"name": "required"}, scenes: map[string][]string{" create ": {"name"}}, targetErr: ErrInvalidScene},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			validator := NewValidator().SetRules(testCase.rules).SetScenes(testCase.scenes)
			result, err := validator.Validate(map[string]interface{}{}, testCase.options...)
			if !errors.Is(err, testCase.targetErr) {
				t.Fatalf("应返回 %v，实际为 result=%#v err=%v", testCase.targetErr, result, err)
			}
			if !result.Valid() {
				t.Fatalf("配置错误不应伪装成用户数据违规: %#v", result.Violations())
			}
		})
	}
}

// TestValidateCollectAllAndSceneOptions 验证新 API 的批量错误与场景均为单次调用选项。
func TestValidateCollectAllAndSceneOptions(t *testing.T) {
	validator := NewValidator().
		SetRules(map[string]string{
			"email": "required|email",
			"name":  "required|length:2,8",
		}).
		SetScenes(map[string][]string{"email_only": {"email"}})

	all, err := validator.Validate(map[string]interface{}{}, CollectAllErrors())
	if err != nil {
		t.Fatalf("批量验证失败: %v", err)
	}
	if len(all.Violations()) != 2 {
		t.Fatalf("批量验证应返回两个必填错误，实际为 %#v", all.Violations())
	}
	sceneResult, err := validator.Validate(map[string]interface{}{}, WithScene("email_only"), CollectAllErrors())
	if err != nil {
		t.Fatalf("场景验证失败: %v", err)
	}
	if violations := sceneResult.Violations(); len(violations) != 1 || violations[0].Field != "email" {
		t.Fatalf("场景只能验证 email，实际为 %#v", violations)
	}
	defaultResult, err := validator.Validate(map[string]interface{}{})
	if err != nil {
		t.Fatalf("默认验证失败: %v", err)
	}
	if len(defaultResult.Violations()) != 1 {
		t.Fatalf("非批量调用应只返回第一条错误，实际为 %#v", defaultResult.Violations())
	}
}

// TestValidatorPlanCacheInvalidatesOnConfigurationChanges 验证规则/场景变更失效计划，消息变更立即生效。
func TestValidatorPlanCacheInvalidatesOnConfigurationChanges(t *testing.T) {
	validator := NewValidator().
		SetRules(map[string]string{"value": "required"}).
		SetMessages(map[string]string{"value.required": "first"}).
		SetScenes(map[string][]string{"only": {"value"}})
	first, err := validator.Validate(map[string]interface{}{}, WithScene("only"))
	if err != nil || first.FirstError() != "first" {
		t.Fatalf("首次计划编译结果错误: result=%#v err=%v", first, err)
	}
	validator.SetMessages(map[string]string{"value.email": "second"})
	validator.SetRules(map[string]string{"value": "email"})
	second, err := validator.Validate(map[string]interface{}{"value": "invalid"}, WithScene("only"))
	if err != nil || second.FirstError() != "second" {
		t.Fatalf("配置变更后应使用新计划和消息: result=%#v err=%v", second, err)
	}
	validator.AddScene("other", []string{"value"})
	if _, err := validator.Validate(map[string]interface{}{"value": "invalid"}, WithScene("other")); err != nil {
		t.Fatalf("新增场景后计划应重新编译: %v", err)
	}
}

// TestWithSceneOptionCanBeReusedConcurrently 验证同一个 Option 不含可变闭包状态。
func TestWithSceneOptionCanBeReusedConcurrently(t *testing.T) {
	validator := NewValidator().
		SetRules(map[string]string{"name": "required"}).
		SetScenes(map[string][]string{"create": {"name"}})
	option := WithScene(" create ")
	var waitGroup sync.WaitGroup
	for index := 0; index < 32; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := validator.Validate(map[string]interface{}{}, option)
			if err != nil || result.Valid() {
				t.Errorf("并发复用场景 Option 失败: result=%#v err=%v", result, err)
			}
		}()
	}
	waitGroup.Wait()
}

// TestValidatorSupportsConcurrentConfigurationReplacement 验证配置替换与计划读取并发时不会暴露半写入状态。
func TestValidatorSupportsConcurrentConfigurationReplacement(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{"value": "email"})
	const iterations = 300
	errCh := make(chan error, iterations)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		for index := 0; index < iterations; index++ {
			rule := "email"
			if index%2 == 0 {
				rule = "integer"
			}
			validator.SetRules(map[string]string{"value": rule})
			validator.SetMessages(map[string]string{"value." + rule: "值不合法"})
		}
	}()
	go func() {
		defer waitGroup.Done()
		for index := 0; index < iterations; index++ {
			result, err := validator.Validate(map[string]interface{}{"value": "invalid"})
			if err != nil {
				errCh <- err
				continue
			}
			if result.Valid() || result.Violations()[0].Field != "value" {
				errCh <- fmt.Errorf("并发配置替换返回异常结果: %#v", result)
			}
		}
	}()
	waitGroup.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestValidateRejectsInvalidOptionsAndNilReceiver 验证调用选项错误不会被当成数据违规。
func TestValidateRejectsInvalidOptionsAndNilReceiver(t *testing.T) {
	validator := NewValidator().SetRules(map[string]string{"name": "required"})
	testCases := [][]Option{
		{nil},
		{WithScene(" ")},
		{WithScene(strings.Repeat("a", maxSceneNameBytes+1))},
		{WithScene("first"), WithScene("second")},
	}
	for _, options := range testCases {
		result, err := validator.Validate(map[string]interface{}{}, options...)
		if !errors.Is(err, ErrInvalidOption) || !result.Valid() {
			t.Fatalf("无效选项应返回 ErrInvalidOption，result=%#v err=%v", result, err)
		}
	}
	var nilValidator *Validator
	if _, err := nilValidator.Validate(nil); !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("nil 验证器应返回 ErrInvalidOption，实际为 %v", err)
	}
}

// TestValidatorUsesConfiguredLanguage 验证直接消息键和默认规则键都通过多语言管理器解析。
func TestValidatorUsesConfiguredLanguage(t *testing.T) {
	directory := t.TempDir()
	translationFile := filepath.Join(directory, "zh-cn.json")
	content := []byte(`{"validate":{"name_required":"{:field}不能为空","default":{"email":"{:field}格式错误"}}}`)
	if err := os.WriteFile(translationFile, content, 0o600); err != nil {
		t.Fatalf("写入测试语言文件失败: %v", err)
	}
	language := lang.NewLang()
	if err := language.Load(translationFile, "zh-cn"); err != nil {
		t.Fatalf("加载测试语言文件失败: %v", err)
	}

	validator := NewValidator().
		SetRules(map[string]string{"email|邮箱": "required|email"}).
		SetMessages(map[string]string{"email.required": "validate.name_required"}).
		SetLang(language)

	requiredResult, err := validator.Validate(map[string]interface{}{})
	if err != nil || requiredResult.FirstError() != "邮箱不能为空" {
		t.Fatalf("直接消息键翻译错误: result=%#v err=%v", requiredResult, err)
	}
	emailResult, err := validator.Validate(map[string]interface{}{"email": "invalid"})
	if err != nil || emailResult.FirstError() != "邮箱格式错误" {
		t.Fatalf("默认规则键翻译错误: result=%#v err=%v", emailResult, err)
	}
}
