package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/config"
	"thinkgo/framework/console"
)

// TestIsValidPort 校验端口校验逻辑：仅接受 1-65535 的整数。
func TestIsValidPort(t *testing.T) {
	cases := map[string]bool{
		"8080":  true,
		"1":     true,
		"65535": true,
		"0":     false,
		"65536": false,
		"70000": false,
		"-1":    false,
		"abc":   false,
		"":      false,
		"80a":   false,
	}
	for input, want := range cases {
		if got := isValidPort(input); got != want {
			t.Errorf("isValidPort(%q)=%v, 期望 %v", input, got, want)
		}
	}
}

// TestRunDeclaresPortOption 验证 run 命令声明了 --port/-p 选项，使帮助可展示且 Parse 可解析。
func TestRunDeclaresPortOption(t *testing.T) {
	cmd := &Run{}
	cmd.Configure()

	defs := cmd.GetOptionDefinitions()
	if len(defs) != 2 {
		t.Fatalf("run 应声明 2 个选项，得到 %d", len(defs))
	}
	if defs[0].Name != "port" || defs[0].Short != "p" {
		t.Fatalf("run 选项应为 port/-p，得到 %s/-%s", defs[0].Name, defs[0].Short)
	}
	if defs[1].Name != "app" || defs[1].Short != "a" {
		t.Fatalf("run 选项应为 app/-a，得到 %s/-%s", defs[1].Name, defs[1].Short)
	}
}

// TestRunEnsuresServerBinaryDirectory 验证热重载构建前会准备 bin 目录。
func TestRunEnsuresServerBinaryDirectory(t *testing.T) {
	basePath := t.TempDir()

	if err := ensureServerBinaryDir(basePath); err != nil {
		t.Fatalf("准备 server 二进制目录失败: %v", err)
	}
	if info, err := os.Stat(filepath.Join(basePath, "bin")); err != nil || !info.IsDir() {
		t.Fatalf("bin 目录应存在，info=%#v err=%v", info, err)
	}
}

