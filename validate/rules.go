package validate

import (
	"encoding/json"
	"math"
	"math/big"
	"net"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxNumericValueBytes = 1024

var (
	alphaRegex       = regexp.MustCompile(`^[a-zA-Z]+$`)
	alphaNumRegex    = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	alphaDashRegex   = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	chsRegex         = regexp.MustCompile(`^[\p{Han}]+$`)
	chsAlphaRegex    = regexp.MustCompile(`^[\p{Han}a-zA-Z]+$`)
	chsAlphaNumRegex = regexp.MustCompile(`^[\p{Han}a-zA-Z0-9]+$`)
	chsDashRegex     = regexp.MustCompile(`^[\p{Han}a-zA-Z0-9_-]+$`)
	mobileRegex      = regexp.MustCompile(`^1[3-9]\d{9}$`)
	idCardRegex      = regexp.MustCompile(`^\d{17}[\dXx]$`)
	integerRegex     = regexp.MustCompile(`^[+-]?\d+$`)
	decimalRegex     = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)
)

func evaluateRule(data map[string]interface{}, field string, value interface{}, exists bool, rule compiledRule, location *time.Location, current time.Time) bool {
	switch rule.name {
	case "required":
		return exists && !isEmptyValue(value)
	case "number", "float":
		_, valid := numericValue(value)
		return valid
	case "integer":
		return isIntegerValue(value)
	case "boolean":
		return isBooleanValue(value)
	case "email":
		text, valid := scalarText(value)
		return valid && isValidEmail(text)
	case "array":
		return isCollectionValue(value)
	case "accepted":
		return isAcceptedValue(value)
	case "date":
		text, valid := scalarText(value)
		if !valid {
			return false
		}
		_, err := parseRuleTimeInLocation(text, location)
		return err == nil
	case "alpha":
		return matchTextRule(value, alphaRegex)
	case "alphaNum":
		return matchTextRule(value, alphaNumRegex)
	case "alphaDash":
		return matchTextRule(value, alphaDashRegex)
	case "chs":
		return matchTextRule(value, chsRegex)
	case "chsAlpha":
		return matchTextRule(value, chsAlphaRegex)
	case "chsAlphaNum":
		return matchTextRule(value, chsAlphaNumRegex)
	case "chsDash":
		return matchTextRule(value, chsDashRegex)
	case "ip":
		text, valid := scalarText(value)
		return valid && isValidIP(text)
	case "url":
		text, valid := scalarText(value)
		return valid && isValidURL(text)
	case "in":
		text, valid := scalarText(value)
		return valid && containsValue(rule.parameters, text)
	case "notIn":
		text, valid := scalarText(value)
		return valid && !containsValue(rule.parameters, text)
	case "between":
		return compareRange(value, rule.parameters, true)
	case "notBetween":
		return compareRange(value, rule.parameters, false)
	case "length":
		return validateLength(value, rule.parameters)
	case "max":
		return compareBoundary(value, rule.param, true)
	case "min":
		return compareBoundary(value, rule.param, false)
	case "eq":
		text, valid := scalarText(value)
		return valid && text == rule.param
	case "gt":
		return compareNumeric(value, rule.param, 1)
	case "lt":
		return compareNumeric(value, rule.param, -1)
	case "egt":
		return compareNumeric(value, rule.param, 0, 1)
	case "elt":
		return compareNumeric(value, rule.param, -1, 0)
	case "regex":
		text, valid := scalarText(value)
		return valid && rule.pattern != nil && rule.pattern.MatchString(text)
	case "confirm":
		targetField := field + "_confirm"
		if rule.hasParam && rule.param != "" {
			targetField = rule.param
		}
		targetValue, targetExists := data[targetField]
		return targetExists && reflect.DeepEqual(value, targetValue)
	case "different":
		targetValue, targetExists := data[rule.param]
		return targetExists && !reflect.DeepEqual(value, targetValue)
	case "mobile":
		return matchTextRule(value, mobileRegex)
	case "dateFormat":
		text, valid := scalarText(value)
		if !valid {
			return false
		}
		_, err := time.Parse(rule.param, text)
		return err == nil
	case "after":
		return compareDateValue(data, value, rule.param, true, location)
	case "before":
		return compareDateValue(data, value, rule.param, false, location)
	case "requireIf":
		relatedValue, relatedExists := data[rule.parameters[0]]
		if !relatedExists {
			return true
		}
		text, comparable := scalarText(relatedValue)
		if !comparable || text != rule.parameters[1] {
			return true
		}
		return exists && !isEmptyValue(value)
	case "requireWith":
		for _, relatedField := range rule.parameters {
			if relatedValue, relatedExists := data[relatedField]; relatedExists && !isEmptyValue(relatedValue) {
				return exists && !isEmptyValue(value)
			}
		}
		return true
	case "idCard":
		text, valid := scalarText(value)
		return valid && isValidIDCardAt(text, location, current)
	default:
		return false
	}
}

