package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/migration"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
	"github.com/zhuhanxin0308/thinkgo/framework/telemetry"
)

// Level 表示部署检查结果的严重级别。
type Level string

const (
	LevelPass Level = "PASS"
	LevelWarn Level = "WARN"
	LevelFail Level = "FAIL"
	// defaultCheckTimeout 限制单项外部资源检查，避免部署命令被数据库或文件系统永久阻塞。
	defaultCheckTimeout = 10 * time.Second
	// maximumOrphanedChecks 限制不遵守 context 契约的扩展检查最多占用的后台槽位。
	// 超过该数量后审计 fail-closed，避免重复调用 Run 无界泄漏 goroutine。
	maximumOrphanedChecks = 4
	// maximumModuleFileBytes 限制部署审计读取的 go.mod 大小。
	maximumModuleFileBytes = 1 << 20
)

// ErrDeploymentBlocked 表示报告包含失败项，或严格模式下包含警告项。
var ErrDeploymentBlocked = errors.New("deployment checks blocked")

// Result 是单项部署检查的稳定机器可读结果。
type Result struct {
	Name    string
	Level   Level
	Message string
}

// Report 聚合全部检查；失败项始终阻止部署，严格模式也阻止警告项。
type Report struct {
	Results []Result
}

// Failed 判断报告是否包含部署阻断项。
func (report Report) Failed(strict bool) bool {
	for _, result := range report.Results {
		if result.Level == LevelFail || strict && result.Level == LevelWarn {
			return true
		}
	}
	return false
}

// Check 是一个只检查当前应用状态、不会修复或覆盖配置的部署门禁。
type Check interface {
	Name() string
	Run(context.Context, *framework.App) Result
}

type checkFunc struct {
	name string
	run  func(context.Context, *framework.App) Result
}

// NewCheck 创建可由业务模块扩展的命名部署检查。
func NewCheck(name string, run func(context.Context, *framework.App) Result) (Check, error) {
	name = strings.TrimSpace(name)
	if name == "" || run == nil || strings.ContainsAny(name, "\r\n\t") {
		return nil, errors.New("部署检查器名称或回调非法")
	}
	return checkFunc{name: name, run: run}, nil
}

func (check checkFunc) Name() string { return check.name }
func (check checkFunc) Run(ctx context.Context, app *framework.App) Result {
	result := check.run(ctx, app)
	result.Name = check.name
	if result.Level != LevelPass && result.Level != LevelWarn && result.Level != LevelFail {
		result.Level = LevelFail
		result.Message = "检查器返回了非法状态"
	}
	return result
}

// Auditor 按稳定名称顺序执行一组部署检查。
type Auditor struct {
	checks         []Check
	executionSlots chan struct{}
}

// NewAuditor 创建自定义部署审计器，拒绝空检查器和重复名称。
func NewAuditor(checks ...Check) (*Auditor, error) {
	seen := make(map[string]struct{}, len(checks))
	validated := make([]Check, 0, len(checks))
	for _, check := range checks {
		if check == nil || strings.TrimSpace(check.Name()) == "" {
			return nil, errors.New("部署检查器名称不能为空")
		}
		if _, exists := seen[check.Name()]; exists {
			return nil, fmt.Errorf("部署检查器 %q 重复", check.Name())
		}
		seen[check.Name()] = struct{}{}
		validated = append(validated, check)
	}
	sort.Slice(validated, func(left, right int) bool {
		return validated[left].Name() < validated[right].Name()
	})
	return &Auditor{checks: validated, executionSlots: make(chan struct{}, maximumOrphanedChecks)}, nil
}

// NewDefaultAuditor 创建运行就绪审计器，检查生命周期、生产配置、文件权限、数据库和迁移。
func NewDefaultAuditor() *Auditor {
	auditor, _ := NewAuditor(
		checkFunc{name: "application.lifecycle", run: checkApplicationLifecycle},
		checkFunc{name: "configuration.production", run: checkProductionConfiguration},
		checkFunc{name: "database.connectivity", run: checkDatabaseConnectivity},
		checkFunc{name: "database.startup_policy", run: checkDatabaseStartupPolicy},
		checkFunc{name: "filesystem.runtime", run: checkRuntimeFilesystem},
		checkFunc{name: "http.hosts", run: checkAllowedHosts},
		checkFunc{name: "http.operational_access", run: checkOperationalAccess},
		checkFunc{name: "http.security_headers", run: checkSecurityHeaders},
		checkFunc{name: "http.tls", run: checkTLSFiles},
		checkFunc{name: "migrations.status", run: checkMigrations},
		checkFunc{name: "observability.telemetry", run: checkTelemetry},
		checkFunc{name: "routes.automatic_dispatch", run: checkAutomaticDispatch},
		checkFunc{name: "routes.snapshot", run: checkRoutes},
	)
	return auditor
}

