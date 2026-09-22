package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	frameworkRoute "github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestOptimizeBuildsConsumedStartupCache 验证 optimize 只生成具有启动消费方的配置缓存。
func TestOptimizeBuildsConsumedStartupCache(t *testing.T) {
	basePath := t.TempDir()
	application := buildConsoleTestApp(t, basePath)
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	command := &Optimize{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行 optimize 失败: %v", err)
	}
	for _, stage := range []string{"Succeed!"} {
		if !strings.Contains(stdout.String(), stage) {
			t.Fatalf("optimize 输出缺少阶段 %q: %q", stage, stdout.String())
		}
	}
	for _, cacheFile := range []string{"config.json"} {
		if information, err := os.Stat(filepath.Join(basePath, "runtime", cacheFile)); err != nil || !information.Mode().IsRegular() {
			t.Fatalf("optimize 未生成 %s: info=%#v err=%v", cacheFile, information, err)
		}
	}
	if _, err := os.Stat(filepath.Join(basePath, "runtime", "route.json")); !os.IsNotExist(err) {
		t.Fatalf("optimize 不应隐式导出路由: %v", err)
	}
	if err := (&Optimize{}).Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用 optimize 应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := command.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出 optimize 应返回 ErrInvalidOutput，实际为 %v", err)
	}
}

// TestOptimizeDirectoryVariantsRequireCompiledApplication 验证配置优化允许显式
// 配置目录，而路由和模型优化要求目标应用已进入编译清单。
func TestOptimizeDirectoryVariantsRequireCompiledApplication(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	writeDiscoveryFixture(t, basePath, "app/tenant/config/custom.json", `{"feature":"tenant"}`)
	application := buildConsoleTestApp(t, basePath)
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)

	configCommand := &OptimizeConfig{Command: console.Command{App: application}}
	snapshot, snapshotErr := optimizedConfigSnapshot(application, "tenant")
	if snapshotErr != nil || snapshot["custom"] == nil {
		t.Fatalf("指定目录配置快照错误: snapshot=%#v err=%v", snapshot, snapshotErr)
	}
	if err := configCommand.Execute(console.NewInput("tenant"), output); err != nil {
		t.Fatalf("执行指定目录配置优化失败: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(basePath, "runtime", "tenant", "config.json"))
	if err != nil || !bytes.Contains(content, []byte(`"custom"`)) || !bytes.Contains(content, []byte(`"tenant"`)) {
		t.Fatalf("指定目录配置缓存错误: content=%s output=%q err=%v", content, stdout.String(), err)
	}

	stdout.Reset()
	if err := configCommand.Execute(console.NewInput("missing"), output); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("缺失配置目录必须返回错误: %v", err)
	}
	if strings.Contains(stdout.String(), "Succeed!") {
		t.Fatalf("缺失配置目录输出错误: %q", stdout.String())
	}

	stdout.Reset()
	routeCommand := &RouteExport{Command: console.Command{App: application}}
	if err := routeCommand.Execute(console.NewInput("tenant"), output); err == nil || !strings.Contains(err.Error(), "cannot be loaded") {
		t.Fatalf("未编译应用的路由导出必须失败: %v", err)
	}
	if strings.Contains(stdout.String(), "Succeed!") {
		t.Fatalf("路由导出失败仍输出成功: %q", stdout.String())
	}

	schemaCommand := &SchemaValidate{Command: console.Command{App: application}}
	if err := schemaCommand.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("无模型时结构预热应成功: %v", err)
	}
	if err := schemaCommand.Execute(console.NewInput("tenant"), output); err == nil || !strings.Contains(err.Error(), filepath.Join("app", "tenant", "model")) {
		t.Fatalf("模型目录边界错误: %v", err)
	}
	if _, err := optimizeDirectory(console.NewInput("../escape")); err == nil {
		t.Fatal("优化目录不得越过项目根目录")
	}
}