func scalarText(value interface{}) (string, bool) {
	if value == nil {
		return "", false
	}
	if number, ok := value.(json.Number); ok {
		return number.String(), true
	}
	if typed, ok := value.(time.Time); ok {
		return typed.Format(time.RFC3339Nano), true
	}

	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return "", false
	}
	switch reflected.Kind() {
	case reflect.String:
		return reflected.String(), true
	case reflect.Bool:
		return strconv.FormatBool(reflected.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(reflected.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(reflected.Float(), 'g', -1, reflected.Type().Bits()), true
	default:
		return "", false
	}
}

func indirectValidationValue(value reflect.Value) (reflect.Value, bool) {
	for depth := 0; depth < 32; depth++ {
		if !value.IsValid() {
			return reflect.Value{}, false
		}
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if value.IsNil() {
				return reflect.Value{}, false
			}
			value = value.Elem()
		default:
			return value, true
		}
	}
	return reflect.Value{}, false
}

func numericValue(value interface{}) (*big.Rat, bool) {
	if value == nil {
		return nil, false
	}
	if number, ok := value.(json.Number); ok {
		return parseNumericString(number.String())
	}
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return nil, false
	}
	var text string
	switch reflected.Kind() {
	case reflect.String:
		text = reflected.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		text = strconv.FormatInt(reflected.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		text = strconv.FormatUint(reflected.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) {
			return nil, false
		}
		text = strconv.FormatFloat(floating, 'g', -1, reflected.Type().Bits())
	default:
		return nil, false
	}
	return parseNumericString(text)
}

func parseNumericString(value string) (*big.Rat, bool) {
	if value == "" || len(value) > maxNumericValueBytes || strings.TrimSpace(value) != value || !decimalRegex.MatchString(value) {
		return nil, false
	}
	parsed, valid := new(big.Rat).SetString(value)
	return parsed, valid
}

func isIntegerValue(value interface{}) bool {
	if value == nil {
		return false
	}
	if number, ok := value.(json.Number); ok {
		return isIntegerText(number.String())
	}
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		return !math.IsNaN(floating) && !math.IsInf(floating, 0) && math.Trunc(floating) == floating
	case reflect.String:
		return isIntegerText(reflected.String())
	default:
		return false
	}
}

func isIntegerText(value string) bool {
	return len(value) <= maxNumericValueBytes && integerRegex.MatchString(value)
}

func isBooleanValue(value interface{}) bool {
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return false
	}
	if reflected.Kind() == reflect.Bool {
		return true
	}
	if reflected.Kind() != reflect.String {
		return false
	}
	_, err := strconv.ParseBool(reflected.String())
	return err == nil
}

func isAcceptedValue(value interface{}) bool {
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return false
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() == 1
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflected.Uint() == 1
	case reflect.String:
		text := strings.ToLower(reflected.String())
		return text == "yes" || text == "on" || text == "1" || text == "true"
	default:
		return false
	}
}

func isCollectionValue(value interface{}) bool {
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return false
	}
	return reflected.Kind() == reflect.Array || reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Map
}

func valueLength(value interface{}) (int, bool) {
	reflected, valid := indirectValidationValue(reflect.ValueOf(value))
	if !valid {
		return 0, false
	}
	switch reflected.Kind() {
	case reflect.String:
		return utf8.RuneCountInString(reflected.String()), true
	case reflect.Array, reflect.Slice, reflect.Map:
		return reflected.Len(), true
	default:
		return 0, false
	}
}

