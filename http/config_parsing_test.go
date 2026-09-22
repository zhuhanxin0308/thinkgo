package http

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3"
	frameworkconfig "github.com/zhuhanxin0308/thinkgo/v3/config"
)

type httpConfigEnvironment map[string]string

func (environment httpConfigEnvironment) Lookup(name string) (string, bool) {
	value, exists := environment[name]
	return value, exists
}

// TestExactConfigIntegerTypes 验证所有受支持整数来源均无损转换，越界和小数被拒绝。
func TestExactConfigIntegerTypes(t *testing.T) {
	tests := []struct {
		name     string
		value    interface{}
		expected int64
		valid    bool
	}{
		{name: "int", value: int(1), expected: 1, valid: true},
		{name: "int8", value: int8(2), expected: 2, valid: true},
		{name: "int16", value: int16(3), expected: 3, valid: true},
		{name: "int32", value: int32(4), expected: 4, valid: true},
		{name: "int64", value: int64(5), expected: 5, valid: true},
		{name: "uint", value: uint(6), expected: 6, valid: true},
		{name: "uint8", value: uint8(7), expected: 7, valid: true},
		{name: "uint16", value: uint16(8), expected: 8, valid: true},
		{name: "uint32", value: uint32(9), expected: 9, valid: true},
		{name: "uint64", value: uint64(10), expected: 10, valid: true},
		{name: "float32", value: float32(11), expected: 11, valid: true},
		{name: "float64", value: float64(12), expected: 12, valid: true},
		{name: "json number", value: json.Number("13"), expected: 13, valid: true},
		{name: "string", value: "14", expected: 14, valid: true},
		{name: "fraction", value: 1.5, valid: false},
		{name: "nan", value: math.NaN(), valid: false},
		{name: "uint overflow", value: uint64(math.MaxUint64), valid: false},
		{name: "object", value: struct{}{}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, err := exactConfigInteger(test.value)
			if test.valid && (err != nil || actual != test.expected) {
				t.Fatalf("整数转换错误，期望 %d，实际 %d，错误=%v", test.expected, actual, err)
			}
			if !test.valid && err == nil {
				t.Fatalf("非法值 %#v 必须返回错误", test.value)
			}
		})
	}
}

// TestHTTPConfigCollectionParsing 验证列表、压缩等级和 Host 规范化不会忽略非法项。
func TestHTTPConfigCollectionParsing(t *testing.T) {
	values := map[string]interface{}{
		"strings":    []string{" a ", "b"},
		"interfaces": []interface{}{"c", "d"},
		"csv":        "e,f",
	}
	for key, expected := range map[string][]string{
		"strings": {"a", "b"}, "interfaces": {"c", "d"}, "csv": {"e", "f"},
	} {
		actual, err := configStringList(values, key, nil)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("列表 %s 解析错误，实际=%#v 错误=%v", key, actual, err)
		}
	}
	if _, err := configStringList(map[string]interface{}{"bad": []interface{}{1}}, "bad", nil); err == nil {
		t.Fatal("非字符串列表项必须返回错误")
	}
	if _, err := splitStrictConfigList("a,,b", "list"); err == nil {
		t.Fatal("CSV 空项必须返回错误")
	}

	interfaceLevels, err := compressionLevelMap(map[string]interface{}{"gzip": 1})
	if err != nil || interfaceLevels["gzip"] != 1 {
		t.Fatalf("interface 压缩等级解析错误: %#v %v", interfaceLevels, err)
	}
	integerLevels, err := compressionLevelMap(map[string]int{"br": 2})
	if err != nil || integerLevels["br"] != 2 {
		t.Fatalf("int 压缩等级解析错误: %#v %v", integerLevels, err)
	}
	if _, err := compressionLevelMap([]int{1}); err == nil {
		t.Fatal("非对象压缩等级必须返回错误")
	}
	for algorithm, levels := range map[string][]int64{
		"gzip": {-2, 9}, "deflate": {-2, 9}, "br": {0, 11}, "zstd": {1, 4},
	} {
		for _, level := range levels {
			if !validCompressionLevel(algorithm, level) {
				t.Fatalf("算法 %s 的边界等级 %d 应合法", algorithm, level)
			}
		}
	}
	if validCompressionLevel("unknown", 1) {
		t.Fatal("未知压缩算法必须被拒绝")
	}
}

// TestAllowedHostNormalization 验证端口、通配子域和 IPv6 Host 使用统一规范化结果。
func TestAllowedHostNormalization(t *testing.T) {
	hosts, err := normalizeAllowedHosts([]string{"Example.COM:8443", "*.API.Example.com", "[::1]", "example.com:8443"})
	if err != nil {
		t.Fatalf("规范化 Host 白名单失败: %v", err)
	}
	if !reflect.DeepEqual(hosts, []string{"example.com", "*.api.example.com", "::1"}) {
		t.Fatalf("Host 白名单规范化结果错误: %#v", hosts)
	}
	handler := &Http{srvConf: serverConf{AllowedHosts: hosts}}
	for _, host := range []string{"example.com:9000", "example.com.:443", "v1.api.example.com", "[::1]:8080"} {
		if !handler.isAllowedHost(host) {
			t.Fatalf("Host %q 应命中白名单", host)
		}
	}
	for _, host := range []string{
		"api.example.com",
		"evil.example.net",
		"bad@host",
		"example.com:not-a-port",
		"example.com:65536",
		"[[::1]]",
		"::1",
	} {
		if handler.isAllowedHost(host) {
			t.Fatalf("Host %q 不应命中白名单", host)
		}
	}
	if _, err := normalizeAllowedHosts([]string{"*.bad_host"}); err == nil {
		t.Fatal("非法通配 Host 必须返回错误")
	}
}

