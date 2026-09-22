package config

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// environmentStub 为配置合并测试提供确定的环境变量来源。
type environmentStub map[string]string

// Lookup 按统一大写环境键返回测试值。
func (environment environmentStub) Lookup(name string) (string, bool) {
	value, exists := environment[name]
	return value, exists
}

// TestApplyEnvironmentOverridesDeclaredConfigPaths 验证所有已声明配置叶子都遵循统一点路径映射。
func TestApplyEnvironmentOverridesDeclaredConfigPaths(t *testing.T) {
	configuration := NewConfig()
	for path, value := range map[string]interface{}{
		"app.server.port":                           json.Number("8081"),
		"app.app_debug":                             false,
		"app.server.allowed_hosts":                  []interface{}{"localhost"},
		"database.connections.mysql.username":       "default-user",
		"database.connections.mysql.password":       "default-password",
		"database.connections.mysql.max_open_conns": json.Number("10"),
		"console.user":                              nil,
	} {
		if err := configuration.Set(path, value); err != nil {
			t.Fatalf("写入测试默认配置 %s 失败: %v", path, err)
		}
	}

	err := configuration.ApplyEnvironment(environmentStub{
		"APP_SERVER_PORT":                           "9090",
		"APP_APP_DEBUG":                             "true",
		"APP_SERVER_ALLOWED_HOSTS":                  `["api.example.com","admin.example.com"]`,
		"DATABASE_CONNECTIONS_MYSQL_USERNAME":       "deploy-user",
		"DATABASE_CONNECTIONS_MYSQL_PASSWORD":       "",
		"DATABASE_CONNECTIONS_MYSQL_MAX_OPEN_CONNS": "32",
		"CONSOLE_USER":                              "operator",
	})
	if err != nil {
		t.Fatalf("统一环境变量覆盖失败: %v", err)
	}

	if got := configuration.GetInt("app.server.port"); got != 9090 {
		t.Fatalf("端口覆盖错误，实际为 %d", got)
	}
	if got := configuration.GetBool("app.app_debug"); !got {
		t.Fatal("布尔配置未由环境变量开启")
	}
	if got := configuration.GetString("database.connections.mysql.username"); got != "deploy-user" {
		t.Fatalf("数据库用户名覆盖错误，实际为 %q", got)
	}
	if got := configuration.GetString("database.connections.mysql.password", "fallback"); got != "" {
		t.Fatalf("显式空密码必须覆盖默认值，实际为 %q", got)
	}
	if got := configuration.GetInt("database.connections.mysql.max_open_conns"); got != 32 {
		t.Fatalf("连接池整数覆盖错误，实际为 %d", got)
	}
	if got := configuration.GetString("console.user"); got != "operator" {
		t.Fatalf("null 默认值未接受字符串环境值，实际为 %q", got)
	}
	wantHosts := []interface{}{"api.example.com", "admin.example.com"}
	if got := configuration.Get("app.server.allowed_hosts"); !reflect.DeepEqual(got, wantHosts) {
		t.Fatalf("数组配置覆盖错误，want=%#v got=%#v", wantHosts, got)
	}
}

// TestApplyEnvironmentIsAtomicOnTypeError 验证任一环境值非法时不会提交其它已解析覆盖。
func TestApplyEnvironmentIsAtomicOnTypeError(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("app.app_debug", false); err != nil {
		t.Fatalf("写入布尔默认值失败: %v", err)
	}
	if err := configuration.Set("app.server.port", json.Number("8081")); err != nil {
		t.Fatalf("写入端口默认值失败: %v", err)
	}

	err := configuration.ApplyEnvironment(environmentStub{
		"APP_APP_DEBUG":   "true",
		"APP_SERVER_PORT": "not-a-number",
	})
	if !errors.Is(err, ErrConfigEnvironmentType) {
		t.Fatalf("非法整数应返回 ErrConfigEnvironmentType，实际为 %v", err)
	}
	if configuration.GetBool("app.app_debug") || configuration.GetInt("app.server.port") != 8081 {
		t.Fatalf("失败的环境合并不得修改配置: debug=%v port=%d", configuration.GetBool("app.app_debug"), configuration.GetInt("app.server.port"))
	}
}