// NewReleaseMetadataCheck 提供源码发布检查，由维护框架或源码分发流程的调用方显式选用。
// 二进制部署不需要携带 go.mod 或框架发布说明，因此默认运行就绪审计不包含此项。
func NewReleaseMetadataCheck() Check {
	return checkFunc{name: "release.metadata", run: checkReleaseMetadata}
}

// Run 执行全部检查；检查 panic 被隔离为失败项，其余检查仍继续运行。
func (auditor *Auditor) Run(ctx context.Context, app *framework.App) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	if auditor == nil {
		return Report{Results: []Result{{Name: "auditor", Level: LevelFail, Message: "部署审计器为空"}}}
	}
	results := make([]Result, 0, len(auditor.checks))
	for _, check := range auditor.checks {
		checkContext, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
		results = append(results, auditor.runCheckWithDeadline(checkContext, app, check))
		cancel()
	}
	return Report{Results: results}
}

// runCheckWithDeadline 隔离不遵守 context 的扩展检查，同时把潜在孤儿 goroutine 数量封顶。
func (auditor *Auditor) runCheckWithDeadline(ctx context.Context, app *framework.App, check Check) Result {
	if auditor == nil || check == nil {
		return Result{Name: "auditor", Level: LevelFail, Message: "部署检查器不可用"}
	}
	if err := ctx.Err(); err != nil {
		return Result{Name: check.Name(), Level: LevelFail, Message: "部署检查超时或上下文已取消"}
	}
	select {
	case auditor.executionSlots <- struct{}{}:
		// 获取执行槽后才启动 goroutine，避免上下文已取消时创建无效后台任务。
	default:
		return Result{Name: check.Name(), Level: LevelFail, Message: "部署检查执行槽已耗尽；存在未遵守 context 的检查"}
	}
	resultChannel := make(chan Result, 1)
	go func() {
		defer func() { <-auditor.executionSlots }()
		resultChannel <- runCheckSafely(ctx, app, check)
	}()
	select {
	case result := <-resultChannel:
		return result
	case <-ctx.Done():
		return Result{Name: check.Name(), Level: LevelFail, Message: "部署检查超时或上下文已取消"}
	}
}

func runCheckSafely(ctx context.Context, app *framework.App, check Check) (result Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = Result{Name: check.Name(), Level: LevelFail, Message: fmt.Sprintf("检查 panic: %v", recovered)}
		}
	}()
	return check.Run(ctx, app)
}

func checkApplicationLifecycle(_ context.Context, app *framework.App) Result {
	if app == nil {
		return fail("应用为空")
	}
	if startupErr := app.StartupError(); startupErr != nil {
		return fail("应用存在启动错误: " + startupErr.Error())
	}
	state := app.State()
	if state != framework.ApplicationStateInitialized && state != framework.ApplicationStateRunning {
		return fail("应用状态不是 initialized/running: " + state.String())
	}
	return pass("应用初始化完成且没有启动错误")
}

func checkProductionConfiguration(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	environment := strings.ToLower(strings.TrimSpace(configuration.GetString("app.app_env")))
	if environment != "production" && environment != "prod" {
		return fail(fmt.Sprintf("app.app_env=%q，不是 production", environment))
	}
	if configuration.GetBool("app.app_debug") || configuration.GetBool("app.app_trace") {
		return fail("生产环境必须关闭 app_debug 和 app_trace")
	}
	profile := strings.ToLower(strings.TrimSpace(configuration.GetString("app.security_profile")))
	if profile != string(framework.SecurityProfileStatelessAPI) && profile != string(framework.SecurityProfileBrowserCookie) {
		return fail("生产环境必须显式配置合法的 app.security_profile")
	}
	return pass("生产环境标识有效，调试和 Trace 已关闭")
}

func checkDatabaseStartupPolicy(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	policy := strings.ToLower(strings.TrimSpace(configuration.GetString("app.database_startup_policy")))
	switch framework.DatabaseStartupPolicy(policy) {
	case framework.DatabaseStartupLazy:
		return warn("数据库按首次使用建立连接；生产流量入口应配合数据库健康检查")
	case framework.DatabaseStartupRequired:
		return pass("默认数据库不可用时将阻止应用启动")
	case framework.DatabaseStartupDegraded:
		return warn("数据库启动策略允许降级；必须确认流量入口严格遵守 readiness")
	case framework.DatabaseStartupDisabled:
		return pass("应用显式声明不装配数据库")
	default:
		return fail("app.database_startup_policy 必须显式配置为 lazy、required、degraded 或 disabled")
	}
}