// TestRunDoesNotMutatePortWhenApplicationStartupFailed 验证应用已有启动错误时，
// run 不会污染进程环境或内存配置。
func TestRunDoesNotMutatePortWhenApplicationStartupFailed(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing-app")
	app := framework.NewConsoleAppUninitialized(missingRoot)
	if app == nil {
		t.Fatal("构建测试控制台应用失败")
	}
	if err := app.Initialize(); err == nil {
		t.Fatal("缺失应用目录时初始化应返回错误")
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.StartupError() == nil {
		t.Fatal("测试应用应包含启动错误")
	}
	t.Setenv("SERVER_PORT", "original-port")
	configuration, err := app.Make(string(framework.ServiceConfig))
	if err != nil {
		t.Fatalf("读取失败应用配置服务失败: %v", err)
	}
	appConfig, ok := configuration.(*config.Config)
	if !ok {
		t.Fatalf("应用配置服务类型错误: %T", configuration)
	}
	portBefore := appConfig.Get("app.server.port")
	command := &Run{Command: console.Command{App: app}}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(console.NewInput("--port", "9000"), output); err == nil {
		t.Fatal("应用启动失败时 run 应返回错误")
	}
	if os.Getenv("SERVER_PORT") != "original-port" {
		t.Fatalf("启动失败不应修改 SERVER_PORT，实际为 %q", os.Getenv("SERVER_PORT"))
	}
	if portAfter := appConfig.Get("app.server.port"); !reflect.DeepEqual(portAfter, portBefore) {
		t.Fatalf("启动失败不应修改内存端口: before=%#v after=%#v", portBefore, portAfter)
	}
}

// TestRunRejectsMissingExecutionDependencies 验证命令输入、输出、应用和配置
// 缺失时均返回错误，不发生 panic。
func TestRunRejectsMissingExecutionDependencies(t *testing.T) {
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := (&Run{}).Execute(nil, output); err == nil {
		t.Fatal("空输入应返回错误")
	}
	if err := (&Run{}).Execute(console.NewInput(), nil); err == nil {
		t.Fatal("空输出应返回错误")
	}
	if err := (&Run{}).Execute(console.NewInput(), output); err == nil {
		t.Fatal("空应用应返回错误")
	}
	t.Setenv("SERVER_PORT", "unchanged")
	command := &Run{Command: console.Command{App: &framework.App{BasePath: t.TempDir()}}}
	if err := command.Execute(console.NewInput("--port", "9000"), output); err == nil {
		t.Fatal("缺少配置管理器时应返回错误")
	}
	if os.Getenv("SERVER_PORT") != "unchanged" {
		t.Fatalf("依赖校验失败不应修改 SERVER_PORT，实际为 %q", os.Getenv("SERVER_PORT"))
	}
	command = &Run{Command: console.Command{App: func() *framework.App {
		app := buildConsoleTestApp(t, t.TempDir())
		app.Instance(string(framework.ServiceConfig), config.NewConfig())
		return app
	}()}}
	if err := command.Execute(console.NewInput("--port", "0"), output); err == nil {
		t.Fatal("越界端口应返回错误")
	}
	if os.Getenv("SERVER_PORT") != "unchanged" {
		t.Fatalf("端口校验失败不应修改 SERVER_PORT，实际为 %q", os.Getenv("SERVER_PORT"))
	}
}

// TestEnsureServerBinaryDirRejectsInvalidBasePath 验证空根目录和普通文件根路径
// 不会被误判为可用的热重载输出目录。
func TestEnsureServerBinaryDirRejectsInvalidBasePath(t *testing.T) {
	if err := ensureServerBinaryDir(" "); err == nil {
		t.Fatal("空应用根目录应返回错误")
	}
	filePath := filepath.Join(t.TempDir(), "app-file")
	if err := os.WriteFile(filePath, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatalf("创建冲突文件失败: %v", err)
	}
	if err := ensureServerBinaryDir(filePath); err == nil {
		t.Fatal("普通文件不能作为应用根目录")
	}
}

type runTestKernel struct{}

func (runTestKernel) Run() error { return nil }

type runTestHost struct {
	runErr error
}

func (host runTestHost) Run() error { return host.runErr }

// TestRunWithOperationsUsesUnifiedApplicationHost 验证多应用 run 只创建一个统一 HTTP 宿主，
// 并把端口配置同步到所有应用实例。
func TestRunWithOperationsUsesUnifiedApplicationHost(t *testing.T) {
	basePath := t.TempDir()
	writeRunManagerConfig(t, basePath)
	manager, err := framework.NewApplicationManagerFromDefinitions(basePath, []framework.ApplicationDefinition{
		{Name: "admin", Path: "app/admin", Register: func(*framework.App) error { return nil }},
		{Name: "index", Path: "app/index", Register: func(*framework.App) error { return nil }},
	}, true)
	if err != nil {
		t.Fatalf("创建多应用管理器失败: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	command := &Run{Command: console.Command{
		App:     manager.DefaultApplication(),
		Manager: manager,
	}}
	command.Configure()
	input := console.NewInput("--app", "admin", "--port", "9000")
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		t.Fatalf("解析多应用 run 参数失败: %v", err)
	}
	selectedName := ""
	setName := ""
	setValue := ""
	hostCreated := false
	hostRun := false
	operations := runOperations{
		setEnvironment: func(name, value string) error {
			setName, setValue = name, value
			return nil
		},
		lookupExecutable: func(string) (string, error) {
			return "", errors.New("air not found")
		},
		runAir: func(string, string) error {
			t.Fatal("多应用普通模式不应启动 air")
			return nil
		},
		newHTTPHost: func(received *framework.ApplicationManager) (applicationHost, error) {
			if received != manager {
				t.Fatal("统一 HTTP 宿主收到错误的应用管理器")
			}
			hostCreated = true
			return runTestHost{}, nil
		},
		runHost: func(host applicationHost) error {
			hostRun = true
			if err := host.Run(); err != nil {
				return err
			}
			selectedName = command.App.ApplicationName
			return nil
		},
	}
	if err := command.executeWithOperations(input, console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false), operations); err != nil {
		t.Fatalf("多应用 run 启动失败: %v", err)
	}
	if !hostCreated || !hostRun || selectedName != "admin" {
		t.Fatalf("多应用宿主执行状态错误: created=%v run=%v selected=%q", hostCreated, hostRun, selectedName)
	}
	if setName != "SERVER_PORT" || setValue != "9000" {
		t.Fatalf("多应用端口环境变量错误: name=%q value=%q", setName, setValue)
	}
	for name, app := range manager.Applications() {
		configuration, resolveErr := framework.ResolveServiceAs[*config.Config](app, framework.ServiceConfig)
		if resolveErr != nil {
			t.Fatalf("解析应用 %q 配置失败: %v", name, resolveErr)
		}
		if got := configuration.Get("app.server.port"); got != "9000" {
			t.Fatalf("应用 %q 未同步端口配置: %#v", name, got)
		}
	}
}