// TestRouteCacheHandlerIdentityCoversDocumentedHandlers 验证路由优化清单可以稳定
// 标识框架函数、net/http 函数、Handler 和控制器字符串。
func TestRouteCacheHandlerIdentityCoversDocumentedHandlers(t *testing.T) {
	frameworkHandler := func(*fwcontext.Request) *fwcontext.Response {
		return fwcontext.NewResponse().Content("framework")
	}
	standardFunction := func(http.ResponseWriter, *http.Request) {}
	standardHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	routes := []frameworkRoute.RouteInfo{
		{Method: http.MethodGet, Path: "/controller", Handler: "Index/show"},
		{Method: http.MethodGet, Path: "/framework", Handler: frameworkHandler},
		{Method: http.MethodGet, Path: "/function", Handler: standardFunction},
		{Method: http.MethodGet, Path: "/handler", Handler: standardHandler},
	}
	manifest, err := buildRouteCacheManifest(routes)
	if err != nil || len(manifest) != len(routes) {
		t.Fatalf("构建完整路由缓存清单失败: manifest=%#v err=%v", manifest, err)
	}
	if manifest[0].Handler != "Index/show" || manifest[1].Handler == "<Closure>" || manifest[2].Handler == "<Closure>" || manifest[3].Handler != "http.HandlerFunc" {
		t.Fatalf("路由处理器标识错误: %#v", manifest)
	}
	var nilFunction func(*fwcontext.Request) *fwcontext.Response
	if identity, err := routeHandlerIdentity(nilFunction); err != nil || identity != "<Closure>" {
		t.Fatalf("nil 函数应使用稳定闭包标识: identity=%q err=%v", identity, err)
	}
	if _, err := buildRouteCacheManifest([]frameworkRoute.RouteInfo{{Method: http.MethodGet, Path: "/invalid", Handler: 42}}); err == nil {
		t.Fatal("未知路由处理器不得写入部署缓存")
	}
}

// TestHelpCommandUsesConsoleRegistry 验证 help 默认展示自身，也可按 ThinkPHP
// 调用方式展示指定命令并接受 --raw。
func TestHelpCommandUsesConsoleRegistry(t *testing.T) {
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	cli := console.NewConsole(nil)
	if err := cli.SetOutput(output); err != nil {
		t.Fatalf("设置命令输出失败: %v", err)
	}
	help := &Help{Console: cli}
	if err := cli.Register(help); err != nil {
		t.Fatalf("注册 help 命令失败: %v", err)
	}
	if err := cli.Register(&Version{}); err != nil {
		t.Fatalf("注册 version 命令失败: %v", err)
	}
	if err := cli.Run("help", "version", "--raw"); err != nil {
		t.Fatalf("执行 help version --raw 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Show version information") || !strings.Contains(stdout.String(), "thinkgo version") {
		t.Fatalf("version 帮助输出错误: %q", stdout.String())
	}
	stdout.Reset()
	if err := help.Execute(nil, output); err != nil || !strings.Contains(stdout.String(), "thinkgo help") {
		t.Fatalf("默认 help 输出错误: output=%q err=%v", stdout.String(), err)
	}
	if err := (&Help{}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("缺少 Console 的 help 应返回错误")
	}
	if err := help.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出 help 应返回 ErrInvalidOutput，实际为 %v", err)
	}
}

// TestServiceDiscoveryCheckDetectsStaleAssembly 验证 service:discover 的只读校验
// 能区分当前、过期和缺失的原生多应用装配文件。
func TestServiceDiscoveryCheckDetectsStaleAssembly(t *testing.T) {
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/discovery")
	writeDiscoveryFixture(t, basePath, "app/index/app.go", "package index\n")
	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	command := &ServiceDiscover{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行 service:discover 失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "Succeed!") {
		t.Fatalf("service:discover 成功输出错误: %q", stdout.String())
	}
	if err := CheckControllerDiscovery(application); err != nil {
		t.Fatalf("刚生成的发现文件应通过校验: %v", err)
	}
	generatedPath := filepath.Join(basePath, "app", applicationDiscoveryFilename)
	if err := os.WriteFile(generatedPath, []byte("package app\n"), 0o644); err != nil {
		t.Fatalf("构造过期发现文件失败: %v", err)
	}
	if err := CheckControllerDiscovery(application); err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Fatalf("过期发现文件应被识别，实际为 %v", err)
	}
	if err := os.Remove(generatedPath); err != nil {
		t.Fatalf("删除发现文件失败: %v", err)
	}
	if err := CheckControllerDiscovery(application); err == nil || !strings.Contains(err.Error(), "读取控制器发现文件") {
		t.Fatalf("缺失发现文件应被识别，实际为 %v", err)
	}
	if err := CheckControllerDiscovery(nil); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用校验应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := command.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出 service:discover 应返回 ErrInvalidOutput，实际为 %v", err)
	}
}