func checkAllowedHosts(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	hosts, err := stringList(configuration.Get("app.server.allowed_hosts"))
	if err != nil || len(hosts) == 0 {
		return fail("app.server.allowed_hosts 必须是非空字符串列表")
	}
	for _, host := range hosts {
		if host == "*" || strings.ContainsAny(host, "\r\n\t") {
			return fail("allowed_hosts 包含通配符或控制字符")
		}
	}
	return pass(fmt.Sprintf("已配置 %d 个显式 Host", len(hosts)))
}

func checkOperationalAccess(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	if !configuration.GetBool("app.operational_routes_enable") {
		return pass("运维端点未启用")
	}
	access := strings.ToLower(strings.TrimSpace(configuration.GetString("app.operational_routes_access")))
	if access == string(framework.OperationalAccessPublic) {
		return warn("运维端点被显式公开；必须由外部网关完成认证和访问限制")
	}
	if access != string(framework.OperationalAccessRestricted) {
		return fail("启用运维端点时必须显式配置 operational_routes_access")
	}
	values, err := stringList(configuration.Get("app.operational_routes_allowed_cidrs"))
	if err != nil || len(values) == 0 {
		return fail("restricted 运维端点必须配置非空 CIDR 白名单")
	}
	seen := make(map[netip.Prefix]struct{}, len(values))
	for _, value := range values {
		prefix, parseErr := netip.ParsePrefix(value)
		if parseErr != nil || prefix.Bits() == 0 {
			return fail("运维端点 CIDR 白名单包含非法或全网段配置")
		}
		prefix = prefix.Masked()
		if _, exists := seen[prefix]; exists {
			return fail("运维端点 CIDR 白名单包含重复项")
		}
		seen[prefix] = struct{}{}
	}
	return pass(fmt.Sprintf("运维端点由 %d 个 CIDR 白名单限制", len(values)))
}

func checkSecurityHeaders(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	if !configuration.GetBool("security_headers.enable") {
		return warn("security_headers.enable 未启用；严格发布门禁要求框架统一输出浏览器安全响应头")
	}
	return pass("浏览器安全响应头中间件已显式启用")
}

func checkTLSFiles(_ context.Context, app *framework.App) Result {
	return checkTLSFilesWithOptions(app, tlsValidationOptions{})
}

func checkRuntimeFilesystem(_ context.Context, app *framework.App) Result {
	if app == nil || strings.TrimSpace(app.RuntimePath) == "" {
		return fail("应用运行时目录为空")
	}
	info, err := os.Lstat(app.RuntimePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail("运行时路径必须是存在的非符号链接目录")
	}
	probe, err := os.CreateTemp(app.RuntimePath, ".thinkgo-deploy-check-*")
	if err != nil {
		return fail("运行时目录不可写: " + err.Error())
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(probePath)
	if err := errors.Join(closeErr, removeErr); err != nil {
		return fail("运行时写入探针清理失败: " + err.Error())
	}
	return pass("运行时目录存在、非符号链接且可写")
}

func checkDatabaseConnectivity(ctx context.Context, app *framework.App) Result {
	if databasePolicyDisabled(app) {
		return pass("数据库启动策略为 disabled，跳过连接探针")
	}
	database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	if err != nil {
		return fail("数据库服务不可用: " + err.Error())
	}
	statement := "SELECT 1 AS ready"
	if database.DialectName() == "oracle" {
		statement = "SELECT 1 AS ready FROM DUAL"
	}
	rows, err := database.QueryContext(ctx, statement)
	if err != nil || len(rows) != 1 {
		return fail(fmt.Sprintf("数据库探测失败: rows=%d err=%v", len(rows), err))
	}
	return pass("默认数据库连接可查询")
}

func checkMigrations(ctx context.Context, app *framework.App) Result {
	if databasePolicyDisabled(app) {
		return pass("数据库启动策略为 disabled，跳过迁移检查")
	}
	registry, err := framework.ResolveServiceAs[*migration.Registry](app, framework.ServiceMigration)
	if err != nil {
		return fail("迁移注册表不可用: " + err.Error())
	}
	database, err := framework.ResolveServiceAs[*db.DB](app, framework.ServiceDB)
	if err != nil {
		return fail("迁移数据库不可用: " + err.Error())
	}
	store, err := migration.NewDatabaseStore(database)
	if err != nil {
		return fail("迁移存储不可用: " + err.Error())
	}
	runner, err := migration.NewRunner(registry, store)
	if err != nil {
		return fail("迁移运行器不可用: " + err.Error())
	}
	statuses, initialized, err := runner.InspectStatus(ctx)
	if err != nil {
		return fail(err.Error())
	}
	pending := 0
	for _, status := range statuses {
		if !status.Applied {
			pending++
		}
	}
	if pending > 0 && !initialized {
		return fail(fmt.Sprintf("迁移历史尚未初始化，存在 %d 个待执行迁移", pending))
	}
	if pending > 0 {
		return fail(fmt.Sprintf("存在 %d 个待执行迁移", pending))
	}
	return pass(fmt.Sprintf("迁移历史校验通过，已注册 %d 个迁移", len(statuses)))
}

func databasePolicyDisabled(app *framework.App) bool {
	configuration, err := applicationConfig(app)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(configuration.GetString("app.database_startup_policy")), string(framework.DatabaseStartupDisabled))
}

