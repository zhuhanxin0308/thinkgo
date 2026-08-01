package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
	"thinkgo/framework/console"
)

// TestRedactConfigDumpDataMasksNestedSensitiveValues 验证配置导出前会递归脱敏敏感字段。
func TestRedactConfigDumpDataMasksNestedSensitiveValues(t *testing.T) {
	source := map[string]interface{}{
		"database": map[string]interface{}{
			"username": "root",
			"password": "db-secret",
			"dsn":      "root:dsn-secret@tcp(localhost:3306)/app",
			"uri":      "mongodb://root:uri-secret@localhost/app",
			"params": []interface{}{
				map[string]interface{}{"api_token": "token-secret"},
				"safe-value",
			},
		},
		"Authorization": "Bearer secret",
		"database_url":  "postgres://root:url-secret@localhost/app",
		"credential":    "credential-secret",
		"passphrase":    "passphrase-secret",
		"db_pass":       "db-pass-secret",
		"signing-key":   "signing-secret",
		"normal":        "visible",
	}

	redacted, err := redactConfigDumpData("", source)
	if err != nil {
		t.Fatalf("归一化配置失败: %v", err)
	}
	jsonBytes, err := json.Marshal(redacted)
	if err != nil {
		t.Fatalf("脱敏后的配置应可序列化: %v", err)
	}
	output := string(jsonBytes)

	for _, secret := range []string{
		"db-secret", "dsn-secret", "uri-secret", "token-secret", "Bearer secret",
		"url-secret", "credential-secret", "passphrase-secret",
		"db-pass-secret", "signing-secret",
	} {
		if strings.Contains(output, secret) {
			t.Fatalf("配置导出不应包含敏感值 %q，实际为 %s", secret, output)
		}
	}
	if !strings.Contains(output, "[REDACTED]") {
		t.Fatalf("配置导出应包含脱敏占位符，实际为 %s", output)
	}
	if !strings.Contains(output, "visible") || !strings.Contains(output, "root") {
		t.Fatalf("配置导出应保留非敏感字段，实际为 %s", output)
	}
	if source["Authorization"] != "Bearer secret" {
		t.Fatalf("脱敏过程不应修改原始配置，实际为 %#v", source)
	}
}

// TestConfigDumpRedactsDirectSensitiveKey 验证按点路径查询单个敏感值时，
// 字段名上下文不会丢失并导致明文泄露。
func TestConfigDumpRedactsDirectSensitiveKey(t *testing.T) {
	appConfig := config.NewConfig()
	appConfig.Set("database.password", "direct-secret")
	cmd := &ConfigDump{Command: console.Command{App: configDumpTestApp(t, appConfig)}}
	cmd.Configure()
	input := console.NewInput("database.password")
	if err := input.Parse(cmd.GetArgumentDefinitions(), cmd.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析配置键失败: %v", err)
	}
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	if err := cmd.Execute(input, output); err != nil {
		t.Fatalf("导出单个敏感配置失败: %v", err)
	}
	if strings.Contains(stdout.String(), "direct-secret") || !strings.Contains(stdout.String(), configDumpRedactedValue) {
		t.Fatalf("单个敏感配置未正确脱敏: %q", stdout.String())
	}
}

// TestConfigDumpRedactsTypedStringMaps 验证类型化字符串映射中的凭据也会递归脱敏。
func TestConfigDumpRedactsTypedStringMaps(t *testing.T) {
	appConfig := config.NewConfig()
	appConfig.Set("service", map[string]string{
		"name":     "visible-service",
		"password": "typed-secret",
	})
	cmd := &ConfigDump{Command: console.Command{App: configDumpTestApp(t, appConfig)}}
	stdout := &bytes.Buffer{}
	if err := cmd.Execute(console.NewInput(), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("导出类型化配置失败: %v", err)
	}
	if strings.Contains(stdout.String(), "typed-secret") || !strings.Contains(stdout.String(), "visible-service") {
		t.Fatalf("类型化字符串映射脱敏结果错误: %q", stdout.String())
	}
}

// TestConfigDumpPropagatesDependencyAndSerializationErrors 验证缺失执行依赖及
// 不支持的配置类型会返回错误，且不会产生部分 JSON 输出。
func TestConfigDumpPropagatesDependencyAndSerializationErrors(t *testing.T) {
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := (&ConfigDump{}).Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := (&ConfigDump{Command: console.Command{App: &framework.App{}}}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("缺少配置管理器时应返回错误")
	}
	appConfig := config.NewConfig()
	appConfig.Set("unsupported", make(chan int))
	stdout := &bytes.Buffer{}
	command := &ConfigDump{Command: console.Command{App: configDumpTestApp(t, appConfig)}}
	if err := command.Execute(console.NewInput(), console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)); err == nil {
		t.Fatal("不支持 JSON 序列化的配置应返回错误")
	}
	if stdout.Len() != 0 {
		t.Fatalf("序列化失败不应输出部分数据: %q", stdout.String())
	}
	if err := command.Execute(nil, output); err == nil {
		t.Fatal("空输入应返回错误")
	}
	if err := command.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}
}

// configDumpTestApp 将测试配置绑定到已初始化控制台应用的配置服务。
func configDumpTestApp(t *testing.T, configuration *config.Config) *framework.App {
	t.Helper()
	return func() *framework.App {
		app := buildConsoleTestApp(t, t.TempDir())
		app.Instance(string(framework.ServiceConfig), configuration)
		return app
	}()
}
