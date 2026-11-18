package framework

import (
	"reflect"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestAppStructDecomposedByResponsibility 验证 App 按单一职责拆分为内嵌状态组件且保持原有访问契约。
func TestAppStructDecomposedByResponsibility(t *testing.T) {
	appType := reflect.TypeFor[App]()
	expectedEmbedded := []string{
		"appMetadataState",
		"appServiceHolders",
		"appStartupState",
		"appRegistrationState",
		"appRuntimePolicyState",
		"appDatabaseRuntimeState",
	}
	for _, name := range expectedEmbedded {
		field, ok := appType.FieldByName(name)
		if !ok || !field.Anonymous {
			t.Fatalf("App 缺少内嵌职责组件 %s: ok=%t field=%+v", name, ok, field)
		}
	}

	basePath := t.TempDir()
	writeTestAppConfigFiles(t, basePath)
	app := mustBuildTestConsoleApp(t, basePath)
	t.Cleanup(func() { _ = app.Close() })

	if app.container == nil || app.config == nil || app.env == nil || app.log == nil || app.route == nil {
		t.Fatal("App 服务持有组件未完成装配")
	}
	if !app.RunningInConsole() || !app.skipDatabaseInit || app.State() != ApplicationStateInitialized {
		t.Fatalf("App 元数据与生命周期组件状态异常: console=%t skipDB=%t state=%s", app.RunningInConsole(), app.skipDatabaseInit, app.State())
	}

	params, err := readDatabaseParams(map[string]string{"sslmode": "verify-full"})
	if err != nil || params["sslmode"] != "verify-full" {
		t.Fatalf("readDatabaseParams 未支持强类型 map[string]string: params=%v err=%v", params, err)
	}
	if _, err := readDatabaseParams(map[string]string{" ": "invalid"}); err == nil || !reflect.DeepEqual(err != nil, true) {
		t.Fatalf("readDatabaseParams 应拒绝空白键: %v", err)
	}
	if empty, err := readDatabaseParams(map[string]string{}); err != nil || empty != nil {
		t.Fatalf("readDatabaseParams 空 map[string]string 应返回 nil: %v %v", empty, err)
	}
	cfg, err := readDatabaseConfig(map[string]interface{}{
		"type":     "pgsql",
		"hostname": "127.0.0.1",
		"database": "app",
		"params":   map[string]string{"sslmode": "verify-full"},
	})
	if err != nil || cfg.Params["sslmode"] != "verify-full" || cfg.TimestampValueType != db.TimestampValueTypeUnix {
		t.Fatalf("readDatabaseConfig 未正确处理 map[string]string params: cfg=%+v err=%v", cfg, err)
	}
}