func writeRunManagerConfig(t *testing.T, basePath string) {
	t.Helper()
	configPath := filepath.Join(basePath, "config")
	if err := os.MkdirAll(configPath, 0o755); err != nil {
		t.Fatalf("创建 run 测试配置目录失败: %v", err)
	}
	configs := map[string]string{
		"app.json":      `{"app_env":"test"}`,
		"log.json":      `{"default":"file","channels":{"file":{"type":"file","path":"./runtime/log"}}}`,
		"cache.json":    `{"default":"file","stores":{"file":{"type":"file","path":"./runtime/cache"}}}`,
		"view.json":     `{"view_path":"./app/view"}`,
		"database.json": `{"default":"default"}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("写入 run 测试配置 %q 失败: %v", name, err)
		}
	}
}

// TestRunWithOperationsStartsHotReload 验证合法端口会同时进入环境与内存配置，
// 并在 air 可用时准备隔离的 bin 目录后启动热重载。
func TestRunWithOperationsStartsHotReload(t *testing.T) {
	basePath := t.TempDir()
	appConfig := config.NewConfig()
	app := buildConsoleTestApp(t, basePath)
	app.Instance(string(framework.ServiceConfig), appConfig)
	setName := ""
	setValue := ""
	airPath := filepath.Join(basePath, "tools", "air.exe")
	runAirCalled := false
	operations := runOperations{
		setEnvironment: func(name, value string) error {
			setName, setValue = name, value
			return nil
		},
		lookupExecutable: func(name string) (string, error) {
			if name != "air" {
				t.Fatalf("查找了意外的可执行文件 %q", name)
			}
			return airPath, nil
		},
		newHTTPKernel: func(*framework.App) (framework.Kernel, error) {
			t.Fatal("热重载模式不应创建当前进程 HTTP 内核")
			return nil, nil
		},
		runApplication: func(*framework.App) error {
			t.Fatal("热重载模式不应直接运行应用")
			return nil
		},
		runAir: func(path, directory string) error {
			runAirCalled = true
			if path != airPath || directory != basePath {
				t.Fatalf("air 启动参数错误: path=%q directory=%q", path, directory)
			}
			return nil
		},
	}
	stdout := &bytes.Buffer{}
	command := &Run{Command: console.Command{App: app}}
	if err := command.executeWithOperations(
		console.NewInput("--port", "9000"),
		console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false),
		operations,
	); err != nil {
		t.Fatalf("启动热重载失败: %v", err)
	}
	if setName != "SERVER_PORT" || setValue != "9000" || appConfig.Get("app.server.port") != "9000" {
		t.Fatalf("端口未同步到环境和配置: name=%q value=%q config=%#v", setName, setValue, appConfig.Get("app.server.port"))
	}
	if !runAirCalled || !strings.Contains(stdout.String(), "9000") {
		t.Fatalf("热重载未执行或输出缺少端口: called=%v output=%q", runAirCalled, stdout.String())
	}
	if info, err := os.Stat(filepath.Join(basePath, "bin")); err != nil || !info.IsDir() {
		t.Fatalf("热重载前应创建 bin 目录: info=%#v err=%v", info, err)
	}
}

// TestRunWithOperationsFallsBackToNormalServer 验证 air 不可用时创建 HTTP 内核，
// 将其安装到应用并通过应用生命周期启动。
func TestRunWithOperationsFallsBackToNormalServer(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	app.Instance(string(framework.ServiceConfig), config.NewConfig())
	kernel := runTestKernel{}
	runCalled := false
	operations := runOperations{
		setEnvironment: os.Setenv,
		lookupExecutable: func(string) (string, error) {
			return "", errors.New("air not found")
		},
		newHTTPKernel: func(received *framework.App) (framework.Kernel, error) {
			if received != app {
				t.Fatal("HTTP 内核收到错误应用")
			}
			return kernel, nil
		},
		runApplication: func(received *framework.App) error {
			runCalled = true
			if received.Kernel != kernel {
				t.Fatalf("运行应用前未安装 HTTP 内核: %#v", received.Kernel)
			}
			return nil
		},
		runAir: func(string, string) error {
			t.Fatal("普通模式不应运行 air")
			return nil
		},
	}
	stdout := &bytes.Buffer{}
	command := &Run{Command: console.Command{App: app}}
	if err := command.executeWithOperations(
		console.NewInput(),
		console.NewOutputWithWriters(stdout, &bytes.Buffer{}, false),
		operations,
	); err != nil {
		t.Fatalf("普通模式启动失败: %v", err)
	}
	if !runCalled || !strings.Contains(stdout.String(), "Hot Reload") || !strings.Contains(stdout.String(), "normal mode") {
		t.Fatalf("普通模式降级行为错误: called=%v output=%q", runCalled, stdout.String())
	}
}

// TestRunWithOperationsPropagatesExternalFailures 验证环境写入、HTTP 初始化、
// 应用运行、目录准备和 air 进程错误均原样保留在错误链中。
func TestRunWithOperationsPropagatesExternalFailures(t *testing.T) {
	testErr := errors.New("external failure")
	newApp := func(basePath string) *framework.App {
		app := buildConsoleTestApp(t, t.TempDir())
		app.BasePath = basePath
		app.Instance(string(framework.ServiceConfig), config.NewConfig())
		return app
	}
	defaultOperations := func() runOperations {
		return runOperations{
			setEnvironment:   func(string, string) error { return nil },
			lookupExecutable: func(string) (string, error) { return "air", nil },
			newHTTPKernel:    func(*framework.App) (framework.Kernel, error) { return runTestKernel{}, nil },
			runApplication:   func(*framework.App) error { return nil },
			runAir:           func(string, string) error { return nil },
		}
	}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)

	t.Run("set environment", func(t *testing.T) {
		operations := defaultOperations()
		operations.setEnvironment = func(string, string) error { return testErr }
		command := &Run{Command: console.Command{App: newApp(t.TempDir())}}
		if err := command.executeWithOperations(console.NewInput("--port", "9000"), output, operations); !errors.Is(err, testErr) {
			t.Fatalf("环境错误未传播: %v", err)
		}
	})

	t.Run("new http kernel", func(t *testing.T) {
		operations := defaultOperations()
		operations.lookupExecutable = func(string) (string, error) { return "", testErr }
		operations.newHTTPKernel = func(*framework.App) (framework.Kernel, error) { return nil, testErr }
		command := &Run{Command: console.Command{App: newApp(t.TempDir())}}
		if err := command.executeWithOperations(console.NewInput(), output, operations); !errors.Is(err, testErr) {
			t.Fatalf("HTTP 配置错误未传播: %v", err)
		}
	})

	t.Run("run application", func(t *testing.T) {
		operations := defaultOperations()
		operations.lookupExecutable = func(string) (string, error) { return "", testErr }
		operations.runApplication = func(*framework.App) error { return testErr }
		command := &Run{Command: console.Command{App: newApp(t.TempDir())}}
		if err := command.executeWithOperations(console.NewInput(), output, operations); !errors.Is(err, testErr) {
			t.Fatalf("应用运行错误未传播: %v", err)
		}
	})

	t.Run("prepare binary directory", func(t *testing.T) {
		filePath := filepath.Join(t.TempDir(), "app-file")
		if err := os.WriteFile(filePath, []byte("not-a-directory"), 0o600); err != nil {
			t.Fatalf("创建冲突文件失败: %v", err)
		}
		command := &Run{Command: console.Command{App: newApp(filePath)}}
		if err := command.executeWithOperations(console.NewInput(), output, defaultOperations()); err == nil {
			t.Fatal("bin 目录准备失败应返回错误")
		}
	})

	t.Run("run air", func(t *testing.T) {
		operations := defaultOperations()
		operations.runAir = func(string, string) error { return testErr }
		command := &Run{Command: console.Command{App: newApp(t.TempDir())}}
		if err := command.executeWithOperations(console.NewInput(), output, operations); !errors.Is(err, testErr) {
			t.Fatalf("air 进程错误未传播: %v", err)
		}
	})
}

// TestRunDefaultOperationsAndAirProcessErrors 验证生产操作集合完整，且 air
// 可执行文件不存在时底层进程错误能够返回。
func TestRunDefaultOperationsAndAirProcessErrors(t *testing.T) {
	if err := defaultRunOperations().validate(); err != nil {
		t.Fatalf("默认 run 操作依赖不完整: %v", err)
	}
	if err := (runOperations{}).validate(); err == nil {
		t.Fatal("空 run 操作依赖应返回错误")
	}
	missingAir := filepath.Join(t.TempDir(), "missing-air")
	if err := runAirProcess(missingAir, t.TempDir()); err == nil {
		t.Fatal("不存在的 air 可执行文件应返回进程错误")
	}
}

func airOptionValue(arguments []string, name string) (string, bool) {
	for index := 0; index+1 < len(arguments); index += 2 {
		if arguments[index] == name {
			return arguments[index+1], true
		}
	}
	return "", false
}

func TestServerBinaryPathForOS(t *testing.T) {
	if got := serverBinaryPathForOS("windows"); got != "bin/server.exe" {
		t.Fatalf("Windows server path = %q, want bin/server.exe", got)
	}
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		if got := serverBinaryPathForOS(goos); got != "bin/server" {
			t.Fatalf("%s server path = %q, want bin/server", goos, got)
		}
	}
}

func TestAirCommandArgumentsUseEntrypointAndExcludeFrameworkDirectories(t *testing.T) {
	binaryPath := "bin/server.exe"
	arguments := airCommandArguments(binaryPath)

	configPath, exists := airOptionValue(arguments, "-c")
	if !exists || configPath != os.DevNull {
		t.Fatalf("config path = %q, exists=%v, want %q", configPath, exists, os.DevNull)
	}
	if _, exists := airOptionValue(arguments, "--build.bin"); exists {
		t.Fatalf("deprecated --build.bin must not be passed: %#v", arguments)
	}
	entrypoint, exists := airOptionValue(arguments, "--build.entrypoint")
	if !exists || entrypoint != "./"+binaryPath {
		t.Fatalf("entrypoint = %q, exists=%v, arguments=%#v", entrypoint, exists, arguments)
	}
	buildCommand, exists := airOptionValue(arguments, "--build.cmd")
	if !exists || buildCommand != "go build -o "+binaryPath+" main.go" {
		t.Fatalf("build command = %q, exists=%v", buildCommand, exists)
	}
	excludeValue, exists := airOptionValue(arguments, "--build.exclude_dir")
	if !exists {
		t.Fatalf("missing --build.exclude_dir: %#v", arguments)
	}
	excluded := make(map[string]bool)
	for _, directory := range strings.Split(excludeValue, ",") {
		excluded[directory] = true
	}
	for _, directory := range []string{"assets", "bin", "cmd", "framework", "runtime", "testdata", "tmp", "vendor"} {
		if !excluded[directory] {
			t.Errorf("directory %q is not excluded: %q", directory, excludeValue)
		}
	}
}
