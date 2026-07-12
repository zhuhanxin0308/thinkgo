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
	if len(defs) != 1 {
		t.Fatalf("run 应声明 1 个选项，得到 %d", len(defs))
	}
	if defs[0].Name != "port" || defs[0].Short != "p" {
		t.Fatalf("run 选项应为 port/-p，得到 %s/-%s", defs[0].Name, defs[0].Short)
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
	app := framework.NewConsoleApp(missingRoot)
	t.Cleanup(func() { _ = app.Close() })
	if app.StartupError() == nil {
		t.Fatal("测试应用应包含启动错误")
	}
	t.Setenv("SERVER_PORT", "original-port")
	portBefore := app.Config.Get("app.server.port")
	command := &Run{Command: console.Command{App: app}}
	output := console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false)
	if err := command.Execute(console.NewInput("--port", "9000"), output); err == nil {
		t.Fatal("应用启动失败时 run 应返回错误")
	}
	if os.Getenv("SERVER_PORT") != "original-port" {
		t.Fatalf("启动失败不应修改 SERVER_PORT，实际为 %q", os.Getenv("SERVER_PORT"))
	}
	if portAfter := app.Config.Get("app.server.port"); !reflect.DeepEqual(portAfter, portBefore) {
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
	command = &Run{Command: console.Command{App: &framework.App{BasePath: t.TempDir(), Config: config.NewConfig()}}}
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

// TestRunWithOperationsStartsHotReload 验证合法端口会同时进入环境与内存配置，
// 并在 air 可用时准备隔离的 bin 目录后启动热重载。
func TestRunWithOperationsStartsHotReload(t *testing.T) {
	basePath := t.TempDir()
	appConfig := config.NewConfig()
	app := &framework.App{BasePath: basePath, Config: appConfig}
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
	app := &framework.App{BasePath: t.TempDir(), Config: config.NewConfig()}
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
		return &framework.App{BasePath: basePath, Config: config.NewConfig()}
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
