package validate

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"thinkgo/framework/lang"
)

const maxSceneNameBytes = 256

var (
	// ErrUnknownRule 表示规则名称不存在，通常是配置拼写错误。
	ErrUnknownRule = errors.New("未知验证规则")
	// ErrInvalidRule 表示字段定义、规则语法或规则参数无效。
	ErrInvalidRule = errors.New("验证规则配置无效")
	// ErrUnknownScene 表示请求的验证场景不存在。
	ErrUnknownScene = errors.New("验证场景不存在")
	// ErrInvalidScene 表示场景为空、包含重复字段或引用了无规则字段。
	ErrInvalidScene = errors.New("验证场景配置无效")
	// ErrInvalidOption 表示单次验证选项无效。
	ErrInvalidOption = errors.New("验证选项无效")
)

// Violation 描述一条用户数据违规；配置错误通过 Validate 的 error 单独返回。
type Violation struct {
	Field   string
	Alias   string
	Rule    string
	Param   string
	Message string
}

// Result 是一次验证调用的不可变结果。
type Result struct {
	violations []Violation
}

// Valid 返回是否没有用户数据违规。
func (r Result) Valid() bool {
	return len(r.violations) == 0
}

// FirstError 返回第一条错误消息。
func (r Result) FirstError() string {
	if len(r.violations) == 0 {
		return ""
	}
	return r.violations[0].Message
}

// Errors 返回全部错误消息的副本。
func (r Result) Errors() []string {
	errorsFound := make([]string, len(r.violations))
	for index, violation := range r.violations {
		errorsFound[index] = violation.Message
	}
	return errorsFound
}

// Violations 返回全部结构化违规的副本。
func (r Result) Violations() []Violation {
	return append([]Violation(nil), r.violations...)
}

type validationOptions struct {
	scene      string
	collectAll bool
	location   *time.Location
}

// Option 配置一次 Validate 调用，不修改共享验证器。
type Option func(*validationOptions) error

// WithScene 仅验证指定场景中的字段。
func WithScene(name string) Option {
	normalized := strings.TrimSpace(name)
	return func(options *validationOptions) error {
		if normalized == "" {
			return fmt.Errorf("%w: 场景名称不能为空", ErrInvalidOption)
		}
		if len(normalized) > maxSceneNameBytes {
			return fmt.Errorf("%w: 场景名称不能超过 %d 字节", ErrInvalidOption, maxSceneNameBytes)
		}
		if options.scene != "" && options.scene != normalized {
			return fmt.Errorf("%w: 不能同时指定场景 %q 和 %q", ErrInvalidOption, options.scene, normalized)
		}
		options.scene = normalized
		return nil
	}
}

// CollectAllErrors 开启本次调用的批量错误收集。
func CollectAllErrors() Option {
	return func(options *validationOptions) error {
		options.collectAll = true
		return nil
	}
}

// WithLocation 设置本次验证中日期规则使用的应用时区。
func WithLocation(location *time.Location) Option {
	return func(options *validationOptions) error {
		if location == nil {
			return fmt.Errorf("%w: 时区不能为空", ErrInvalidOption)
		}
		options.location = location
		return nil
	}
}

// Validator 保存可并发读取的验证配置；每次 Validate 返回独立 Result。
type Validator struct {
	configMu sync.RWMutex
	rules    map[string]string
	messages map[string]string
	scenes   map[string][]string
	language *lang.Lang
	version  uint64
	plans    map[string]cachedValidationPlan
	location *time.Location
	now      func() time.Time
}

type validatorConfig struct {
	messages map[string]string
	language *lang.Lang
}

type cachedValidationPlan struct {
	version uint64
	fields  []compiledField
}

// NewValidator 创建空验证器。
func NewValidator() *Validator {
	return &Validator{
		rules:    make(map[string]string),
		messages: make(map[string]string),
		scenes:   make(map[string][]string),
		plans:    make(map[string]cachedValidationPlan),
		location: time.Local,
		now:      time.Now,
	}
}

