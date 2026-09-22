package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrConfigEnvironmentType 表示环境变量值无法转换为默认配置声明的类型。
	ErrConfigEnvironmentType = errors.New("config environment value has invalid type")
	// ErrConfigEnvironmentCollision 表示多个配置路径映射到了同一个环境变量名。
	ErrConfigEnvironmentCollision = errors.New("config environment path collision")
)

// ConfigEnvironmentValueError 描述环境变量与已声明配置类型不兼容的结构化上下文。
// 错误只记录变量名、配置路径和期望类型，绝不保存可能包含凭据的原始值。
type ConfigEnvironmentValueError struct {
	EnvironmentName string
	Path            string
	Expected        string
}

// Error 返回适合日志和 CLI 展示的脱敏错误。
func (err *ConfigEnvironmentValueError) Error() string {
	if err == nil {
		return ErrConfigEnvironmentType.Error()
	}
	location := err.Path
	if err.EnvironmentName != "" {
		location = fmt.Sprintf("%s maps to %s", err.EnvironmentName, err.Path)
	}
	if err.Expected == "" {
		return fmt.Sprintf("%s: %s", ErrConfigEnvironmentType, location)
	}
	return fmt.Sprintf("%s: %s; expected %s", ErrConfigEnvironmentType, location, err.Expected)
}

// Unwrap 保留 errors.Is 对 ErrConfigEnvironmentType 的兼容语义。
func (err *ConfigEnvironmentValueError) Unwrap() error {
	return ErrConfigEnvironmentType
}

// EnvironmentSource 提供统一环境变量查询能力，屏蔽系统环境与文件来源差异。
type EnvironmentSource interface {
	Lookup(name string) (string, bool)
}

// configEnvironmentBinding 保存环境键与配置叶子节点之间的确定映射。
type configEnvironmentBinding struct {
	path      string
	container map[string]interface{}
	key       string
	prototype interface{}
}

// ApplyEnvironment 按“完整配置点路径转大写下划线”的规则原子覆盖已声明配置。
func (c *Config) ApplyEnvironment(source EnvironmentSource) error {
	if c == nil || isNilEnvironmentSource(source) {
		return nil
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.ensureInitializedLocked()

	working := deepCopyMap(c.config)
	bindings := make(map[string][]configEnvironmentBinding)
	collectConfigEnvironmentBindings(working, nil, bindings)

	environmentNames := make([]string, 0, len(bindings))
	for name := range bindings {
		environmentNames = append(environmentNames, name)
	}
	sort.Strings(environmentNames)

	changed := false
	for _, environmentName := range environmentNames {
		raw, exists := source.Lookup(environmentName)
		if !exists {
			continue
		}
		candidates := bindings[environmentName]
		if len(candidates) != 1 {
			paths := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				paths = append(paths, candidate.path)
			}
			sort.Strings(paths)
			return fmt.Errorf("%w: %s maps to %s", ErrConfigEnvironmentCollision, environmentName, strings.Join(paths, ", "))
		}

		binding := candidates[0]
		value, err := parseConfigEnvironmentValue(binding.path, raw, binding.prototype)
		if err != nil {
			return attachConfigEnvironmentName(environmentName, err)
		}
		binding.container[binding.key] = value
		changed = true
	}

	if changed {
		c.config = working
		c.invalidateLookupCacheLocked()
	}
	return nil
}

// collectConfigEnvironmentBindings 绑定声明过的叶子节点；空对象作为显式动态配置节点可整体覆盖。
func collectConfigEnvironmentBindings(current map[string]interface{}, prefix []string, bindings map[string][]configEnvironmentBinding) {
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := current[key]
		pathParts := appendConfigPath(prefix, key)
		if nested, ok := value.(map[string]interface{}); ok {
			if len(nested) == 0 && len(prefix) > 0 {
				addConfigEnvironmentBinding(bindings, pathParts, current, key, value)
			}
			collectConfigEnvironmentBindings(nested, pathParts, bindings)
			continue
		}
		addConfigEnvironmentBinding(bindings, pathParts, current, key, value)
	}
}

func addConfigEnvironmentBinding(bindings map[string][]configEnvironmentBinding, pathParts []string, container map[string]interface{}, key string, prototype interface{}) {
	path := strings.Join(pathParts, ".")
	environmentName := strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
	bindings[environmentName] = append(bindings[environmentName], configEnvironmentBinding{
		path: path, container: container, key: key, prototype: prototype,
	})
}

func appendConfigPath(prefix []string, key string) []string {
	result := make([]string, len(prefix)+1)
	copy(result, prefix)
	result[len(prefix)] = key
	return result
}