// TestApplyEnvironmentTypeErrorRedactsRawValueAndRetainsContext 验证非法环境值
// 不会进入错误文本，同时保留环境变量名、配置路径和期望类型供运维定位。
func TestApplyEnvironmentTypeErrorRedactsRawValueAndRetainsContext(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("app.server.allowed_hosts", []interface{}{"localhost"}); err != nil {
		t.Fatalf("写入数组默认配置失败: %v", err)
	}

	const rawSecret = `not-json-with-secret-token-734912`
	err := configuration.ApplyEnvironment(environmentStub{
		"APP_SERVER_ALLOWED_HOSTS": rawSecret,
	})
	if !errors.Is(err, ErrConfigEnvironmentType) {
		t.Fatalf("非法 JSON 数组应返回 ErrConfigEnvironmentType，实际为 %v", err)
	}
	if strings.Contains(err.Error(), rawSecret) || strings.Contains(err.Error(), "secret-token-734912") {
		t.Fatalf("配置类型错误不得泄露环境变量原值，实际为 %q", err.Error())
	}

	var valueErr *ConfigEnvironmentValueError
	if !errors.As(err, &valueErr) {
		t.Fatalf("配置类型错误应保留结构化上下文，实际类型为 %T", err)
	}
	if valueErr.EnvironmentName != "APP_SERVER_ALLOWED_HOSTS" ||
		valueErr.Path != "app.server.allowed_hosts" || valueErr.Expected != "JSON array" {
		t.Fatalf("配置类型错误上下文不完整: %#v", valueErr)
	}
	for _, expected := range []string{"APP_SERVER_ALLOWED_HOSTS", "app.server.allowed_hosts", "JSON array"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("配置类型错误应包含 %q，实际为 %q", expected, err.Error())
		}
	}
}

// TestApplyEnvironmentRejectsAmbiguousFlattenedPath 验证下划线扁平化冲突不会静默覆盖错误路径。
func TestApplyEnvironmentRejectsAmbiguousFlattenedPath(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("feature.rate_limit", "flat"); err != nil {
		t.Fatalf("写入扁平配置失败: %v", err)
	}
	if err := configuration.Set("feature.rate.limit", "nested"); err != nil {
		t.Fatalf("写入嵌套配置失败: %v", err)
	}

	err := configuration.ApplyEnvironment(environmentStub{"FEATURE_RATE_LIMIT": "override"})
	if !errors.Is(err, ErrConfigEnvironmentCollision) {
		t.Fatalf("歧义环境键应返回 ErrConfigEnvironmentCollision，实际为 %v", err)
	}
	if configuration.GetString("feature.rate_limit") != "flat" || configuration.GetString("feature.rate.limit") != "nested" {
		t.Fatal("歧义环境键不得修改任一候选配置")
	}
}

// TestApplyEnvironmentRequiresJSONArray 验证数组设置不能用模块自定义的逗号字符串替代。
func TestApplyEnvironmentRequiresJSONArray(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("app.server.allowed_hosts", []interface{}{"localhost"}); err != nil {
		t.Fatalf("写入数组默认配置失败: %v", err)
	}

	err := configuration.ApplyEnvironment(environmentStub{"APP_SERVER_ALLOWED_HOSTS": "api.example.com,admin.example.com"})
	if !errors.Is(err, ErrConfigEnvironmentType) {
		t.Fatalf("非 JSON 数组应返回 ErrConfigEnvironmentType，实际为 %v", err)
	}
	if got := configuration.Get("app.server.allowed_hosts"); !reflect.DeepEqual(got, []interface{}{"localhost"}) {
		t.Fatalf("非法数组覆盖不应污染默认值，实际为 %#v", got)
	}
}

// TestApplyEnvironmentOverridesDeclaredObject 验证默认空对象也能通过 JSON 环境值整体覆盖。
func TestApplyEnvironmentOverridesDeclaredObject(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("database.connections.mysql.params", map[string]interface{}{}); err != nil {
		t.Fatalf("写入对象默认配置失败: %v", err)
	}

	err := configuration.ApplyEnvironment(environmentStub{
		"DATABASE_CONNECTIONS_MYSQL_PARAMS": `{"charset":"utf8mb4","timeout":10}`,
	})
	if err != nil {
		t.Fatalf("JSON 对象环境值覆盖失败: %v", err)
	}
	want := map[string]interface{}{"charset": "utf8mb4", "timeout": json.Number("10")}
	if got := configuration.Get("database.connections.mysql.params"); !reflect.DeepEqual(got, want) {
		t.Fatalf("对象配置覆盖错误，want=%#v got=%#v", want, got)
	}
}

// TestApplyEnvironmentIgnoresUndeclaredObjectReplacement 验证非空结构对象不能被整体环境值替换。
func TestApplyEnvironmentIgnoresUndeclaredObjectReplacement(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("database.connections.mysql.username", "default-user"); err != nil {
		t.Fatalf("写入对象子项失败: %v", err)
	}
	if err := configuration.Set("database.connections.mysql.password", "default-password"); err != nil {
		t.Fatalf("写入对象子项失败: %v", err)
	}

	err := configuration.ApplyEnvironment(environmentStub{
		"DATABASE_CONNECTIONS_MYSQL": `{"username":"object-user","unknown":"value"}`,
	})
	if err != nil {
		t.Fatalf("未声明的对象整体环境键应被忽略，实际为 %v", err)
	}
	if got := configuration.GetString("database.connections.mysql.username"); got != "default-user" {
		t.Fatalf("非空结构对象不得被整体替换，实际为 %q", got)
	}
}

