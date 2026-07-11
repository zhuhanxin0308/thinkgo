package command

import (
	"encoding/json"
	"strings"

	"thinkgo/framework/console"
)

const configDumpRedactedValue = "[REDACTED]"

// ConfigDump command
type ConfigDump struct {
	console.Command
}

func (c *ConfigDump) Configure() {
	c.Signature = "config:dump"
	c.Description = "Dump configuration values"
}

func (c *ConfigDump) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)

	var data interface{}
	if name != "" {
		data = c.App.Config.Get(name)
	} else {
		data = c.App.Config.Get("")
	}

	data = redactConfigDumpData(data)
	jsonBytes, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		output.Error("Failed to marshal config: " + err.Error())
		return
	}

	output.Writeln(string(jsonBytes))
}

// redactConfigDumpData 递归复制配置并脱敏敏感字段，避免 config:dump 泄露密钥。
func redactConfigDumpData(value interface{}) interface{} {
	return redactConfigDumpValue("", value)
}

// redactConfigDumpValue 按字段名判断是否脱敏，普通值保持原样。
func redactConfigDumpValue(key string, value interface{}) interface{} {
	if isConfigDumpSensitiveKey(key) {
		return configDumpRedactedValue
	}

	switch typed := value.(type) {
	case map[string]interface{}:
		redacted := make(map[string]interface{}, len(typed))
		for innerKey, innerValue := range typed {
			redacted[innerKey] = redactConfigDumpValue(innerKey, innerValue)
		}
		return redacted
	case []interface{}:
		redacted := make([]interface{}, len(typed))
		for index, item := range typed {
			redacted[index] = redactConfigDumpValue("", item)
		}
		return redacted
	default:
		return value
	}
}

// isConfigDumpSensitiveKey 识别常见凭据字段，使用包含匹配覆盖 api_token 等组合名称。
func isConfigDumpSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}

	sensitiveFragments := []string{
		"authorization",
		"cookie",
		"password",
		"passwd",
		"private_key",
		"secret",
		"session",
		"token",
		"api_key",
		"apikey",
		"access_key",
	}
	for _, fragment := range sensitiveFragments {
		if key == fragment || strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}
