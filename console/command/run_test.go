package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/config"
	"github.com/zhuhanxin0308/thinkgo/framework/console"
	fwhttp "github.com/zhuhanxin0308/thinkgo/framework/http"
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

// TestRunDeclaresThinkPHPServerOptions 验证 run 命令的选项名称、短名和默认值
// 与 ThinkPHP 开发服务器保持一致。
func TestRunDeclaresThinkPHPServerOptions(t *testing.T) {
	cmd := &Run{}
	cmd.Configure()

	defs := cmd.GetOptionDefinitions()
	if len(defs) != 3 {
		t.Fatalf("run 应声明 3 个选项，得到 %d", len(defs))
	}
	if defs[0].Name != "host" || defs[0].Short != "H" || defs[0].Default != defaultRunHost {
		t.Fatalf("run host 选项错误: %#v", defs[0])
	}
	if defs[1].Name != "port" || defs[1].Short != "p" || defs[1].Default != defaultRunPort {
		t.Fatalf("run port 选项错误: %#v", defs[1])
	}
	if defs[2].Name != "root" || defs[2].Short != "r" || defs[2].Default != "" {
		t.Fatalf("run root 选项错误: %#v", defs[2])
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
	t.Setenv("APP_SERVER_PORT", "original-port")
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
	if os.Getenv("APP_SERVER_PORT") != "original-port" {
		t.Fatalf("启动失败不应修改 APP_SERVER_PORT，实际为 %q", os.Getenv("APP_SERVER_PORT"))
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
	t.Setenv("APP_SERVER_PORT", "8088")
	command := &Run{Command: console.Command{App: &framework.App{BasePath: t.TempDir()}}}
	if err := command.Execute(console.NewInput("--port", "9000"), output); err == nil {
		t.Fatal("缺少配置管理器时应返回错误")
	}
	if os.Getenv("APP_SERVER_PORT") != "8088" {
		t.Fatalf("依赖校验失败不应修改 APP_SERVER_PORT，实际为 %q", os.Getenv("APP_SERVER_PORT"))
	}
	command = &Run{Command: console.Command{App: func() *framework.App {
		app := buildConsoleTestApp(t, t.TempDir())
		app.Instance(string(framework.ServiceConfig), config.NewConfig())
		return app
	}()}}
	if err := command.Execute(console.NewInput("--port", "0"), output); err == nil {
		t.Fatal("越界端口应返回错误")
	}
	if os.Getenv("APP_SERVER_PORT") != "8088" {
		t.Fatalf("端口校验失败不应修改 APP_SERVER_PORT，实际为 %q", os.Getenv("APP_SERVER_PORT"))
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

// runEnvironmentStub 模拟当前进程环境，并记录写入与回滚顺序。
// setFailure 在写入已经生效后返回错误，用于覆盖系统调用部分成功的保守回滚路径。
type runEnvironmentStub struct {
	values       map[string]string
	calls        []string
	setFailure   func(string, string) error
	unsetFailure func(string) error
}

func newRunEnvironmentStub(initial map[string]string) *runEnvironmentStub {
	values := make(map[string]string, len(initial))
	for name, value := range initial {
		values[name] = value
	}
	return &runEnvironmentStub{values: values}
}

func (environment *runEnvironmentStub) lookup(name string) (string, bool) {
	environment.calls = append(environment.calls, "lookup "+name)
	value, exists := environment.values[name]
	return value, exists
}

func (environment *runEnvironmentStub) set(name, value string) error {
	environment.calls = append(environment.calls, "set "+name+"="+value)
	environment.values[name] = value
	if environment.setFailure != nil {
		return environment.setFailure(name, value)
	}
	return nil
}

func (environment *runEnvironmentStub) unset(name string) error {
	environment.calls = append(environment.calls, "unset "+name)
	delete(environment.values, name)
	if environment.unsetFailure != nil {
		return environment.unsetFailure(name)
	}
	return nil
}

func (environment *runEnvironmentStub) operations() runOperations {
	return runOperations{
		lookupEnvironment: environment.lookup,
		setEnvironment:    environment.set,
		unsetEnvironment:  environment.unset,
		lookupExecutable:  func(string) (string, error) { return "air", nil },
		newHTTPKernel:     func(*framework.App) (framework.Kernel, error) { return runTestKernel{}, nil },
		runApplication:    func(*framework.App) error { return nil },
		runAir:            func(string, string) error { return nil },
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
		lookupEnvironment: func(string) (string, bool) { return "", false },
		setEnvironment: func(name, value string) error {
			setName, setValue = name, value
			return nil
		},
		unsetEnvironment: func(string) error { return nil },
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
	if setName != "APP_SERVER_PORT" || setValue != "9000" || appConfig.GetInt("app.server.port") != 9000 {
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
	t.Setenv(applicationServerHostEnvironment, "127.0.0.1")
	t.Setenv(applicationServerPortEnvironment, "8080")
	t.Setenv(applicationPublicPathEnvironment, "")
	app := buildConsoleTestApp(t, t.TempDir())
	app.Instance(string(framework.ServiceConfig), config.NewConfig())
	kernel := runTestKernel{}
	runCalled := false
	operations := runOperations{
		lookupEnvironment: os.LookupEnv,
		setEnvironment:    os.Setenv,
		unsetEnvironment:  os.Unsetenv,
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

// TestRunOptionsReachFrozenProjectHTTPConfig 验证 Air 与普通模式都把命令行
// 地址写入项目级冻结快照；真实 HTTP 严格解析不得继续读取初始化时的非法地址。
func TestRunOptionsReachFrozenProjectHTTPConfig(t *testing.T) {
	for _, test := range []struct {
		name    string
		withAir bool
	}{
		{name: "air", withAir: true},
		{name: "normal", withAir: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			basePath := t.TempDir()
			ensureConsoleTestConfigFiles(t, basePath)
			invalidConfig := `{"app_env":"test","server":{"host":"bad host","port":0},"compression":{"enable":false}}`
			if err := os.WriteFile(filepath.Join(basePath, "config", "app.json"), []byte(invalidConfig), 0o644); err != nil {
				t.Fatalf("写入冻结快照回归配置失败: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(basePath, "app", "index"), 0o755); err != nil {
				t.Fatalf("创建原生应用目录失败: %v", err)
			}
			application := framework.NewConsoleAppUninitialized(basePath)
			if err := application.RegisterApplications(
				func(*framework.App) error { return nil },
				framework.ApplicationDefinition{Name: "index", Register: func(*framework.App) error { return nil }},
			); err != nil {
				t.Fatalf("注册原生应用失败: %v", err)
			}
			if err := application.Initialize(); err != nil {
				t.Fatalf("初始化冻结快照回归应用失败: %v", err)
			}
			if err := application.BootProviders(); err != nil {
				t.Fatalf("启动冻结快照回归应用失败: %v", err)
			}
			t.Cleanup(func() { _ = application.Close() })

			root := filepath.Join(basePath, "web")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("创建显式文档根目录失败: %v", err)
			}
			environment := make(map[string]string)
			assertRealHTTPConfig := func(received *framework.App) (framework.Kernel, error) {
				if received != application {
					t.Fatal("HTTP 内核收到错误应用")
				}
				handler, err := fwhttp.NewHttp(received)
				if err != nil {
					return nil, err
				}
				return fwhttp.NewServer(handler), nil
			}
			operations := runOperations{
				lookupEnvironment: func(name string) (string, bool) {
					value, exists := environment[name]
					return value, exists
				},
				setEnvironment: func(name, value string) error {
					environment[name] = value
					return nil
				},
				unsetEnvironment: func(name string) error {
					delete(environment, name)
					return nil
				},
				lookupExecutable: func(string) (string, error) {
					if test.withAir {
						return filepath.Join(basePath, "air"), nil
					}
					return "", errors.New("air not found")
				},
				newHTTPKernel:  assertRealHTTPConfig,
				runApplication: func(*framework.App) error { return nil },
				runAir: func(string, string) error {
					_, err := assertRealHTTPConfig(application)
					return err
				},
			}
			command := &Run{Command: console.Command{App: application}}
			if err := command.executeWithOperations(
				console.NewInput("--host", "127.0.0.1", "--port", "39091", "--root", root),
				console.NewOutputWithWriters(&bytes.Buffer{}, &bytes.Buffer{}, false),
				operations,
			); err != nil {
				t.Fatalf("run 参数未进入真实项目 HTTP 配置: %v", err)
			}

			projectConfig := application.ProjectApplicationConfig()
			serverConfig, ok := projectConfig["server"].(map[string]interface{})
			if !ok || serverConfig["host"] != "127.0.0.1" || serverConfig["port"] != 39091 {
				t.Fatalf("项目监听快照未更新: %#v", projectConfig["server"])
			}
			if environment[applicationServerHostEnvironment] != "127.0.0.1" ||
				environment[applicationServerPortEnvironment] != "39091" ||
				environment[applicationPublicPathEnvironment] != root {
				t.Fatalf("子进程环境与当前进程配置不一致: %#v", environment)
			}
		})
	}
}

// TestApplyRunConfigurationRestoresMixedEnvironmentOnLifecycleFailure 验证项目配置
// 已进入运行期而拒绝覆盖时，已有值与原本不存在的变量都会严格逆序恢复。
func TestApplyRunConfigurationRestoresMixedEnvironmentOnLifecycleFailure(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	lease, err := app.AcquireRunLease()
	if err != nil {
		t.Fatalf("获取配置生命周期失败测试运行租约失败: %v", err)
	}
	defer lease.Release()

	initial := map[string]string{
		applicationServerHostEnvironment: "old-host",
		applicationServerPortEnvironment: "old-port",
	}
	environment := newRunEnvironmentStub(initial)
	root := filepath.Join(app.BasePath, "next-public")
	err = applyRunConfiguration(app, environment.operations(), "127.0.0.1", "39091", root)
	if !errors.Is(err, framework.ErrProjectConfigurationOverrideUnavailable) || !errors.Is(err, framework.ErrApplicationRunning) {
		t.Fatalf("运行期配置覆盖应返回稳定生命周期错误: %v", err)
	}
	if !reflect.DeepEqual(environment.values, initial) {
		t.Fatalf("混合存在性环境未恢复: actual=%#v expected=%#v", environment.values, initial)
	}
	expectedCalls := []string{
		"lookup " + applicationServerHostEnvironment,
		"lookup " + applicationPublicPathEnvironment,
		"lookup " + applicationServerPortEnvironment,
		"set " + applicationServerHostEnvironment + "=127.0.0.1",
		"set " + applicationPublicPathEnvironment + "=" + root,
		"set " + applicationServerPortEnvironment + "=39091",
		"set " + applicationServerPortEnvironment + "=old-port",
		"unset " + applicationPublicPathEnvironment,
		"set " + applicationServerHostEnvironment + "=old-host",
	}
	if !reflect.DeepEqual(environment.calls, expectedCalls) {
		t.Fatalf("环境回滚顺序错误: actual=%#v expected=%#v", environment.calls, expectedCalls)
	}
}

// TestApplyRunConfigurationRollsBackFailedSetAndEarlierWrites 验证中途写入即使
// 已部分生效后才返回错误，当前变量和更早变量也会按逆序恢复，后续变量不被触碰。
func TestApplyRunConfigurationRollsBackFailedSetAndEarlierWrites(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	initial := map[string]string{
		applicationServerHostEnvironment: "old-host",
		applicationServerPortEnvironment: "old-port",
	}
	environment := newRunEnvironmentStub(initial)
	setErr := errors.New("public path set failed")
	root := filepath.Join(app.BasePath, "next-public")
	environment.setFailure = func(name, value string) error {
		if name == applicationPublicPathEnvironment && value == root {
			return setErr
		}
		return nil
	}

	err := applyRunConfiguration(app, environment.operations(), "127.0.0.1", "39091", root)
	if !errors.Is(err, setErr) {
		t.Fatalf("中途环境写入错误未保留: %v", err)
	}
	if !reflect.DeepEqual(environment.values, initial) {
		t.Fatalf("中途写入失败后环境未恢复: actual=%#v expected=%#v", environment.values, initial)
	}
	expectedCalls := []string{
		"lookup " + applicationServerHostEnvironment,
		"lookup " + applicationPublicPathEnvironment,
		"lookup " + applicationServerPortEnvironment,
		"set " + applicationServerHostEnvironment + "=127.0.0.1",
		"set " + applicationPublicPathEnvironment + "=" + root,
		"unset " + applicationPublicPathEnvironment,
		"set " + applicationServerHostEnvironment + "=old-host",
	}
	if !reflect.DeepEqual(environment.calls, expectedCalls) {
		t.Fatalf("中途失败回滚顺序错误: actual=%#v expected=%#v", environment.calls, expectedCalls)
	}
}

// TestApplyRunConfigurationJoinsPrimaryAndRollbackFailures 验证回滚单项失败
// 不会中断后续恢复，并把配置主错误与全部回滚错误保留在同一错误链中。
func TestApplyRunConfigurationJoinsPrimaryAndRollbackFailures(t *testing.T) {
	app := buildConsoleTestApp(t, t.TempDir())
	lease, err := app.AcquireRunLease()
	if err != nil {
		t.Fatalf("获取回滚聚合测试运行租约失败: %v", err)
	}
	defer lease.Release()

	initial := map[string]string{
		applicationServerHostEnvironment: "old-host",
		applicationServerPortEnvironment: "old-port",
	}
	environment := newRunEnvironmentStub(initial)
	restoreHostErr := errors.New("restore host failed")
	restorePortErr := errors.New("restore port failed")
	unsetPublicPathErr := errors.New("unset public path failed")
	environment.setFailure = func(name, value string) error {
		switch {
		case name == applicationServerPortEnvironment && value == "old-port":
			return restorePortErr
		case name == applicationServerHostEnvironment && value == "old-host":
			return restoreHostErr
		default:
			return nil
		}
	}
	environment.unsetFailure = func(name string) error {
		if name == applicationPublicPathEnvironment {
			return unsetPublicPathErr
		}
		return nil
	}

	root := filepath.Join(app.BasePath, "next-public")
	err = applyRunConfiguration(app, environment.operations(), "127.0.0.1", "39091", root)
	for name, expected := range map[string]error{
		"配置生命周期": framework.ErrProjectConfigurationOverrideUnavailable,
		"恢复端口":   restorePortErr,
		"删除公共目录": unsetPublicPathErr,
		"恢复主机":   restoreHostErr,
	} {
		if !errors.Is(err, expected) {
			t.Errorf("聚合错误缺少%s错误 %v: %v", name, expected, err)
		}
	}
	if !reflect.DeepEqual(environment.values, initial) {
		t.Fatalf("回滚函数报错后仍应尽力恢复全部环境: actual=%#v expected=%#v", environment.values, initial)
	}
	expectedCalls := []string{
		"lookup " + applicationServerHostEnvironment,
		"lookup " + applicationPublicPathEnvironment,
		"lookup " + applicationServerPortEnvironment,
		"set " + applicationServerHostEnvironment + "=127.0.0.1",
		"set " + applicationPublicPathEnvironment + "=" + root,
		"set " + applicationServerPortEnvironment + "=39091",
		"set " + applicationServerPortEnvironment + "=old-port",
		"unset " + applicationPublicPathEnvironment,
		"set " + applicationServerHostEnvironment + "=old-host",
	}
	if !reflect.DeepEqual(environment.calls, expectedCalls) {
		t.Fatalf("回滚错误后未继续严格逆序恢复: actual=%#v expected=%#v", environment.calls, expectedCalls)
	}
}

// TestRunWithOperationsPropagatesExternalFailures 验证环境写入、HTTP 初始化、
// 应用运行、目录准备和 air 进程错误均原样保留在错误链中。
func TestRunWithOperationsPropagatesExternalFailures(t *testing.T) {
	testErr := errors.New("external failure")
	newApp := func(basePath string) *framework.App {
		if information, err := os.Stat(basePath); err == nil && information.IsDir() {
			if err := os.MkdirAll(filepath.Join(basePath, "public"), 0o755); err != nil {
				t.Fatalf("创建 run 错误传播测试公共目录失败: %v", err)
			}
		}
		app := buildConsoleTestApp(t, t.TempDir())
		app.BasePath = basePath
		app.Instance(string(framework.ServiceConfig), config.NewConfig())
		return app
	}
	defaultOperations := func() runOperations {
		return runOperations{
			lookupEnvironment: func(string) (string, bool) { return "", false },
			setEnvironment:    func(string, string) error { return nil },
			unsetEnvironment:  func(string) error { return nil },
			lookupExecutable:  func(string) (string, error) { return "air", nil },
			newHTTPKernel:     func(*framework.App) (framework.Kernel, error) { return runTestKernel{}, nil },
			runApplication:    func(*framework.App) error { return nil },
			runAir:            func(string, string) error { return nil },
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

func TestAirCommandArgumentsDiscoverControllersAndBuildApplicationPackage(t *testing.T) {
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
	preBuildCommand, exists := airOptionValue(arguments, "--build.pre_cmd")
	if !exists || preBuildCommand != "go run ./cmd/think service:discover" {
		t.Fatalf("pre-build command = %q, exists=%v", preBuildCommand, exists)
	}
	buildCommand, exists := airOptionValue(arguments, "--build.cmd")
	if !exists || buildCommand != "go build -o "+binaryPath+" ." {
		t.Fatalf("build command = %q, exists=%v", buildCommand, exists)
	}
	if strings.Contains(preBuildCommand+buildCommand, "appsync") || strings.Contains(preBuildCommand+buildCommand, "appregistry") {
		t.Fatalf("开发服务器不应要求应用注册表生成步骤: pre=%q build=%q", preBuildCommand, buildCommand)
	}
	if strings.Contains(preBuildCommand+buildCommand, "&&") {
		t.Fatalf("Air 构建命令不得依赖旧版 PowerShell 不支持的 &&: pre=%q build=%q", preBuildCommand, buildCommand)
	}
	excludeValue, exists := airOptionValue(arguments, "--build.exclude_dir")
	if !exists {
		t.Fatalf("missing --build.exclude_dir: %#v", arguments)
	}
	excluded := make(map[string]bool)
	for _, directory := range strings.Split(excludeValue, ",") {
		excluded[directory] = true
	}
	for _, directory := range []string{"assets", "bin", "cmd", "docs", "framework", "public", "runtime", "testdata", "tmp", "vendor"} {
		if !excluded[directory] {
			t.Errorf("directory %q is not excluded: %q", directory, excludeValue)
		}
	}
}