// TestDefaultServerAddressMatchesThinkPHPRun 验证未配置服务地址时与
// think run 的默认监听地址 0.0.0.0:8000 一致。
func TestDefaultServerAddressMatchesThinkPHPRun(t *testing.T) {
	configuration, err := parseServerConfig(map[string]interface{}{})
	if err != nil {
		t.Fatalf("解析默认 HTTP 配置失败: %v", err)
	}
	if configuration.Host != "0.0.0.0" || configuration.Port != 8000 {
		t.Fatalf("默认 HTTP 地址错误: %s:%d", configuration.Host, configuration.Port)
	}
}

// TestRequestEndTimeoutConfig 验证请求收尾截止时间有稳定默认值，
// 显式毫秒配置会生效，零值则按安全配置规则拒绝。
func TestRequestEndTimeoutConfig(t *testing.T) {
	defaults, err := parseServerConfig(map[string]interface{}{})
	if err != nil {
		t.Fatalf("解析默认请求收尾配置失败: %v", err)
	}
	if defaults.RequestEndTimeout != defaultRequestEndTimeout {
		t.Fatalf("默认请求收尾超时错误: %v", defaults.RequestEndTimeout)
	}

	configured, err := parseServerConfig(map[string]interface{}{"request_end_timeout_ms": 275})
	if err != nil {
		t.Fatalf("解析显式请求收尾配置失败: %v", err)
	}
	if configured.RequestEndTimeout != 275*time.Millisecond {
		t.Fatalf("显式请求收尾超时错误: %v", configured.RequestEndTimeout)
	}
	if _, err = parseServerConfig(map[string]interface{}{"request_end_timeout_ms": 0}); err == nil {
		t.Fatal("请求收尾超时为零时必须返回配置错误")
	}
}

// TestProjectTemplateDeclaresRequestEndTimeoutOverride 验证项目配置模板声明了
// 请求收尾超时叶子，因此严格环境合并可以识别对应的 APP_SERVER 覆盖键。
func TestProjectTemplateDeclaresRequestEndTimeoutOverride(t *testing.T) {
	configuration := frameworkconfig.NewConfig()
	if err := configuration.LoadAll(filepath.Join("..", "testdata", "project", "config")); err != nil {
		t.Fatalf("加载项目配置模板失败: %v", err)
	}
	if milliseconds := configuration.GetInt("app.server.request_end_timeout_ms"); milliseconds != 2000 {
		t.Fatalf("项目配置模板的请求收尾超时错误: %d", milliseconds)
	}
	if err := configuration.ApplyEnvironment(httpConfigEnvironment{
		"APP_SERVER_REQUEST_END_TIMEOUT_MS": "3250",
	}); err != nil {
		t.Fatalf("环境覆盖请求收尾超时失败: %v", err)
	}
	server, _, err := parseHTTPApplicationConfig(configuration.Get("app", map[string]interface{}{}))
	if err != nil {
		t.Fatalf("解析环境覆盖后的请求收尾配置失败: %v", err)
	}
	if server.RequestEndTimeout != 3250*time.Millisecond {
		t.Fatalf("环境覆盖后的请求收尾超时错误: %v", server.RequestEndTimeout)
	}
}

// TestProjectHTTPConfigUsesExplicitRuntimeAddress 验证项目快照经过宿主覆盖后，
// HTTP 严格解析保留显式环回地址和整数端口，不会退回全接口默认值。
func TestProjectHTTPConfigUsesExplicitRuntimeAddress(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	projectConfig := `{"app_env":"test","server":{"host":"0.0.0.0","port":8000},"compression":{"enable":false}}`
	if err := os.WriteFile(filepath.Join(basePath, "config", "app.json"), []byte(projectConfig), 0o644); err != nil {
		t.Fatalf("写入项目 HTTP 配置失败: %v", err)
	}
	app, err := framework.BuildConsoleApp(basePath)
	if err != nil {
		t.Fatalf("构建项目 HTTP 配置测试应用失败: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.ApplyProjectApplicationOverrides(map[string]interface{}{
		"server.host": "127.0.0.1",
		"server.port": 39091,
	}); err != nil {
		t.Fatalf("覆盖项目 HTTP 地址失败: %v", err)
	}

	server, _, err := parseProjectHTTPConfig(app)
	if err != nil {
		t.Fatalf("解析覆盖后的项目 HTTP 配置失败: %v", err)
	}
	if server.Host != "127.0.0.1" || server.Port != 39091 {
		t.Fatalf("显式监听地址被默认值覆盖: %s:%d", server.Host, server.Port)
	}
}
