package validate

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"thinkgo/framework/lang"
)

// regexCache 缓存按规则参数编译的正则，避免每次校验都重新编译同一模式。
var regexCache sync.Map // map[string]*regexp.Regexp

// compileCachedRegex 返回缓存的编译结果，非法模式返回 nil。
func compileCachedRegex(pattern string) *regexp.Regexp {
	if cached, ok := regexCache.Load(pattern); ok {
		re, _ := cached.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		regexCache.Store(pattern, (*regexp.Regexp)(nil))
		return nil
	}
	regexCache.Store(pattern, re)
	return re
}

var (
	emailRegex       = regexp.MustCompile(`^[\w-\.]+@([\w-]+\.)+[\w-]{2,4}$`)
	alphaRegex       = regexp.MustCompile(`^[a-zA-Z]+$`)
	alphaNumRegex    = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	alphaDashRegex   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	chsRegex         = regexp.MustCompile(`^[\p{Han}]+$`)
	chsAlphaRegex    = regexp.MustCompile(`^[\p{Han}a-zA-Z]+$`)
	chsAlphaNumRegex = regexp.MustCompile(`^[\p{Han}a-zA-Z0-9]+$`)
	chsDashRegex     = regexp.MustCompile(`^[\p{Han}a-zA-Z0-9_-]+$`)
	urlRegex         = regexp.MustCompile(`^((https|http|ftp|rtsp|mms)?://)[^\s]+`)
	mobileRegex      = regexp.MustCompile(`^1[3-9]\d{9}$`)
	idCardRegex      = regexp.MustCompile(`^\d{17}[\dXx]$`)
)

// Validator 提供接近 ThinkPHP 风格的通用验证能力。
type Validator struct {
	Rule         map[string]string
	Message      map[string]string
	Scene        map[string][]string
	Lang         *lang.Lang
	currentScene string
	batch        bool
	errors       []string
}

// NewValidator 创建空验证器。
func NewValidator() *Validator {
	return &Validator{
		Rule:    make(map[string]string),
		Message: make(map[string]string),
		Scene:   make(map[string][]string),
	}
}

// Make 创建兼容旧用法的验证器。
func Make(data map[string]interface{}, rules map[string]string, message ...map[string]string) *LegacyValidator {
	validator := &LegacyValidator{
		Validator: Validator{
			Rule:    rules,
			Message: make(map[string]string),
			Scene:   make(map[string][]string),
		},
		data: data,
	}
	if len(message) > 0 {
		validator.Message = message[0]
	}
	return validator
}

// LegacyValidator 兼容旧版本的 Scene/Check 调用方式。
type LegacyValidator struct {
	Validator
	data map[string]interface{}
}

// Check 执行旧式验证。
func (v *LegacyValidator) Check() bool {
	return v.Validator.Check(v.data)
}

// Scene 设置旧式场景。
func (v *LegacyValidator) Scene(name string) *LegacyValidator {
	v.currentScene = name
	return v
}

// AddScene 注册旧式场景。
func (v *LegacyValidator) AddScene(name string, fields []string) *LegacyValidator {
	v.Validator.Scene[name] = fields
	return v
}

// SetScene 设置当前验证场景。
func (v *Validator) SetScene(name string) *Validator {
	v.currentScene = name
	return v
}

// SetLang 设置多语言实例。
func (v *Validator) SetLang(l *lang.Lang) *Validator {
	v.Lang = l
	return v
}

// Batch 开启批量验证模式。
func (v *Validator) Batch(batch bool) *Validator {
	v.batch = batch
	return v
}