// ValidateRules 使用规则快照执行无场景验证，适合控制器便捷 API 的热路径。
// 规则配置错误仍通过 error 返回；成功的规则快照会进入有界进程级缓存。
func ValidateRules(data map[string]interface{}, rules map[string]string, options ...Option) (Result, error) {
	cacheKey, cacheable := sharedValidationPlanKey(rules, nil, "")
	if cacheable {
		if validator, ok := processValidatorCache.load(cacheKey); ok {
			return validator.Validate(data, options...)
		}
	}
	validator := NewValidator().SetRules(rules)
	result, err := validator.Validate(data, options...)
	if err != nil {
		return result, err
	}
	if cacheable {
		processValidatorCache.store(cacheKey, validator)
	}
	return result, nil
}

// SetRules 原子替换规则，并复制调用方 map。
func (v *Validator) SetRules(rules map[string]string) *Validator {
	v.configMu.Lock()
	v.rules = cloneStringMap(rules)
	v.invalidatePlansLocked()
	v.configMu.Unlock()
	return v
}

// SetMessages 原子替换自定义消息，并复制调用方 map。
func (v *Validator) SetMessages(messages map[string]string) *Validator {
	v.configMu.Lock()
	v.messages = cloneStringMap(messages)
	v.configMu.Unlock()
	return v
}

// SetScenes 原子替换场景，并递归复制字段切片。
func (v *Validator) SetScenes(scenes map[string][]string) *Validator {
	v.configMu.Lock()
	v.scenes = cloneScenes(scenes)
	v.invalidatePlansLocked()
	v.configMu.Unlock()
	return v
}

// AddScene 注册或替换单个场景。
func (v *Validator) AddScene(name string, fields []string) *Validator {
	v.configMu.Lock()
	updatedScenes := cloneScenes(v.scenes)
	updatedScenes[name] = append([]string(nil), fields...)
	v.scenes = updatedScenes
	v.invalidatePlansLocked()
	v.configMu.Unlock()
	return v
}

// SetLang 设置多语言实例。
func (v *Validator) SetLang(language *lang.Lang) *Validator {
	v.configMu.Lock()
	v.language = language
	v.configMu.Unlock()
	return v
}

// SetLocation 设置验证器默认使用的应用时区。
func (v *Validator) SetLocation(location *time.Location) *Validator {
	if v == nil {
		return v
	}
	if location == nil {
		location = time.Local
	}
	v.configMu.Lock()
	v.location = location
	v.configMu.Unlock()
	return v
}

// Validate 执行一次无状态验证；规则或场景配置错误通过 error 返回。
func (v *Validator) Validate(data map[string]interface{}, optionFunctions ...Option) (Result, error) {
	if v == nil {
		return Result{}, fmt.Errorf("%w: 验证器不能为空", ErrInvalidOption)
	}
	options := validationOptions{}
	for _, applyOption := range optionFunctions {
		if applyOption == nil {
			return Result{}, fmt.Errorf("%w: option 不能为空", ErrInvalidOption)
		}
		if err := applyOption(&options); err != nil {
			return Result{}, err
		}
	}
	location, current := v.runtimeTime(options.location)

	config, plan, err := v.configAndPlan(options.scene)
	if err != nil {
		return Result{}, err
	}
	result := Result{violations: make([]Violation, 0)}
	for _, field := range plan {
		value, exists := data[field.name]
		for _, rule := range field.rules {
			if !exists && !rule.requiresPresence() {
				continue
			}
			if evaluateRule(data, field.name, value, exists, rule, location, current) {
				continue
			}
			result.violations = append(result.violations, buildViolation(config, field, rule))
			if !options.collectAll {
				return result, nil
			}
		}
	}
	return result, nil
}