// parseConfigEnvironmentValue 根据默认值类型解析环境文本，避免模块各自实现不一致的转换。
func parseConfigEnvironmentValue(path, raw string, prototype interface{}) (interface{}, error) {
	switch typedPrototype := prototype.(type) {
	case string:
		return raw, nil
	case bool:
		return parseConfigEnvironmentBool(path, raw)
	case json.Number:
		return parseConfigEnvironmentNumber(path, raw, typedPrototype)
	case nil:
		return parseConfigEnvironmentNil(raw), nil
	case []interface{}:
		return parseConfigEnvironmentJSONArray(path, raw)
	case map[string]interface{}:
		return parseConfigEnvironmentJSONObject(path, raw)
	}

	typeOf := reflect.TypeOf(prototype)
	if typeOf == nil {
		return raw, nil
	}
	switch typeOf.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, typeOf.Bits())
		if err != nil {
			return nil, configEnvironmentTypeError(path, typeOf.String())
		}
		result := reflect.New(typeOf).Elem()
		result.SetInt(value)
		return result.Interface(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value, err := strconv.ParseUint(strings.TrimSpace(raw), 10, typeOf.Bits())
		if err != nil {
			return nil, configEnvironmentTypeError(path, typeOf.String())
		}
		result := reflect.New(typeOf).Elem()
		result.SetUint(value)
		return result.Interface(), nil
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), typeOf.Bits())
		if err != nil {
			return nil, configEnvironmentTypeError(path, typeOf.String())
		}
		result := reflect.New(typeOf).Elem()
		result.SetFloat(value)
		return result.Interface(), nil
	case reflect.Slice, reflect.Array, reflect.Map:
		return parseTypedConfigEnvironmentCollection(path, raw, typeOf)
	default:
		return nil, configEnvironmentTypeError(path, typeOf.String())
	}
}

func parseConfigEnvironmentJSONObject(path, raw string) (map[string]interface{}, error) {
	decoded, err := decodeSingleEnvironmentJSON(raw)
	if err != nil {
		return nil, configEnvironmentTypeError(path, "JSON object")
	}
	values, ok := decoded.(map[string]interface{})
	if !ok {
		return nil, configEnvironmentTypeError(path, "JSON object")
	}
	normalized, err := normalizeConfigInputValue(values, 0, "$"+path)
	if err != nil {
		return nil, configEnvironmentTypeError(path, "JSON object")
	}
	return normalized.(map[string]interface{}), nil
}

func parseConfigEnvironmentBool(path, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "on":
		return true, nil
	case "false", "0", "off":
		return false, nil
	default:
		return false, configEnvironmentTypeError(path, "boolean")
	}
}

func parseConfigEnvironmentNumber(path, raw string, prototype json.Number) (json.Number, error) {
	decoded, err := decodeSingleEnvironmentJSON(raw)
	if err != nil {
		return "", configEnvironmentTypeError(path, "number")
	}
	number, ok := decoded.(json.Number)
	if !ok {
		return "", configEnvironmentTypeError(path, "number")
	}
	if _, prototypeErr := prototype.Int64(); prototypeErr == nil {
		if _, valueErr := number.Int64(); valueErr != nil {
			return "", configEnvironmentTypeError(path, "integer")
		}
	}
	return number, nil
}

func parseConfigEnvironmentNil(raw string) interface{} {
	if strings.EqualFold(strings.TrimSpace(raw), "null") {
		return nil
	}
	return raw
}

func parseConfigEnvironmentJSONArray(path, raw string) ([]interface{}, error) {
	decoded, err := decodeSingleEnvironmentJSON(raw)
	if err != nil {
		return nil, configEnvironmentTypeError(path, "JSON array")
	}
	values, ok := decoded.([]interface{})
	if !ok {
		return nil, configEnvironmentTypeError(path, "JSON array")
	}
	normalized, err := normalizeConfigInputValue(values, 0, "$"+path)
	if err != nil {
		return nil, configEnvironmentTypeError(path, "JSON array")
	}
	return normalized.([]interface{}), nil
}

func parseTypedConfigEnvironmentCollection(path, raw string, targetType reflect.Type) (interface{}, error) {
	target := reflect.New(targetType)
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target.Interface()); err != nil {
		return nil, configEnvironmentTypeError(path, targetType.String())
	}
	if err := ensureJSONDecoderEOF(decoder); err != nil {
		return nil, configEnvironmentTypeError(path, targetType.String())
	}
	return target.Elem().Interface(), nil
}

func decodeSingleEnvironmentJSON(raw string) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var decoded interface{}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := ensureJSONDecoderEOF(decoder); err != nil {
		return nil, err
	}
	return decoded, nil
}

func ensureJSONDecoderEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("environment JSON contains trailing data")
		}
		return err
	}
	return nil
}

func configEnvironmentTypeError(path, expected string) error {
	return &ConfigEnvironmentValueError{Path: path, Expected: expected}
}

// attachConfigEnvironmentName 将确定的环境变量名绑定到类型错误，保持错误可检查且不引入原值。
func attachConfigEnvironmentName(environmentName string, err error) error {
	var valueErr *ConfigEnvironmentValueError
	if !errors.As(err, &valueErr) {
		return fmt.Errorf("%s: %w", environmentName, err)
	}
	bound := *valueErr
	bound.EnvironmentName = environmentName
	return &bound
}

func isNilEnvironmentSource(source EnvironmentSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