// TestGeneratorHelpersKeepApplicationAssemblyAtomic 验证自动发现类型生成失败时会
// 回滚本次源码，并覆盖生成器统一输入、缓存解析和安全名称规范。
func TestGeneratorHelpersKeepApplicationAssemblyAtomic(t *testing.T) {
	basePath := t.TempDir()
	application := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	source := []byte("package model\n\ntype User struct{}\n")
	err := writeAndRefreshGeneratedApplicationSource(application, generatorTarget{application: "index", name: "User"}, "model", "user.go", source)
	if err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("缺少 go.mod 时刷新发现入口应失败，实际为 %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(basePath, "app", "index", "model", "user.go")); !os.IsNotExist(statErr) {
		t.Fatalf("刷新失败后必须回滚模型源码，实际为 %v", statErr)
	}

	initialized := buildConsoleTestApp(t, t.TempDir())
	if resolved, err := resolveApplicationCache(initialized); err != nil || resolved == nil {
		t.Fatalf("命令层缓存服务解析失败: cache=%#v err=%v", resolved, err)
	}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	baseCommand := &console.Command{App: initialized}
	name, err := normalizedGeneratorInput(baseCommand, console.NewInput("user-profile"), output, "Service")
	if err != nil || name != "UserProfileService" {
		t.Fatalf("生成器名称规范错误: name=%q err=%v", name, err)
	}
	for index, call := range []func() error{
		func() error {
			_, callErr := normalizedGeneratorInput(nil, console.NewInput("User"), output, "")
			return callErr
		},
		func() error { _, callErr := normalizedGeneratorInput(baseCommand, nil, output, ""); return callErr },
		func() error {
			_, callErr := normalizedGeneratorInput(baseCommand, console.NewInput("User"), nil, "")
			return callErr
		},
		func() error {
			_, callErr := normalizedGeneratorInput(&console.Command{}, console.NewInput("User"), output, "")
			return callErr
		},
	} {
		if call() == nil {
			t.Fatalf("第 %d 个无效生成器上下文必须返回错误", index+1)
		}
	}
	if err := removeGeneratedAppSource(nil, "app/model", "user.go"); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用回滚应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := removeGeneratedAppSource(initialized, filepath.Join(initialized.BasePath, "app"), "user.go"); err == nil {
		t.Fatal("回滚路径不得使用绝对目录")
	}
}