// runtimeTime 获取本次验证使用的时区和当前时间，避免日期规则读取进程全局时区。
func (v *Validator) runtimeTime(override *time.Location) (*time.Location, time.Time) {
	v.configMu.RLock()
	location := v.location
	clock := v.now
	v.configMu.RUnlock()
	if override != nil {
		location = override
	}
	if location == nil {
		location = time.Local
	}
	if clock == nil {
		clock = time.Now
	}
	return location, clock().In(location)
}

func (v *Validator) configAndPlan(scene string) (validatorConfig, []compiledField, error) {
	for {
		v.configMu.RLock()
		if cached, exists := v.plans[scene]; exists && cached.version == v.version {
			config := validatorConfig{messages: v.messages, language: v.language}
			v.configMu.RUnlock()
			return config, cached.fields, nil
		}
		version := v.version
		rules := v.rules
		scenes := v.scenes
		v.configMu.RUnlock()

		cacheKey, cacheable := sharedValidationPlanKey(rules, scenes, scene)
		if cacheable {
			if plan, ok := processValidationPlanCache.load(cacheKey); ok {
				v.configMu.Lock()
				if v.version != version {
					v.configMu.Unlock()
					continue
				}
				if v.plans == nil {
					v.plans = make(map[string]cachedValidationPlan)
				}
				v.plans[scene] = cachedValidationPlan{version: version, fields: plan}
				config := validatorConfig{messages: v.messages, language: v.language}
				v.configMu.Unlock()
				return config, plan, nil
			}
		}

		plan, err := compileValidationPlan(rules, scenes, scene)
		if err != nil {
			return validatorConfig{}, nil, err
		}
		if cacheable {
			processValidationPlanCache.store(cacheKey, plan)
		}
		v.configMu.Lock()
		if v.version != version {
			v.configMu.Unlock()
			continue
		}
		if v.plans == nil {
			v.plans = make(map[string]cachedValidationPlan)
		}
		v.plans[scene] = cachedValidationPlan{version: version, fields: plan}
		config := validatorConfig{messages: v.messages, language: v.language}
		v.configMu.Unlock()
		return config, plan, nil
	}
}

func (v *Validator) invalidatePlansLocked() {
	v.version++
	v.plans = make(map[string]cachedValidationPlan)
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneScenes(source map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(source))
	for name, fields := range source {
		cloned[name] = append([]string(nil), fields...)
	}
	return cloned
}

func buildViolation(config validatorConfig, field compiledField, rule compiledRule) Violation {
	displayField := field.alias
	if displayField == "" {
		displayField = field.name
	}
	variables := map[string]interface{}{
		"field": displayField,
		"rule":  rule.name,
		"param": rule.param,
	}

	message := configuredMessage(config.messages, field.name, rule.name)
	if message != "" && config.language != nil {
		translated := config.language.Get(message, variables, "")
		if translated != message {
			message = translated
		}
	}
	if message == "" && config.language != nil {
		defaultKey := "validate.default." + rule.name
		translated := config.language.Get(defaultKey, variables, "")
		if translated != defaultKey {
			message = translated
		}
	}
	if message == "" {
		message = defaultMessage(displayField, rule.name, rule.param)
	}
	message = interpolateValidationMessage(message, displayField, rule.name, rule.param)
	return Violation{
		Field: field.name, Alias: field.alias, Rule: rule.name, Param: rule.param, Message: message,
	}
}

func configuredMessage(messages map[string]string, field string, rule string) string {
	keys := []string{field + "." + rule, field, rule}
	for _, key := range keys {
		if message, exists := messages[key]; exists {
			return message
		}
	}
	return ""
}

func interpolateValidationMessage(message string, field string, rule string, param string) string {
	replacer := strings.NewReplacer(
		"{:field}", field,
		"{:rule}", rule,
		"{:param}", param,
	)
	return replacer.Replace(message)
}

