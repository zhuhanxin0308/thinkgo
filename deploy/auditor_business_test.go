package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace/noop"
)

type deployAuditConnection struct {
	id       db.ConnectionID
	rows     []map[string]interface{}
	queryErr error
}

func newDeployAuditConnection() *deployAuditConnection {
	return &deployAuditConnection{id: db.NewConnectionID("deploy-audit")}
}

func (connection *deployAuditConnection) ConnectionID() db.ConnectionID { return connection.id }
func (connection *deployAuditConnection) Select(context.Context, db.SelectRequest) ([]map[string]interface{}, error) {
	return nil, errors.New("部署探针连接不支持 ORM Select")
}
func (connection *deployAuditConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, errors.New("部署探针连接不支持 ORM Insert")
}
func (connection *deployAuditConnection) Update(context.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, errors.New("部署探针连接不支持 ORM Update")
}
func (connection *deployAuditConnection) Delete(context.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, errors.New("部署探针连接不支持 ORM Delete")
}
func (connection *deployAuditConnection) Count(context.Context, db.CountRequest) (int64, error) {
	return 0, errors.New("部署探针连接不支持 ORM Count")
}
func (connection *deployAuditConnection) Close() error { return nil }
func (connection *deployAuditConnection) QueryContext(ctx context.Context, _ string, _ ...interface{}) ([]map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return connection.rows, connection.queryErr
}
func (connection *deployAuditConnection) ExecuteContext(context.Context, string, ...interface{}) (int64, error) {
	return 0, errors.New("部署探针连接不支持原生写入")
}

// TestCheckContractsRejectInvalidDefinitions 验证扩展检查器名称、状态和空审计器都使用稳定失败语义。
func TestCheckContractsRejectInvalidDefinitions(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *framework.App) Result
	}{
		{name: "", run: func(context.Context, *framework.App) Result { return pass("ok") }},
		{name: "bad\nname", run: func(context.Context, *framework.App) Result { return pass("ok") }},
		{name: "missing-run", run: nil},
	} {
		if _, err := NewCheck(test.name, test.run); err == nil {
			t.Fatalf("非法检查器必须被拒绝: name=%q", test.name)
		}
	}
	check, err := NewCheck("  custom.health  ", func(context.Context, *framework.App) Result {
		return Result{Level: Level("UNKNOWN"), Message: "不应透传"}
	})
	if err != nil || check.Name() != "custom.health" {
		t.Fatalf("合法检查器创建失败: check=%v err=%v", check, err)
	}
	result := check.Run(context.Background(), nil)
	if result.Name != "custom.health" || result.Level != LevelFail || result.Message != "检查器返回了非法状态" {
		t.Fatalf("非法检查状态必须归一化为失败: %#v", result)
	}
	if _, err := NewAuditor(nil); err == nil {
		t.Fatal("空检查器实例必须被拒绝")
	}
	var nilAuditor *Auditor
	report := nilAuditor.Run(context.Background(), nil)
	if len(report.Results) != 1 || report.Results[0].Level != LevelFail {
		t.Fatalf("空审计器必须返回稳定失败报告: %#v", report)
	}
}

// TestLifecycleRuntimeAndHostChecksCoverUnsafeBoundaries 验证 nil 应用、控制字符 Host 和非法运行时路径均阻断部署。
func TestLifecycleRuntimeAndHostChecksCoverUnsafeBoundaries(t *testing.T) {
	if result := checkApplicationLifecycle(context.Background(), nil); result.Level != LevelFail {
		t.Fatalf("nil 应用必须阻断生命周期检查: %#v", result)
	}
	if result := checkProductionConfiguration(context.Background(), nil); result.Level != LevelFail {
		t.Fatalf("nil 应用必须阻断生产配置检查: %#v", result)
	}
	app, configuration := newDeployTestApp(t)
	configuration.Set("app.server.allowed_hosts", []string{" api.example.com ", "admin.example.com"})
	if result := checkAllowedHosts(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("显式字符串 Host 列表应通过: %#v", result)
	}
	configuration.Set("app.server.allowed_hosts", []string{"api.example.com\rignored"})
	if result := checkAllowedHosts(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("包含控制字符的 Host 必须失败: %#v", result)
	}
	app.RuntimePath = filepath.Join(app.BasePath, "missing-runtime")
	if result := checkRuntimeFilesystem(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("不存在的运行时目录必须失败: %#v", result)
	}
	runtimeFile := filepath.Join(app.BasePath, "runtime-file")
	if err := os.WriteFile(runtimeFile, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("创建运行时边界文件失败: %v", err)
	}
	app.RuntimePath = runtimeFile
	if result := checkRuntimeFilesystem(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("普通文件不能充当运行时目录: %#v", result)
	}
}