// TestRuntimeAndVendorPathContracts 验证命令生成和 vendor:publish 共享的路径、
// Composer 元数据形态与配置映射均保持严格且可预测。
func TestRuntimeAndVendorPathContracts(t *testing.T) {
	basePath := t.TempDir()
	inside, err := projectRelativePath(basePath, filepath.Join(basePath, "runtime", "cache.json"))
	if err != nil || inside != filepath.Join("runtime", "cache.json") {
		t.Fatalf("项目内路径规范错误: path=%q err=%v", inside, err)
	}
	for _, target := range []string{"", " spaced ", basePath, filepath.Join(basePath, "..", "outside.json")} {
		if _, err := projectRelativePath(basePath, target); err == nil {
			t.Fatalf("不安全项目路径 %q 必须被拒绝", target)
		}
	}
	for _, valid := range []string{"", "tenant", "租户"} {
		if _, err := safeOptionalDirectory(valid); err != nil {
			t.Fatalf("安全可选目录 %q 被误拒绝: %v", valid, err)
		}
	}
	for _, invalid := range []string{".", "..", "a/b", `a\b`, "bad\nname"} {
		if _, err := safeOptionalDirectory(invalid); err == nil {
			t.Fatalf("不安全可选目录 %q 必须被拒绝", invalid)
		}
	}

	arrayPackages, err := decodeComposerPackages([]byte(`[{"name":"vendor/package"}]`))
	if err != nil || len(arrayPackages) != 1 {
		t.Fatalf("Composer 数组元数据解析失败: packages=%#v err=%v", arrayPackages, err)
	}
	wrapperPackages, err := decodeComposerPackages([]byte(`{"packages":[{"name":"vendor/package"}]}`))
	if err != nil || len(wrapperPackages) != 1 {
		t.Fatalf("Composer 包装元数据解析失败: packages=%#v err=%v", wrapperPackages, err)
	}
	for _, invalid := range [][]byte{nil, []byte(`{}` + "\n" + `{}`), []byte(`true`)} {
		if _, err := decodeComposerPackages(invalid); err == nil {
			t.Fatalf("非法 Composer 元数据 %q 必须被拒绝", invalid)
		}
	}

	configCases := []struct {
		raw  string
		want map[string]string
	}{
		{raw: `"config/app.php"`, want: map[string]string{"0": "config/app.php"}},
		{raw: `["config/app.php","config/cache.php"]`, want: map[string]string{"0": "config/app.php", "1": "config/cache.php"}},
		{raw: `{"app":"config/app.php"}`, want: map[string]string{"app": "config/app.php"}},
		{raw: `null`, want: map[string]string{}},
	}
	for _, testCase := range configCases {
		actual, err := decodePublishConfigMap(json.RawMessage(testCase.raw))
		if err != nil || len(actual) != len(testCase.want) {
			t.Fatalf("发布配置映射解析失败: raw=%s actual=%#v err=%v", testCase.raw, actual, err)
		}
		for key, value := range testCase.want {
			if actual[key] != value {
				t.Fatalf("发布配置映射错误: raw=%s want=%#v actual=%#v", testCase.raw, testCase.want, actual)
			}
		}
	}
	if _, err := decodePublishConfigMap(json.RawMessage(`[1]`)); err == nil {
		t.Fatal("非字符串发布配置列表必须被拒绝")
	}
}

// TestRouteListMoreAndEverySortColumn 验证 route:list 的详细输出及所有公开排序名。
func TestRouteListMoreAndEverySortColumn(t *testing.T) {
	rows := []routeListRow{
		{rule: "/z", handler: "Alpha/show", method: http.MethodPost, name: "zeta", domain: "z.example.com"},
		{rule: "/a", handler: "Zulu/show", method: http.MethodGet, name: "alpha", domain: "a.example.com"},
	}
	for _, column := range []string{"rule", "route", "method", "name", "domain", "0", "1", "2", "3", "4"} {
		copied := append([]routeListRow(nil), rows...)
		if err := sortRouteListRows(copied, column); err != nil {
			t.Fatalf("按 %q 排序失败: %v", column, err)
		}
	}
	if err := sortRouteListRows(rows, "unknown"); err == nil {
		t.Fatal("未知 route:list 排序列必须被拒绝")
	}

	application := buildConsoleTestApp(t, t.TempDir())
	router, err := resolveApplicationRoute(application)
	if err != nil {
		t.Fatalf("解析 route:list 路由器失败: %v", err)
	}
	registered, err := router.Get("/users", "User/index")
	if err != nil {
		t.Fatalf("注册 route:list 测试路由失败: %v", err)
	}
	if err := registered.WithName("users.index"); err != nil {
		t.Fatalf("设置 route:list 路由名称失败: %v", err)
	}
	if err := registered.WithDomain("api.example.com"); err != nil {
		t.Fatalf("设置 route:list 路由域名失败: %v", err)
	}
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	command := &RouteList{Command: console.Command{App: application}}
	if err := command.Execute(console.NewInput("--more", "--sort=domain"), output); err != nil {
		t.Fatalf("执行详细 route:list 失败: %v", err)
	}
	for _, expected := range []string{"Domain", "Option", "Pattern", "api.example.com", "users.index"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("详细 route:list 输出缺少 %q: %q", expected, stdout.String())
		}
	}
}
