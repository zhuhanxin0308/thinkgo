package command

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

const configDumpRedactedValue = "[REDACTED]"

var configDumpKeyNormalizer = strings.NewReplacer("-", "_", ".", "_")

// ConfigDump command
type ConfigDump struct {
	console.Command
}

func (c *ConfigDump) Configure() {
	c.Signature = "config:dump"
	c.Description = "Dump configuration values"
	c.AddArgument("name", "Optional dot-separated configuration key", false)
	addApplicationSelectionOption(&c.Command)
}

func (c *ConfigDump) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	applications, err := compiledApplicationsForCommand(c.App, input.GetOption("app"), false)
	if err != nil {
		return err
	}
	configuration, err := resolveApplicationConfig(applications[0].application)
	if err != nil {
		return fmt.Errorf("应用配置不可用: %w", err)
	}
	name := input.GetArgument(0)

	var data interface{}
	if name != "" {
		data = configuration.Get(name)
	} else {
		data = configuration.Get("")
	}

	data, err = redactConfigDumpData(name, data)
	if err != nil {
		return fmt.Errorf("failed to normalize config: %w", err)
	}
	jsonBytes, err := json.MarshalIndent(data, "", "    ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	output.Writeln(string(jsonBytes))
	return nil
}

// redactConfigDumpData 先按 JSON 语义归一化强类型映射、切片和结构体，
// 再递归脱敏；UseNumber 避免大整数在归一化过程中损失精度。
func redactConfigDumpData(key string, value interface{}) (interface{}, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized interface{}
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	// 按 Config.Get 使用的层级判断，禁止组合别名跨越两个不同的键命中。
	for segment := range strings.SplitSeq(key, ".") {
		if isConfigDumpSensitiveKey(segment) {
			return configDumpRedactedValue, nil
		}
	}
	return redactConfigDumpValue("", normalized), nil
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
	normalizedKey := configDumpKeyNormalizer.Replace(key)

	sensitiveFragments := []string{
		"authorization",
		"cookie",
		"credential",
		"password",
		"passwd",
		"passphrase",
		"private_key",
		"privatekey",
		"signing_key",
		"encryption_key",
		"client_secret",
		"secret",
		"session",
		"token",
		"api_key",
		"apikey",
		"access_key",
		"accesskey",
		"connection_string",
		"headers",
	}
	for _, fragment := range sensitiveFragments {
		if normalizedKey == fragment || strings.Contains(normalizedKey, fragment) {
			return true
		}
	}
	boundedKey := "_" + normalizedKey + "_"
	for _, alias := range []string{"_sign_key_", "_signkey_", "_app_key_", "_appkey_", "_mch_key_", "_mchkey_"} {
		if strings.Contains(boundedKey, alias) {
			return true
		}
	}
	// 短格式别名按完整片段匹配，避免误遮蔽 author、duration 等字段。
	for part := range strings.SplitSeq(normalizedKey, "_") {
		switch part {
		case "auth", "pwd", "pass", "salt", "cert", "dsn", "uri", "url":
			return true
		}
	}
	return false
}