// Check 执行数据验证。
func (v *Validator) Check(data map[string]interface{}) bool {
	v.errors = make([]string, 0)
	rules := v.Rule

	// 字段遍历顺序：场景指定了字段则按场景顺序；否则按字段名排序，
	// 保证非批量模式下“第一条错误”稳定可复现（修复 map 无序导致的随机错误）。
	var fieldOrder []string
	if v.currentScene != "" {
		if fields, ok := v.Scene[v.currentScene]; ok && len(fields) > 0 {
			filtered := make(map[string]string, len(fields))
			for _, field := range fields {
				if rule, exists := rules[field]; exists {
					filtered[field] = rule
					fieldOrder = append(fieldOrder, field)
				}
			}
			rules = filtered
		}
	}
	if fieldOrder == nil {
		fieldOrder = make([]string, 0, len(rules))
		for field := range rules {
			fieldOrder = append(fieldOrder, field)
		}
		sort.Strings(fieldOrder)
	}

	for _, field := range fieldOrder {
		for _, rule := range strings.Split(rules[field], "|") {
			if !v.validateField(data, field, strings.TrimSpace(rule)) {
				if !v.batch {
					v.currentScene = ""
					return false
				}
			}
		}
	}

	v.currentScene = ""
	return len(v.errors) == 0
}

// GetError 返回第一条错误信息。
func (v *Validator) GetError() string {
	if len(v.errors) == 0 {
		return ""
	}
	return v.errors[0]
}

// Error 是 GetError 的别名。
func (v *Validator) Error() string {
	return v.GetError()
}

// GetErrors 返回全部错误信息。
func (v *Validator) GetErrors() []string {
	return v.errors
}

func (v *Validator) validateField(data map[string]interface{}, field string, rule string) bool {
	val, exists := data[field]

	ruleName := rule
	ruleParam := ""
	if strings.Contains(rule, ":") {
		parts := strings.SplitN(rule, ":", 2)
		ruleName = parts[0]
		ruleParam = parts[1]
	}

	if !isKnownRule(ruleName) {
		// 未知规则通常来自拼写错误，必须失败关闭，避免把业务必填或格式校验静默绕过。
		v.addError(field, ruleName, ruleParam)
		return false
	}

	if !exists && !requiresFieldPresence(ruleName) {
		return true
	}

	valid := true
	switch ruleName {
	case "required":
		valid = !isEmptyValue(val)
	case "number":
		_, err := strconv.ParseFloat(fmt.Sprintf("%v", val), 64)
		valid = err == nil
	case "integer":
		_, err := strconv.Atoi(fmt.Sprintf("%v", val))
		valid = err == nil
	case "float":
		_, err := strconv.ParseFloat(fmt.Sprintf("%v", val), 64)
		valid = err == nil
	case "boolean":
		_, err := strconv.ParseBool(fmt.Sprintf("%v", val))
		valid = err == nil
	case "email":
		valid = emailRegex.MatchString(fmt.Sprintf("%v", val))
	case "array":
		_, valid = val.([]interface{})
		if !valid {
			_, valid = val.(map[string]interface{})
		}
	case "accepted":
		text := strings.ToLower(fmt.Sprintf("%v", val))
		valid = text == "yes" || text == "on" || text == "1" || text == "true"
	case "date":
		_, err := parseRuleTime(fmt.Sprintf("%v", val))
		valid = err == nil
	case "alpha":
		valid = alphaRegex.MatchString(fmt.Sprintf("%v", val))
	case "alphaNum":
		valid = alphaNumRegex.MatchString(fmt.Sprintf("%v", val))
	case "alphaDash":
		valid = alphaDashRegex.MatchString(fmt.Sprintf("%v", val))
	case "chs":
		valid = chsRegex.MatchString(fmt.Sprintf("%v", val))
	case "chsAlpha":
		valid = chsAlphaRegex.MatchString(fmt.Sprintf("%v", val))
	case "chsAlphaNum":
		valid = chsAlphaNumRegex.MatchString(fmt.Sprintf("%v", val))
	case "chsDash":
		valid = chsDashRegex.MatchString(fmt.Sprintf("%v", val))
	case "ip":
		valid = isValidIP(fmt.Sprintf("%v", val))
	case "url":
		valid = urlRegex.MatchString(fmt.Sprintf("%v", val))
	case "in":
		valid = containsValue(strings.Split(ruleParam, ","), fmt.Sprintf("%v", val))
	case "notIn":
		valid = !containsValue(strings.Split(ruleParam, ","), fmt.Sprintf("%v", val))
	case "between":
		valid = compareRange(fmt.Sprintf("%v", val), ruleParam, true)
	case "notBetween":
		valid = compareRange(fmt.Sprintf("%v", val), ruleParam, false)
	case "length":
		valid = validateLength(fmt.Sprintf("%v", val), ruleParam)
	case "max":
		valid = compareBoundary(fmt.Sprintf("%v", val), ruleParam, true)
	case "min":
		valid = compareBoundary(fmt.Sprintf("%v", val), ruleParam, false)
	case "eq":
		valid = fmt.Sprintf("%v", val) == ruleParam
	case "gt":
		valid = compareNumeric(fmt.Sprintf("%v", val), ruleParam, ">")
	case "lt":
		valid = compareNumeric(fmt.Sprintf("%v", val), ruleParam, "<")
	case "egt":
		valid = compareNumeric(fmt.Sprintf("%v", val), ruleParam, ">=")
	case "elt":
		valid = compareNumeric(fmt.Sprintf("%v", val), ruleParam, "<=")
	case "regex":
		if re := compileCachedRegex(ruleParam); re != nil {
			valid = re.MatchString(fmt.Sprintf("%v", val))
		} else {
			valid = false
		}
	case "confirm":
		targetField := field + "_confirm"
		if ruleParam != "" {
			targetField = ruleParam
		}
		valid = fmt.Sprintf("%v", val) == fmt.Sprintf("%v", data[targetField])
	case "different":
		valid = fmt.Sprintf("%v", val) != fmt.Sprintf("%v", data[ruleParam])
	case "mobile":
		valid = mobileRegex.MatchString(fmt.Sprintf("%v", val))
	case "dateFormat":
		_, err := time.Parse(ruleParam, fmt.Sprintf("%v", val))
		valid = err == nil
	case "after":
		current, err := parseRuleTime(fmt.Sprintf("%v", val))
		if err != nil {
			valid = false
			break
		}
		target, err := parseRuleTime(resolveComparisonValue(data, ruleParam))
		valid = err == nil && current.After(target)
	case "before":
		current, err := parseRuleTime(fmt.Sprintf("%v", val))
		if err != nil {
			valid = false
			break
		}
		target, err := parseRuleTime(resolveComparisonValue(data, ruleParam))
		valid = err == nil && current.Before(target)
	case "requireIf":
		params := strings.SplitN(ruleParam, ",", 2)
		if len(params) != 2 {
			valid = false
			break
		}
		if fmt.Sprintf("%v", data[params[0]]) == params[1] {
			valid = !isEmptyValue(val)
		}
	case "requireWith":
		relatedFields := strings.Split(ruleParam, ",")
		required := false
		for _, relatedField := range relatedFields {
			if relatedValue, ok := data[strings.TrimSpace(relatedField)]; ok && !isEmptyValue(relatedValue) {
				required = true
				break
			}
		}
		if required {
			valid = !isEmptyValue(val)
		}
	case "idCard":
		valid = isValidIDCard(fmt.Sprintf("%v", val))
	}

	if !valid {
		v.addError(field, ruleName, ruleParam)
		return false
	}
	return true
}