func isEmptyValue(value interface{}) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && (reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer) {
		if reflected.IsNil() {
			return true
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() {
		return true
	}
	switch reflected.Kind() {
	case reflect.String:
		return strings.TrimSpace(reflected.String()) == ""
	case reflect.Array, reflect.Slice, reflect.Map:
		return reflected.Len() == 0
	default:
		return false
	}
}

func matchTextRule(value interface{}, pattern *regexp.Regexp) bool {
	text, valid := scalarText(value)
	return valid && pattern.MatchString(text)
}

func containsValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func compareRange(value interface{}, parameters []string, inclusive bool) bool {
	current, valid := numericValue(value)
	if !valid {
		return false
	}
	minimum, _ := parseNumericString(parameters[0])
	maximum, _ := parseNumericString(parameters[1])
	inside := current.Cmp(minimum) >= 0 && current.Cmp(maximum) <= 0
	if inclusive {
		return inside
	}
	return !inside
}

func validateLength(value interface{}, parameters []string) bool {
	length, valid := valueLength(value)
	if !valid {
		return false
	}
	minimum, _ := strconv.Atoi(parameters[0])
	if len(parameters) == 1 {
		return length == minimum
	}
	maximum, _ := strconv.Atoi(parameters[1])
	return length >= minimum && length <= maximum
}

func compareBoundary(value interface{}, parameter string, maximum bool) bool {
	target, _ := parseNumericString(parameter)
	if current, numeric := numericValue(value); numeric {
		if maximum {
			return current.Cmp(target) <= 0
		}
		return current.Cmp(target) >= 0
	}
	length, valid := valueLength(value)
	if !valid || !target.IsInt() {
		return false
	}
	boundary := target.Num()
	currentLength := big.NewInt(int64(length))
	if maximum {
		return currentLength.Cmp(boundary) <= 0
	}
	return currentLength.Cmp(boundary) >= 0
}

func compareNumeric(value interface{}, parameter string, allowedComparisons ...int) bool {
	current, valid := numericValue(value)
	if !valid {
		return false
	}
	target, _ := parseNumericString(parameter)
	comparison := current.Cmp(target)
	for _, allowed := range allowedComparisons {
		if comparison == allowed {
			return true
		}
	}
	return false
}

func compareDateValue(data map[string]interface{}, value interface{}, parameter string, after bool, location *time.Location) bool {
	text, valid := scalarText(value)
	if !valid {
		return false
	}
	current, err := parseRuleTimeInLocation(text, location)
	if err != nil {
		return false
	}
	targetText := parameter
	if targetValue, exists := data[parameter]; exists {
		targetText, valid = scalarText(targetValue)
		if !valid {
			return false
		}
	}
	target, err := parseRuleTimeInLocation(targetText, location)
	if err != nil {
		return false
	}
	if after {
		return current.After(target)
	}
	return current.Before(target)
}

// parseRuleTimeInLocation 按应用时区解析不带偏移的业务日期时间。
func parseRuleTimeInLocation(value string, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.UTC
	}
	layouts := [...]string{
		"2006-01-02 15:04:05",
		"2006-01-02",
		time.RFC3339Nano,
	}
	var lastError error
	for _, layout := range layouts {
		var parsed time.Time
		var err error
		if layout == time.RFC3339Nano {
			parsed, err = time.Parse(layout, value)
		} else {
			parsed, err = time.ParseInLocation(layout, value, location)
		}
		if err == nil {
			return parsed, nil
		}
		lastError = err
	}
	return time.Time{}, lastError
}

func isValidEmail(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value
}

func isValidURL(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ftp", "rtsp", "mms":
		return true
	default:
		return false
	}
}

func isValidIP(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	parsed := net.ParseIP(value)
	return parsed != nil && parsed.To4() != nil
}

// isValidIDCardAt 按指定时区和当前时间校验身份证生日不能晚于当前日期。
func isValidIDCardAt(value string, location *time.Location, current time.Time) bool {
	if !idCardRegex.MatchString(value) {
		return false
	}
	if value[:6] == "000000" || value[14:17] == "000" {
		return false
	}
	if location == nil {
		location = time.UTC
	}
	birthday, err := time.ParseInLocation("20060102", value[6:14], location)
	if err != nil || birthday.After(current.In(location)) {
		return false
	}
	weights := [...]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	codes := [...]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
	sum := 0
	for index := range weights {
		sum += int(value[index]-'0') * weights[index]
	}
	last := value[17]
	if last >= 'a' && last <= 'z' {
		last -= 'a' - 'A'
	}
	return last == codes[sum%11]
}