// TestApplyEnvironmentConvertsProgrammaticConfigTypes 验证代码声明的 Go 类型也沿用默认值类型转换。
func TestApplyEnvironmentConvertsProgrammaticConfigTypes(t *testing.T) {
	configuration := NewConfig()
	defaults := map[string]interface{}{
		"feature.signed":   int32(1),
		"feature.unsigned": uint16(1),
		"feature.ratio":    float32(1),
		"feature.names":    []string{"default"},
		"feature.limits":   map[string]int{"default": 1},
		"feature.optional": nil,
		"feature.enabled":  true,
	}
	for path, value := range defaults {
		if err := configuration.Set(path, value); err != nil {
			t.Fatalf("写入代码配置 %s 失败: %v", path, err)
		}
	}

	err := configuration.ApplyEnvironment(environmentStub{
		"FEATURE_SIGNED":   "-12",
		"FEATURE_UNSIGNED": "42",
		"FEATURE_RATIO":    "1.25",
		"FEATURE_NAMES":    `["api","worker"]`,
		"FEATURE_LIMITS":   `{"api":8}`,
		"FEATURE_OPTIONAL": "null",
		"FEATURE_ENABLED":  "off",
	})
	if err != nil {
		t.Fatalf("代码配置类型转换失败: %v", err)
	}
	if got := configuration.Get("feature.signed"); got != int32(-12) {
		t.Fatalf("有符号整数转换错误: %#v", got)
	}
	if got := configuration.Get("feature.unsigned"); got != uint16(42) {
		t.Fatalf("无符号整数转换错误: %#v", got)
	}
	if got := configuration.Get("feature.ratio"); got != float32(1.25) {
		t.Fatalf("浮点数转换错误: %#v", got)
	}
	if got := configuration.Get("feature.names"); !reflect.DeepEqual(got, []string{"api", "worker"}) {
		t.Fatalf("字符串切片转换错误: %#v", got)
	}
	if got := configuration.Get("feature.limits"); !reflect.DeepEqual(got, map[string]int{"api": 8}) {
		t.Fatalf("类型化对象转换错误: %#v", got)
	}
	if got := configuration.Get("feature.optional"); got != nil {
		t.Fatalf("null 环境值应保持 nil，实际为 %#v", got)
	}
	if got := configuration.GetBool("feature.enabled", true); got {
		t.Fatal("off 应转换为 false")
	}
}

// TestApplyEnvironmentRejectsInvalidProgrammaticValues 验证溢出和尾随 JSON 不会进入最终快照。
func TestApplyEnvironmentRejectsInvalidProgrammaticValues(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		prototype interface{}
		raw       string
	}{
		{name: "无符号整数为负数", path: "feature.unsigned", prototype: uint8(1), raw: "-1"},
		{name: "JSON 整数不能使用小数", path: "feature.limit", prototype: json.Number("10"), raw: "1.5"},
		{name: "浮点数格式非法", path: "feature.ratio", prototype: float64(1), raw: "one"},
		{name: "对象包含尾随值", path: "feature.options", prototype: map[string]interface{}{}, raw: `{} {}`},
		{name: "类型化数组格式非法", path: "feature.names", prototype: []string{}, raw: `[1]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := NewConfig()
			if err := configuration.Set(test.path, test.prototype); err != nil {
				t.Fatalf("写入测试默认配置失败: %v", err)
			}
			environmentName := strings.ToUpper(strings.ReplaceAll(test.path, ".", "_"))
			if err := configuration.ApplyEnvironment(environmentStub{environmentName: test.raw}); !errors.Is(err, ErrConfigEnvironmentType) {
				t.Fatalf("非法环境值应返回 ErrConfigEnvironmentType，实际为 %v", err)
			}
		})
	}
}

// TestApplyEnvironmentAcceptsTypedNilSource 验证可选环境服务为类型化 nil 时保持默认配置。
func TestApplyEnvironmentAcceptsTypedNilSource(t *testing.T) {
	configuration := NewConfig()
	if err := configuration.Set("app.app_debug", false); err != nil {
		t.Fatalf("写入默认配置失败: %v", err)
	}
	var source *environmentStub
	if err := configuration.ApplyEnvironment(source); err != nil {
		t.Fatalf("类型化 nil 环境源不应返回错误: %v", err)
	}
}