func isKnownRule(ruleName string) bool {
	switch ruleName {
	case "required",
		"number",
		"integer",
		"float",
		"boolean",
		"email",
		"array",
		"accepted",
		"date",
		"alpha",
		"alphaNum",
		"alphaDash",
		"chs",
		"chsAlpha",
		"chsAlphaNum",
		"chsDash",
		"ip",
		"url",
		"in",
		"notIn",
		"between",
		"notBetween",
		"length",
		"max",
		"min",
		"eq",
		"gt",
		"lt",
		"egt",
		"elt",
		"regex",
		"confirm",
		"different",
		"mobile",
		"dateFormat",
		"after",
		"before",
		"requireIf",
		"requireWith",
		"idCard":
		return true
	default:
		return false
	}
}

func requiresFieldPresence(ruleName string) bool {
	switch ruleName {
	case "required", "requireIf", "requireWith":
		return true
	default:
		return false
	}
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func compareRange(value string, ruleParam string, inclusive bool) bool {
	params := strings.Split(ruleParam, ",")
	if len(params) != 2 {
		return false
	}
	min, err := strconv.ParseFloat(strings.TrimSpace(params[0]), 64)
	if err != nil {
		return false
	}
	max, err := strconv.ParseFloat(strings.TrimSpace(params[1]), 64)
	if err != nil {
		return false
	}
	current, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	if inclusive {
		return current >= min && current <= max
	}
	return current < min || current > max
}

func validateLength(value string, ruleParam string) bool {
	params := strings.Split(ruleParam, ",")
	length := len(value)
	if len(params) == 1 {
		expected, err := strconv.Atoi(strings.TrimSpace(params[0]))
		return err == nil && length == expected
	}
	if len(params) == 2 {
		min, err := strconv.Atoi(strings.TrimSpace(params[0]))
		if err != nil {
			return false
		}
		max, err := strconv.Atoi(strings.TrimSpace(params[1]))
		return err == nil && length >= min && length <= max
	}
	return false
}

func compareBoundary(value string, ruleParam string, isMax bool) bool {
	target, err := strconv.ParseFloat(strings.TrimSpace(ruleParam), 64)
	if err != nil {
		return false
	}
	if current, err := strconv.ParseFloat(value, 64); err == nil {
		if isMax {
			return current <= target
		}
		return current >= target
	}
	if isMax {
		return float64(len(value)) <= target
	}
	return float64(len(value)) >= target
}

func compareNumeric(value string, ruleParam string, operator string) bool {
	current, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	target, err := strconv.ParseFloat(strings.TrimSpace(ruleParam), 64)
	if err != nil {
		return false
	}
	switch operator {
	case ">":
		return current > target
	case "<":
		return current < target
	case ">=":
		return current >= target
	case "<=":
		return current <= target
	default:
		return false
	}
}

func resolveComparisonValue(data map[string]interface{}, ruleParam string) string {
	if value, ok := data[ruleParam]; ok {
		return fmt.Sprintf("%v", value)
	}
	return ruleParam
}

func parseRuleTime(value string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339,
		time.RFC3339Nano,
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func isEmptyValue(value interface{}) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) == ""
	case []string:
		return len(typed) == 0
	case []interface{}:
		return len(typed) == 0
	default:
		return fmt.Sprintf("%v", value) == ""
	}
}