// TestOperationalAndDatabasePoliciesAreDeploymentVisible 验证运维暴露与数据库启动策略进入严格部署门禁。
func TestOperationalAndDatabasePoliciesAreDeploymentVisible(t *testing.T) {
	app, configuration := newDeployTestApp(t)
	configuration.Set("app.operational_routes_enable", true)
	configuration.Set("app.operational_routes_access", string(framework.OperationalAccessRestricted))
	configuration.Set("app.operational_routes_allowed_cidrs", []interface{}{"10.0.0.0/8"})
	if result := checkOperationalAccess(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("受限运维端点配置应通过: %#v", result)
	}
	configuration.Set("app.operational_routes_allowed_cidrs", []interface{}{})
	if result := checkOperationalAccess(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("空运维白名单必须阻断部署: %#v", result)
	}
	configuration.Set("app.operational_routes_access", string(framework.OperationalAccessPublic))
	if result := checkOperationalAccess(context.Background(), app); result.Level != LevelWarn {
		t.Fatalf("显式公开运维端点必须产生警告: %#v", result)
	}

	configuration.Set("app.database_startup_policy", string(framework.DatabaseStartupRequired))
	if result := checkDatabaseStartupPolicy(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("required 数据库策略应通过: %#v", result)
	}
	configuration.Set("app.database_startup_policy", string(framework.DatabaseStartupDegraded))
	if result := checkDatabaseStartupPolicy(context.Background(), app); result.Level != LevelWarn {
		t.Fatalf("degraded 数据库策略必须产生警告: %#v", result)
	}
	configuration.Set("app.database_startup_policy", "")
	if result := checkDatabaseStartupPolicy(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("缺少数据库启动策略必须阻断部署: %#v", result)
	}
}

// TestDatabaseConnectivityChecksRealQueryShape 验证部署数据库探针要求无错误且恰好返回一行。
func TestDatabaseConnectivityChecksRealQueryShape(t *testing.T) {
	app, _ := newDeployTestApp(t)
	connection := newDeployAuditConnection()
	app.Instance(string(framework.ServiceDB), db.NewDB(connection))
	connection.rows = []map[string]interface{}{{"ready": int64(1)}}
	if result := checkDatabaseConnectivity(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("单行数据库探针应通过: %#v", result)
	}
	connection.rows = []map[string]interface{}{{"ready": int64(1)}, {"ready": int64(1)}}
	if result := checkDatabaseConnectivity(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("多行数据库探针必须失败: %#v", result)
	}
	connection.rows = nil
	connection.queryErr = errors.New("database unavailable")
	if result := checkDatabaseConnectivity(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("数据库查询错误必须失败: %#v", result)
	}
}

// TestMigrationCheckRejectsWrongServicesAndUnsupportedDialect 验证迁移门禁不会接受错误服务类型或非 SQL 方言。
func TestMigrationCheckRejectsWrongServicesAndUnsupportedDialect(t *testing.T) {
	wrongRegistry, _ := newDeployTestApp(t)
	wrongRegistry.Instance(string(framework.ServiceMigration), "not-a-registry")
	if result := checkMigrations(context.Background(), wrongRegistry); result.Level != LevelFail {
		t.Fatalf("错误迁移服务类型必须失败: %#v", result)
	}
	unsupportedDatabase, _ := newDeployTestApp(t)
	unsupportedDatabase.Instance(string(framework.ServiceDB), db.NewDB(newDeployAuditConnection()))
	if result := checkMigrations(context.Background(), unsupportedDatabase); result.Level != LevelFail {
		t.Fatalf("不支持的迁移方言必须失败: %#v", result)
	}
}

// TestRouteAndTelemetryChecksDistinguishWarningFailureAndPass 验证空路由、追踪服务错配和明文端点的分级语义。
func TestRouteAndTelemetryChecksDistinguishWarningFailureAndPass(t *testing.T) {
	emptyApp, _ := newDeployTestApp(t)
	if result := checkRoutes(context.Background(), emptyApp); result.Level != LevelWarn {
		t.Fatalf("空路由应用应返回警告: %#v", result)
	}
	routedApp, _ := newDeployTestApp(t)
	router, err := framework.ResolveServiceAs[*route.Router](routedApp, framework.ServiceRoute)
	if err != nil {
		t.Fatalf("解析测试路由器失败: %v", err)
	}
	if _, err := router.Get("/health", "Health@index"); err != nil {
		t.Fatalf("注册测试路由失败: %v", err)
	}
	if result := checkRoutes(context.Background(), routedApp); result.Level != LevelPass {
		t.Fatalf("非空冻结路由快照应通过: %#v", result)
	}
	if err := routedApp.Instance(string(framework.ServiceRoute), "wrong-route-service"); !errors.Is(err, framework.ErrServiceTypeMismatch) {
		t.Fatalf("错误路由服务类型必须在绑定阶段被拒绝: %v", err)
	}

	telemetryApp, configuration := newDeployTestApp(t)
	configuration.Set("telemetry.enable", true)
	configuration.Set("telemetry.endpoint", "https://collector.example.com")
	telemetryApp.Instance(string(framework.ServiceTelemetry), telemetry.Disabled())
	if result := checkTelemetry(context.Background(), telemetryApp); result.Level != LevelFail {
		t.Fatalf("启用配置与禁用服务错配必须失败: %#v", result)
	}
	tracing, err := telemetry.New(telemetry.Config{
		Enabled:             true,
		Provider:            noop.NewTracerProvider(),
		Propagator:          propagation.TraceContext{},
		InstrumentationName: "thinkgo/deploy-test",
	})
	if err != nil {
		t.Fatalf("创建测试追踪服务失败: %v", err)
	}
	telemetryApp.Instance(string(framework.ServiceTelemetry), tracing)
	if result := checkTelemetry(context.Background(), telemetryApp); result.Level != LevelPass {
		t.Fatalf("HTTPS OTLP 服务应通过: %#v", result)
	}
	configuration.Set("telemetry.endpoint", "http://collector.internal")
	if result := checkTelemetry(context.Background(), telemetryApp); result.Level != LevelWarn {
		t.Fatalf("明文 OTLP 端点应告警: %#v", result)
	}
}