func defaultMessage(field string, rule string, param string) string {
	switch rule {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "email":
		return fmt.Sprintf("%s must be a valid email", field)
	case "max":
		return fmt.Sprintf("%s must not exceed %s", field, param)
	case "min":
		return fmt.Sprintf("%s must be at least %s", field, param)
	case "number", "float":
		return fmt.Sprintf("%s must be a number", field)
	case "integer":
		return fmt.Sprintf("%s must be an integer", field)
	case "length":
		return fmt.Sprintf("%s length must be %s", field, param)
	default:
		return fmt.Sprintf("%s is not valid (%s)", field, rule)
	}
}

func compileValidationPlan(rules map[string]string, scenes map[string][]string, sceneName string) ([]compiledField, error) {
	fields := make(map[string]compiledField, len(rules))
	definitions := make([]string, 0, len(rules))
	for definition := range rules {
		definitions = append(definitions, definition)
	}
	sort.Strings(definitions)
	for _, definition := range definitions {
		specification := rules[definition]
		fieldName, alias, err := parseFieldDefinition(definition)
		if err != nil {
			return nil, err
		}
		if _, exists := fields[fieldName]; exists {
			return nil, fmt.Errorf("%w: 字段 %q 被重复定义", ErrInvalidRule, fieldName)
		}
		compiledRules, err := parseRuleSpecification(fieldName, specification)
		if err != nil {
			return nil, err
		}
		fields[fieldName] = compiledField{name: fieldName, alias: alias, rules: compiledRules}
	}
	scenePlans, err := compileScenePlans(fields, scenes)
	if err != nil {
		return nil, err
	}

	if sceneName != "" {
		plan, exists := scenePlans[sceneName]
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrUnknownScene, sceneName)
		}
		return plan, nil
	}

	fieldNames := make([]string, 0, len(fields))
	for fieldName := range fields {
		fieldNames = append(fieldNames, fieldName)
	}
	sort.Strings(fieldNames)
	plan := make([]compiledField, 0, len(fieldNames))
	for _, fieldName := range fieldNames {
		plan = append(plan, fields[fieldName])
	}
	return plan, nil
}

// compileScenePlans 校验全部场景并生成不可变计划，避免未选中的错误配置被静默保留。
func compileScenePlans(fields map[string]compiledField, scenes map[string][]string) (map[string][]compiledField, error) {
	sceneNames := make([]string, 0, len(scenes))
	for sceneName := range scenes {
		sceneNames = append(sceneNames, sceneName)
	}
	sort.Strings(sceneNames)

	plans := make(map[string][]compiledField, len(scenes))
	for _, sceneName := range sceneNames {
		if sceneName == "" || sceneName != strings.TrimSpace(sceneName) || len(sceneName) > maxSceneNameBytes {
			return nil, fmt.Errorf("%w: 场景名称 %q 无效", ErrInvalidScene, sceneName)
		}
		sceneFields := scenes[sceneName]
		if len(sceneFields) == 0 {
			return nil, fmt.Errorf("%w: 场景 %q 不能为空", ErrInvalidScene, sceneName)
		}
		plan := make([]compiledField, 0, len(sceneFields))
		seen := make(map[string]struct{}, len(sceneFields))
		for _, rawField := range sceneFields {
			fieldName, _, err := parseFieldDefinition(rawField)
			if err != nil {
				return nil, fmt.Errorf("%w: 场景 %q 包含无效字段 %q", ErrInvalidScene, sceneName, rawField)
			}
			if _, duplicate := seen[fieldName]; duplicate {
				return nil, fmt.Errorf("%w: 场景 %q 重复字段 %q", ErrInvalidScene, sceneName, fieldName)
			}
			field, exists := fields[fieldName]
			if !exists {
				return nil, fmt.Errorf("%w: 场景 %q 引用了无规则字段 %q", ErrInvalidScene, sceneName, fieldName)
			}
			seen[fieldName] = struct{}{}
			plan = append(plan, field)
		}
		plans[sceneName] = plan
	}
	return plans, nil
}