func checkRoutes(_ context.Context, app *framework.App) Result {
	if app == nil {
		return fail("应用为空")
	}
	if err := app.LoadRoutes(); err != nil {
		return fail("加载应用路由失败: " + err.Error())
	}
	router, err := framework.ResolveServiceAs[*route.Router](app, framework.ServiceRoute)
	if err != nil {
		return fail("路由服务不可用: " + err.Error())
	}
	routes, err := router.Routes()
	if err != nil {
		return fail("冻结路由快照失败: " + err.Error())
	}
	if len(routes) == 0 {
		return warn("应用没有注册动态路由；请确认是否为纯静态服务")
	}
	return pass(fmt.Sprintf("路由快照已冻结，共 %d 条路由", len(routes)))
}

func checkTelemetry(_ context.Context, app *framework.App) Result {
	configuration, err := applicationConfig(app)
	if err != nil {
		return fail(err.Error())
	}
	if !configuration.GetBool("telemetry.enable") {
		return warn("OpenTelemetry Trace 导出未启用")
	}
	tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
	if err != nil || !tracing.Enabled() {
		return fail("telemetry.enable=true，但 OTLP Provider 未注册或初始化失败")
	}
	endpoint := strings.TrimSpace(configuration.GetString("telemetry.endpoint"))
	if strings.HasPrefix(strings.ToLower(endpoint), "http://") {
		return warn("OTLP Collector 使用明文 HTTP；请确认链路位于受信网络")
	}
	return pass("OpenTelemetry Trace Provider 已启用")
}

func checkReleaseMetadata(_ context.Context, app *framework.App) Result {
	if app == nil {
		return fail("应用为空")
	}
	missing := make([]string, 0)
	for _, name := range []string{"RELEASING.md"} {
		if err := requireRegularFile(filepath.Join(app.BasePath, name)); err != nil {
			missing = append(missing, name)
		}
	}
	moduleName, err := readModuleName(filepath.Join(app.BasePath, "go.mod"))
	if err != nil {
		return fail("读取 Go 模块路径失败: " + err.Error())
	}
	if moduleName == "thinkgo" {
		missing = append(missing, "公开 Go module 路径")
	}
	if len(missing) > 0 {
		return fail("发布元数据未完成: " + strings.Join(missing, ", "))
	}
	return pass("发布流程和模块路径已就绪")
}

func applicationConfig(app *framework.App) (*config.Config, error) {
	configuration, err := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
	if err != nil {
		return nil, fmt.Errorf("应用配置不可用: %w", err)
	}
	return configuration, nil
}

func stringList(value interface{}) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		result := append([]string(nil), typed...)
		for index := range result {
			result[index] = strings.TrimSpace(result[index])
			if result[index] == "" {
				return nil, errors.New("列表包含空字符串")
			}
		}
		return result, nil
	case []interface{}:
		result := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, errors.New("列表元素必须是非空字符串")
			}
			result[index] = strings.TrimSpace(text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("列表类型非法: %T", value)
	}
}

func resolveApplicationPath(basePath, configured string) string {
	configured = strings.TrimSpace(configured)
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured)
	}
	return filepath.Join(basePath, configured)
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("不是普通非符号链接文件")
	}
	return nil
}

func readModuleName(path string) (string, error) {
	cleaned := filepath.Clean(path)
	root, err := os.OpenRoot(filepath.Dir(cleaned))
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(cleaned)
	information, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.Mode().IsRegular() {
		return "", errors.New("go.mod 不是普通非符号链接文件")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	openedInformation, err := file.Stat()
	if err != nil || !openedInformation.Mode().IsRegular() || !os.SameFile(information, openedInformation) {
		return "", errors.New("go.mod 在打开期间发生变化")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumModuleFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(content) > maximumModuleFileBytes {
		return "", errors.New("go.mod 超过 1 MiB")
	}
	currentInformation, err := root.Lstat(name)
	if err != nil || currentInformation.Mode()&os.ModeSymlink != 0 || !os.SameFile(currentInformation, openedInformation) {
		return "", errors.New("go.mod 在读取期间发生变化")
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", errors.New("go.mod 缺少 module 声明")
}

func pass(message string) Result { return Result{Level: LevelPass, Message: message} }
func warn(message string) Result { return Result{Level: LevelWarn, Message: message} }
func fail(message string) Result { return Result{Level: LevelFail, Message: message} }
