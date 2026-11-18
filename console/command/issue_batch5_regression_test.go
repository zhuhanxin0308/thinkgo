package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestIssue606OptimizeConfigRefreshesStaleCacheIgnoresAmbientEnvAndHonorsRuntimePath
// 验证连续执行 optimize:config 会重新读取最新源配置而非回写旧缓存、不固化临时环境变量，并遵循 SetRuntimePath。
func TestIssue606OptimizeConfigRefreshesStaleCacheIgnoresAmbientEnvAndHonorsRuntimePath(t *testing.T) {
	basePath := t.TempDir()
	ensureConsoleTestConfigFiles(t, basePath)
	appConfigPath := filepath.Join(basePath, "config", "app.json")
	if err := os.WriteFile(appConfigPath, []byte(`{"app_name":"v1","app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_NAME", "ephemeral-ci-override")

	app1 := buildConsoleTestApp(t, basePath)
	out1 := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := (&OptimizeConfig{Command: console.Command{App: app1}}).Execute(console.NewInput(), out1); err != nil {
		t.Fatalf("首次执行 optimize:config 失败: %v", err)
	}

	firstCache, err := os.ReadFile(filepath.Join(basePath, "runtime", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(firstCache), "ephemeral-ci-override") || !strings.Contains(string(firstCache), `"v1"`) {
		t.Fatalf("配置缓存不应固化构建机临时环境变量: %s", firstCache)
	}

	// 修改源配置为 v2_updated，即使新 App 初始化时已消费旧缓存 v1，二次执行 optimize:config 也必须写出 v2_updated。
	if err := os.WriteFile(appConfigPath, []byte(`{"app_name":"v2_updated","app_env":"test","server":{"host":"127.0.0.1","port":8080},"compression":{"enable":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	app2 := buildConsoleTestApp(t, basePath)
	out2 := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := (&OptimizeConfig{Command: console.Command{App: app2}}).Execute(console.NewInput(), out2); err != nil {
		t.Fatalf("二次执行 optimize:config 失败: %v", err)
	}
	secondCache, err := os.ReadFile(filepath.Join(basePath, "runtime", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secondCache), `"v2_updated"`) || strings.Contains(string(secondCache), `"v1"`) {
		t.Fatalf("二次执行 optimize:config 仍回写了过期旧缓存: %s", secondCache)
	}

	// 外部 SetRuntimePath 也应把 config.json 写入指定运行目录。
	externalRuntime := t.TempDir()
	app3 := buildConsoleTestApp(t, basePath)
	if err := app3.SetRuntimePath(externalRuntime); err != nil {
		t.Fatal(err)
	}
	if err := (&OptimizeConfig{Command: console.Command{App: app3}}).Execute(console.NewInput(), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("外部 runtime optimize:config 失败: %v", err)
	}
	externalCache, err := os.ReadFile(filepath.Join(externalRuntime, "config.json"))
	if err != nil || !strings.Contains(string(externalCache), `"v2_updated"`) {
		t.Fatalf("外部 runtime 未写入最新配置缓存: err=%v content=%s", err, externalCache)
	}

	// runtime/config.json 损坏后，Clear 命令仍能自愈清理缓存。
	if err := os.WriteFile(filepath.Join(basePath, "runtime", "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	uninitProject := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = uninitProject.Close() })
	noop := func(*framework.App) error { return nil }
	if err := os.MkdirAll(filepath.Join(basePath, "app", "index"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := uninitProject.RegisterApplications(noop, framework.ApplicationDefinition{Name: "index", Register: noop}); err != nil {
		t.Fatal(err)
	}
	clearCmd := &Clear{Command: console.Command{App: uninitProject}}
	if err := clearCmd.Execute(console.NewInput(), console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)); err != nil {
		t.Fatalf("损坏配置缓存不应阻断 Clear 自愈: %v", err)
	}
}

// TestIssue607DiscoveryGenericAliasesInternalDirsPackageVarsAndOpenAPI 验证 Go 1.27 泛型别名、
// internal/testdata/隐藏目录过滤、包级 var 常量长度求值以及 OpenAPI 类型参数作用域与下划线目录。
func TestIssue607DiscoveryGenericAliasesInternalDirsPackageVarsAndOpenAPI(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	writeDiscoveryFixture(t, basePath, "main.go", "package main\n\nfunc main() {}\n")

	// 1. 共享 internal、testdata、隐藏目录均不得被误当作应用或控制器包导入。
	writeDiscoveryFixture(t, basePath, "app/internal/secret/secret.go", "package secret\n\ntype InternalHelper struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/testdata/fixture.go", "package testdata\n\ntype IgnoredApp struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/.hidden/skip.go", "package hidden\n\ntype HiddenApp struct{}\n")
	writeDiscoveryFixture(t, basePath, "app/index/controller/internal/helper/helper.go", "package helper\n\ntype PrivateCtrl struct{}\n")

	// 2. 业务包同时包含未实例化泛型类型别名、已实例化别名以及包级 var 数组长度常量表达式。
	writeDiscoveryFixture(t, basePath, "app/index/controller/home.go", `package controller

import "unsafe"

var magicBytes = [...]byte{'T', 'G', 'O'}
var sampleWord uint64

type Header [len(magicBytes) + int(unsafe.Sizeof(sampleWord))]byte

type GenericBox[T any] struct {
	Value T
}

type GenericAlias[T any] = GenericBox[T]
type InstantiatedAlias = GenericBox[string]

type Home struct {
	Header Header
}

func (*Home) Index() string { return "ok" }
`)
	writeDiscoveryFixture(t, basePath, "app/index/command/generic.go", `package command

import "github.com/zhuhanxin0308/thinkgo/v3/console"

type GenericCmd[T any] = struct {
	console.Command
	Payload T
}

type SyncCmd struct {
	console.Command
}

func (c *SyncCmd) Configure() {
	c.Signature = "sync:demo"
	c.Description = "演示命令"
}

func (c *SyncCmd) Execute(*console.Input, *console.Output) error { return nil }
`)

	// 3. 下划线前缀应用与泛型类型形参同名遮蔽。
	writeDiscoveryFixture(t, basePath, "app/_admin/controller/panel.go", `package controller

type Box[T any] struct {
	Value T
}

// Alias 验证类型形参 Item 不会被误判为同名结构体引用。
type Alias[Item any] = Box[Item]

// Item 后台条目说明。
type Item struct {
	// Title 标题说明。
	Title Alias[string] `+"`json:\"title\"`"+`
}

type Panel struct{}

// Dashboard 后台概览。
//
// 返回概览指标。
func (*Panel) Dashboard() Item { return Item{} }
`)

	// 同时直接覆盖 AST 裸形参遮蔽场景 type RawAlias[Item any] = Item; type Item RawAlias[string]。
	fset := token.NewFileSet()
	shadowAST, err := parser.ParseFile(fset, "shadow.go", "package p\ntype RawAlias[Item any] = Item\ntype Item RawAlias[string]\n", parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := inheritAPITypeComments(make(binding.Documentation), []commentSourceFile{{packagePath: "example.com/p", file: shadowAST}}, nil, fset); err != nil {
		t.Fatalf("泛型类型形参遮蔽不应误判为循环定义: %v", err)
	}

	app := &framework.App{BasePath: basePath, ApplicationPath: filepath.Join(basePath, "app")}
	discoverCmd := &ServiceDiscover{Command: console.Command{App: app}}
	if err := discoverCmd.Execute(console.NewInput(), generatorTestOutput()); err != nil {
		t.Fatalf("service:discover 处理合法 Go 1.27 源码失败: %v", err)
	}

	commands, err := DiscoverProjectCommandsContext(context.Background(), basePath)
	if err != nil {
		t.Fatalf("发现项目命令失败: %v", err)
	}
	if len(commands) != 1 || commands[0].TypeName != "SyncCmd" {
		t.Fatalf("未实例化泛型命令别名应被过滤，实际发现: %#v", commands)
	}

	comments, err := scanOpenAPIComments(context.Background(), basePath)
	if err != nil {
		t.Fatalf("scanOpenAPIComments 不应因泛型形参遮蔽或下划线目录报错: %v", err)
	}
	itemDoc := comments.Types["example.com/project/app/_admin/controller.Item"]
	if !strings.Contains(itemDoc.Description, "后台条目说明") || !strings.Contains(itemDoc.Fields["Title"], "标题说明") {
		t.Fatalf("下划线前缀应用 _admin 的 OpenAPI 注释未被扫描到: %#v", comments.Types)
	}

	build := exec.Command("go", "build", "-mod=mod", "./...")
	build.Dir = basePath
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("生成的 autoload_generated.go 无法通过 go build ./...: %v\n%s", err, out)
	}
}

// TestIssue608GeneratorsRejectBuiltinSignaturesSupportNestedCRUDAndIdempotentSuffix
// 验证 make:command 拦截内置与重复签名、make:crud 支持分层目录、make:controller 后缀幂等以及 isValidRunHost IPv6 校验。
func TestIssue608GeneratorsRejectBuiltinSignaturesSupportNestedCRUDAndIdempotentSuffix(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	writeDiscoveryFixture(t, basePath, "main.go", "package main\n\nfunc main() {}\n")

	app := buildConsoleTestApp(t, basePath)

	// 1. make:command 必须拒绝全部内置命令同名签名以及已有自定义命令重复签名。
	makeCmd := &MakeCommand{Command: console.Command{App: app}}
	makeCmd.Configure()
	for _, args := range [][]string{{"Version"}, {"List"}, {"Migrate"}, {"CustomReport", "route:list"}} {
		inp := console.NewInput(args...)
		if err := inp.Parse(makeCmd.GetArgumentDefinitions(), makeCmd.GetOptionDefinitions()); err != nil {
			t.Fatal(err)
		}
		if err := makeCmd.Execute(inp, generatorTestOutput()); !errors.Is(err, console.ErrDuplicateCommand) {
			t.Fatalf("make:command %v 应拒绝与内置命令重名: %v", args, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(basePath, "app", "index", "command", "version.go")); !os.IsNotExist(statErr) {
		t.Fatalf("被拦截的内置同名命令不应落盘: %v", statErr)
	}

	firstInp := console.NewInput("SyncOrder", "order:sync")
	if err := firstInp.Parse(makeCmd.GetArgumentDefinitions(), makeCmd.GetOptionDefinitions()); err != nil {
		t.Fatal(err)
	}
	if err := makeCmd.Execute(firstInp, generatorTestOutput()); err != nil {
		t.Fatalf("创建合法自定义命令失败: %v", err)
	}
	dupInp := console.NewInput("AnotherSync", "order:sync")
	if err := dupInp.Parse(makeCmd.GetArgumentDefinitions(), makeCmd.GetOptionDefinitions()); err != nil {
		t.Fatal(err)
	}
	if err := makeCmd.Execute(dupInp, generatorTestOutput()); !errors.Is(err, console.ErrDuplicateCommand) {
		t.Fatalf("重复的项目命令签名 order:sync 应被拒绝: %v", err)
	}

	// 2. make:controller 在开启 controller_suffix=true 时对已有后缀保持幂等。
	if err := app.Config().Set("route.controller_suffix", true); err != nil {
		t.Fatal(err)
	}
	makeCtrl := &MakeController{Command: console.Command{App: app}}
	makeCtrl.Configure()
	ctrlInp := console.NewInput("UserController")
	if err := ctrlInp.Parse(makeCtrl.GetArgumentDefinitions(), makeCtrl.GetOptionDefinitions()); err != nil {
		t.Fatal(err)
	}
	if err := makeCtrl.Execute(ctrlInp, generatorTestOutput()); err != nil {
		t.Fatalf("make:controller UserController 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "index", "controller", "usercontroller.go")); err != nil {
		t.Fatalf("预期生成 usercontroller.go 而非双写后缀文件: %v", err)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "index", "controller", "usercontrollercontroller.go")); !os.IsNotExist(err) {
		t.Fatal("不应生成双写后缀的 usercontrollercontroller.go")
	}

	// 3. make:crud 分层目标 admin@account/User 必须绑定 app/admin/model/account 而非误绑根 model。
	writeDiscoveryFixture(t, basePath, "app/admin/model/user.go", `package model

import "github.com/zhuhanxin0308/thinkgo/v3/db"

type User struct {
	*db.Model
	ID int64 `+"`thinkgo:\"id\" json:\"id\"`"+`
	WrongRootField string `+"`thinkgo:\"wrong_root\" json:\"wrong_root\"`"+`
}
`)
	writeDiscoveryFixture(t, basePath, "app/admin/model/account/user.go", `package account

import "github.com/zhuhanxin0308/thinkgo/v3/db"

type User struct {
	*db.Model
	ID int64 `+"`thinkgo:\"id\" json:\"id\"`"+`
	AccountEmail string `+"`thinkgo:\"account_email\" json:\"account_email\" validate:\"required\"`"+`
}
`)
	makeCRUD := &MakeCRUD{Command: console.Command{App: app}}
	makeCRUD.Configure()
	crudInp := &console.Input{
		Args:    []string{"admin@account/User"},
		Options: map[string]string{"write": "AccountEmail", "read": "ID,AccountEmail", "path": "/accounts"},
	}
	if err := makeCRUD.Execute(crudInp, generatorTestOutput()); err != nil {
		t.Fatalf("make:crud admin@account/User 失败: %v", err)
	}
	generatedCRUD, err := os.ReadFile(filepath.Join(basePath, "app", "admin", "api", "account", "user.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(generatedCRUD), "package account\n") || !strings.Contains(string(generatedCRUD), "AccountEmail") || strings.Contains(string(generatedCRUD), "WrongRootField") {
		t.Fatalf("分层 CRUD 绑定了错误包名或根模型: %s", generatedCRUD)
	}
	testRun := exec.Command("go", "test", "-mod=mod", "./app/admin/api/account", "-count=1")
	testRun.Dir = basePath
	testRun.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if out, err := testRun.CombinedOutput(); err != nil {
		t.Fatalf("分层生成的 CRUD 测试未通过: %v\n%s", err, out)
	}

	// 4. isValidRunHost 支持合法 [::1]，拒绝 [127.0.0.1] 与非法括号。
	for _, validHost := range []string{"127.0.0.1", "localhost", "[::1]", "[fe80::1]"} {
		if !isValidRunHost(validHost) {
			t.Fatalf("合法开发监听主机 %q 被错误拒绝", validHost)
		}
	}
	for _, invalidHost := range []string{"[127.0.0.1]", "[localhost]", "[[::1]]", "[::1", "::", "127.0.0.1:8080", "-bad.host"} {
		if isValidRunHost(invalidHost) {
			t.Fatalf("非法开发监听主机 %q 未被拒绝", invalidHost)
		}
	}
}

// TestIssue565CreateProjectRestrictsConfigFilePermissions 验证 thinkgo create 生成的 config/*.json
// （及子应用 config/*.json）使用 0600 私有权限，而普通源码和静态资源保持 0644。
func TestIssue565CreateProjectRestrictsConfigFilePermissions(t *testing.T) {
	base := t.TempDir()
	err := createProject(context.Background(), base, "secure-demo", func(_ context.Context, directory, _ string) error {
		// 模拟子应用配置文件场景并校验暂存阶段写入权限。
		subCfg := filepath.Join(directory, "app", "index", "config", "secret.json")
		return os.WriteFile(subCfg, []byte(`{"token":""}`), 0o600)
	})
	if err != nil {
		t.Fatalf("createProject 失败: %v", err)
	}

	projectDir := filepath.Join(base, "secure-demo")
	configFiles := []string{
		filepath.Join(projectDir, "config", "database.json"),
		filepath.Join(projectDir, "config", "cookie.json"),
		filepath.Join(projectDir, "config", "app.json"),
		filepath.Join(projectDir, "app", "index", "config", "secret.json"),
	}
	publicFiles := []string{
		filepath.Join(projectDir, "main.go"),
		filepath.Join(projectDir, "app", "index", "controller", "index.go"),
		filepath.Join(projectDir, "public", "index.html"),
	}

	for _, cfgPath := range configFiles {
		content, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("读取生成配置文件 %s 失败: %v", cfgPath, err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(content, &parsed); err != nil {
			t.Fatalf("生成配置文件 %s JSON 非法: %v", cfgPath, err)
		}
		if !isDistributionConfig(strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(cfgPath, projectDir)), "/")) {
			t.Fatalf("配置文件 %s 未被识别为敏感配置路径", cfgPath)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("配置文件 %s 权限必须为 0600，实际为 %#o", cfgPath, info.Mode().Perm())
			}
		}
	}
	for _, pubPath := range publicFiles {
		if isDistributionConfig(strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(pubPath, projectDir)), "/")) {
			t.Fatalf("非配置文件 %s 不应被归类为敏感配置", pubPath)
		}
		if runtime.GOOS != "windows" {
			info, err := os.Stat(pubPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("源码/静态资源 %s 权限应保持 0644，实际为 %#o", pubPath, info.Mode().Perm())
			}
		}
	}
}