// TestReleaseMetadataRequiresCompleteBoundedFiles 验证发布元数据完整性、模块路径和 go.mod 大小边界。
func TestReleaseMetadataRequiresCompleteBoundedFiles(t *testing.T) {
	app, _ := newDeployTestApp(t)
	for name, content := range map[string]string{
		"RELEASING.md": "# Releasing",
		"go.mod":       "module example.com/acme/service\n",
	} {
		if err := os.WriteFile(filepath.Join(app.BasePath, name), []byte(content), 0o600); err != nil {
			t.Fatalf("写入发布元数据 %s 失败: %v", name, err)
		}
	}
	if result := checkReleaseMetadata(context.Background(), app); result.Level != LevelPass {
		t.Fatalf("完整发布元数据应通过: %#v", result)
	}
	if err := os.WriteFile(filepath.Join(app.BasePath, "go.mod"), []byte("module thinkgo\n"), 0o600); err != nil {
		t.Fatalf("写入占位模块路径失败: %v", err)
	}
	if result := checkReleaseMetadata(context.Background(), app); result.Level != LevelFail {
		t.Fatalf("占位模块路径必须失败: %#v", result)
	}
	oversizedPath := filepath.Join(app.BasePath, "oversized.mod")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat("x", 1024*1024+1)), 0o600); err != nil {
		t.Fatalf("写入超限 go.mod 失败: %v", err)
	}
	if _, err := readModuleName(oversizedPath); err == nil {
		t.Fatal("超过 1 MiB 的 go.mod 必须被拒绝")
	}
	missingModulePath := filepath.Join(app.BasePath, "missing-module.mod")
	if err := os.WriteFile(missingModulePath, []byte("go 1.26\n"), 0o600); err != nil {
		t.Fatalf("写入缺少模块声明的文件失败: %v", err)
	}
	if _, err := readModuleName(missingModulePath); err == nil {
		t.Fatal("缺少 module 声明必须被拒绝")
	}
	externalPath := filepath.Join(t.TempDir(), "external.mod")
	if err := os.WriteFile(externalPath, []byte("module example.com/external\n"), 0o600); err != nil {
		t.Fatalf("写入外部模块文件失败: %v", err)
	}
	linkedPath := filepath.Join(app.BasePath, "linked.mod")
	if err := os.Symlink(externalPath, linkedPath); err != nil {
		t.Skipf("当前环境不能创建模块文件符号链接: %v", err)
	}
	if _, err := readModuleName(linkedPath); err == nil {
		t.Fatal("符号链接模块文件必须被拒绝")
	}
}

// TestDeploymentValueHelpersPreserveCanonicalInputs 验证列表、路径和普通文件辅助逻辑的输入边界。
func TestDeploymentValueHelpersPreserveCanonicalInputs(t *testing.T) {
	values, err := stringList([]string{" first ", "second"})
	if err != nil || len(values) != 2 || values[0] != "first" {
		t.Fatalf("字符串列表规范化失败: values=%v err=%v", values, err)
	}
	for _, value := range []interface{}{[]string{""}, []interface{}{"ok", 7}, 7} {
		if _, err := stringList(value); err == nil {
			t.Fatalf("非法列表必须被拒绝: %#v", value)
		}
	}
	basePath := t.TempDir()
	relative := resolveApplicationPath(basePath, "config/app.json")
	if relative != filepath.Join(basePath, "config", "app.json") {
		t.Fatalf("相对路径解析错误: %s", relative)
	}
	absolute := filepath.Join(basePath, "absolute.pem")
	if resolveApplicationPath("ignored", absolute) != filepath.Clean(absolute) {
		t.Fatalf("绝对路径不应被应用根目录改写: %s", absolute)
	}
	if err := requireRegularFile(basePath); err == nil {
		t.Fatal("目录不能通过普通文件检查")
	}
	if err := os.WriteFile(absolute, []byte("ready"), 0o600); err != nil {
		t.Fatalf("写入普通文件失败: %v", err)
	}
	if err := requireRegularFile(absolute); err != nil {
		t.Fatalf("普通文件应通过检查: %v", err)
	}
}