func isValidIDCard(value string) bool {
	if !idCardRegex.MatchString(value) {
		return false
	}

	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	codes := []byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0

	for index := 0; index < 17; index++ {
		sum += int(value[index]-'0') * weights[index]
	}

	checkCode := codes[sum%11]
	last := value[17]
	if last >= 'a' && last <= 'z' {
		last -= 'a' - 'A'
	}
	return last == checkCode
}

// isValidIP 校验 IPv4 地址，确保每段在 0-255 范围内。
func isValidIP(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.To4() != nil
}

func (v *Validator) addError(field string, rule string, param string) {
	message := ""
	if value, ok := v.Message[field+"."+rule]; ok {
		message = value
	} else if value, ok := v.Message[field]; ok {
		message = value
	} else if value, ok := v.Message[rule]; ok {
		message = value
	}

	if message != "" && v.Lang != nil {
		translated := v.Lang.Get(message, map[string]interface{}{
			"field": field,
			"rule":  rule,
			"param": param,
		}, "")
		if translated != message {
			message = translated
		}
	}

	if message == "" {
		if v.Lang != nil {
			defaultKey := "validate.default." + rule
			translated := v.Lang.Get(defaultKey, map[string]interface{}{
				"field": field,
				"param": param,
			}, "")
			if translated != defaultKey {
				message = translated
			}
		}
		if message == "" {
			message = v.defaultMessage(field, rule, param)
		}
	}

	v.errors = append(v.errors, message)
}

func (v *Validator) defaultMessage(field string, rule string, param string) string {
	switch rule {
	case "required":
		return fmt.Sprintf("%s is required", field)
	case "email":
		return fmt.Sprintf("%s must be a valid email", field)
	case "max":
		return fmt.Sprintf("%s must not exceed %s", field, param)
	case "min":
		return fmt.Sprintf("%s must be at least %s", field, param)
	case "number":
		return fmt.Sprintf("%s must be a number", field)
	case "integer":
		return fmt.Sprintf("%s must be an integer", field)
	case "length":
		return fmt.Sprintf("%s length must be %s", field, param)
	default:
		return fmt.Sprintf("%s is not valid (%s)", field, rule)
	}
}
