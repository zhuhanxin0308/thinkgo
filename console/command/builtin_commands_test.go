package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// TestBuiltInCommandDefinitions 验证内置命令公开签名及参数声明，
// 并确保框架重复配置命令时不会产生重复定义。
func TestBuiltInCommandDefinitions(t *testing.T) {
	testCases := []struct {
		command      console.ICommand
		signature    string
		argumentName string
		required     bool
		optionNames  []string
	}{
		{command: &Build{}, signature: "build", argumentName: "target", optionNames: []string{"goarm"}},
		{command: &Create{}, signature: "create", argumentName: "name", required: true},
		{command: &MakeApp{}, signature: "make:app", argumentName: "app"},
		{command: &Clear{}, signature: "clear", argumentName: "app", optionNames: []string{"path", "cache", "log", "dir", "expire"}},
		{command: &ConfigDump{}, signature: "config:dump", argumentName: "name", optionNames: []string{"app"}},
		{command: &Help{}, signature: "help", argumentName: "command_name", optionNames: []string{"raw"}},
		{command: &List{}, signature: "list", argumentName: "namespace", optionNames: []string{"raw"}},
		{command: &RouteList{}, signature: "route:list", argumentName: "style", optionNames: []string{"app", "sort", "more"}},
		{command: &Run{}, signature: "run", optionNames: []string{"host", "port", "root"}},
		{command: &Migrate{}, signature: "migrate", optionNames: []string{"app"}},
		{command: &MigrateRollback{}, signature: "migrate:rollback", optionNames: []string{"app", "batches"}},
		{command: &MigrateStatus{}, signature: "migrate:status", optionNames: []string{"app"}},
		{command: &DeployCheck{}, signature: "deploy:check", optionNames: []string{"app", "strict"}},
		{command: &Version{}, signature: "version"},
		{command: &ServiceDiscover{}, signature: "service:discover"},
		{command: &Optimize{}, signature: "optimize"},
		{command: &OptimizeConfig{}, signature: "optimize:config", argumentName: "dir"},
		{command: &OptimizeRoute{}, signature: "optimize:route", argumentName: "dir"},
		{command: &OptimizeSchema{}, signature: "optimize:schema", argumentName: "dir", optionNames: []string{"connection", "table"}},
		{command: &RouteExport{}, signature: "route:export", argumentName: "dir"},
		{command: &SchemaValidate{}, signature: "schema:validate", argumentName: "dir", optionNames: []string{"connection", "table"}},
		{command: &VendorPublish{}, signature: "vendor:publish", optionNames: []string{"force"}},
		{command: &MakeCommand{}, signature: "make:command", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeController{}, signature: "make:controller", argumentName: "name", required: true, optionNames: []string{"app", "api", "plain"}},
		{command: &MakeEvent{}, signature: "make:event", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeListener{}, signature: "make:listener", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeMiddleware{}, signature: "make:middleware", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeModel{}, signature: "make:model", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &OpenAPIGenerate{}, signature: "openapi:generate", optionNames: []string{"check"}},
		{command: &MakeService{}, signature: "make:service", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeSubscribe{}, signature: "make:subscribe", argumentName: "name", required: true, optionNames: []string{"app"}},
		{command: &MakeValidate{}, signature: "make:validate", argumentName: "name", required: true, optionNames: []string{"app"}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.signature, func(t *testing.T) {
			testCase.command.Configure()
			testCase.command.Configure()
			if testCase.command.GetSignature() != testCase.signature || strings.TrimSpace(testCase.command.GetDescription()) == "" {
				t.Fatalf("命令元数据错误: signature=%q description=%q", testCase.command.GetSignature(), testCase.command.GetDescription())
			}
			arguments := testCase.command.GetArgumentDefinitions()
			if testCase.argumentName == "" {
				if len(arguments) != 0 {
					t.Fatalf("命令不应声明位置参数: %v", arguments)
				}
			} else if len(arguments) == 0 || arguments[0].Name != testCase.argumentName || arguments[0].Required != testCase.required {
				t.Fatalf("位置参数声明错误: %v", arguments)
			}
			expectedCount := 1
			if testCase.argumentName == "" {
				expectedCount = 0
			}
			if testCase.signature == "make:command" {
				expectedCount = 2
				if arguments[1].Name != "commandName" || arguments[1].Required {
					t.Fatalf("可选命令签名声明错误: %v", arguments)
				}
			}
			if len(arguments) != expectedCount {
				t.Fatalf("位置参数数量错误: %v", arguments)
			}
			options := testCase.command.GetOptionDefinitions()
			if len(options) != len(testCase.optionNames) {
				t.Fatalf("选项声明数量错误: want=%v got=%v", testCase.optionNames, options)
			}
			for index, optionName := range testCase.optionNames {
				if options[index].Name != optionName {
					t.Fatalf("选项声明错误: want=%v got=%v", testCase.optionNames, options)
				}
			}
		})
	}
}

// TestVersionAndListCommandsWriteExpectedOutput 验证版本命令与命令列表共享
// Console 输出通道，并向调用方传播缺失依赖错误。
func TestVersionAndListCommandsWriteExpectedOutput(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, stderr, false)
	version := &Version{}
	if err := version.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行版本命令失败: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "ThinkGo Framework v3.0.0" {
		t.Fatalf("版本输出错误: %q", stdout.String())
	}
	if err := version.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}

	stdout.Reset()
	cli := console.NewConsole(nil)
	if err := cli.SetOutput(output); err != nil {
		t.Fatalf("设置 Console 输出失败: %v", err)
	}
	if err := cli.Register(version); err != nil {
		t.Fatalf("注册版本命令失败: %v", err)
	}
	if err := (&List{Console: cli}).Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行命令列表失败: %v", err)
	}
	if !strings.Contains(stdout.String(), "version") {
		t.Fatalf("命令列表缺少 version: %q", stdout.String())
	}
	if err := (&List{}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("缺少 Console 的 list 命令应返回错误")
	}
}

// TestRouteListCommandPrintsStableRouteSnapshot 验证路由列表展示字符串处理器，
// 并在应用或路由器缺失时返回明确错误。
func TestRouteListCommandPrintsStableRouteSnapshot(t *testing.T) {
	router := route.NewRouter()
	if _, err := router.Get("/users", "User@Index"); err != nil {
		t.Fatalf("注册 GET 路由失败: %v", err)
	}
	if _, err := router.Post("/users", "User@Save"); err != nil {
		t.Fatalf("注册 POST 路由失败: %v", err)
	}
	stdout := &bytes.Buffer{}
	output := console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false)
	app := buildConsoleTestApp(t, t.TempDir())
	app.Instance(string(framework.ServiceRoute), router)
	command := &RouteList{Command: console.Command{App: app}}
	if err := command.Execute(console.NewInput(), output); err != nil {
		t.Fatalf("执行路由列表失败: %v", err)
	}
	for _, expected := range []string{"GET", "POST", "/users", "User@Index", "User@Save"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("路由列表缺少 %q: %q", expected, stdout.String())
		}
	}
	if err := (&RouteList{}).Execute(console.NewInput(), output); !errors.Is(err, framework.ErrNilApplication) {
		t.Fatalf("空应用应返回 ErrNilApplication，实际为 %v", err)
	}
	if err := (&RouteList{Command: console.Command{App: &framework.App{}}}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("缺少路由器时应返回错误")
	}
	if err := command.Execute(console.NewInput(), nil); !errors.Is(err, console.ErrInvalidOutput) {
		t.Fatalf("空输出应返回 ErrInvalidOutput，实际为 %v", err)
	}
}
