package command

import (
	"errors"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// TestWriteGeneratedSourceConfinesAndExclusivelyCreates 验证生成源码只能写入应用根目录，
// 并发创建同名文件时只有一个成功，已有源码不会被截断覆盖。
func TestWriteGeneratedSourceConfinesAndExclusivelyCreates(t *testing.T) {
	parent := t.TempDir()
	basePath := filepath.Join(parent, "project")
	if err := os.Mkdir(basePath, 0o700); err != nil {
		t.Fatalf("创建项目根目录失败: %v", err)
	}

	if err := writeGeneratedSource(basePath, "..", "escaped.go", []byte("package escaped\n")); err == nil {
		t.Fatal("生成目录逃逸应用根目录应失败")
	}
	if _, err := os.Stat(filepath.Join(parent, "escaped.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("根目录外不应生成文件，实际错误为 %v", err)
	}

	content := []byte("package generated\n\ntype Safe struct{}\n")
	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	var waitGroup sync.WaitGroup
	for index := range errorsByCall {
		waitGroup.Add(1)
		go func(call int) {
			defer waitGroup.Done()
			<-start
			errorsByCall[call] = writeGeneratedSource(basePath, filepath.Join("app", "model"), "safe.go", content)
		}(index)
	}
	close(start)
	waitGroup.Wait()
	successes := 0
	existsErrors := 0
	for _, err := range errorsByCall {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, fs.ErrExist):
			existsErrors++
		default:
			t.Fatalf("并发生成返回意外错误: %v", err)
		}
	}
	if successes != 1 || existsErrors != 1 {
		t.Fatalf("并发同名生成结果错误: success=%d exists=%d errors=%v", successes, existsErrors, errorsByCall)
	}

	generatedPath := filepath.Join(basePath, "app", "model", "safe.go")
	before, err := os.ReadFile(generatedPath)
	if err != nil {
		t.Fatalf("读取生成源码失败: %v", err)
	}
	if err := writeGeneratedSource(basePath, filepath.Join("app", "model"), "safe.go", []byte("package overwritten\n")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("覆盖已有源码应返回 fs.ErrExist，实际为 %v", err)
	}
	after, err := os.ReadFile(generatedPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("已有源码被修改: before=%q after=%q err=%v", before, after, err)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(generatedPath)
		if statErr != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("生成源码权限错误: mode=%v err=%v", info.Mode().Perm(), statErr)
		}
	}
}

// TestWriteGeneratedSourceRejectsSymlinkEscapeAndInvalidGo 验证目录符号链接不能逃逸，
// 无法通过 go/format 的源码也不会留下半成品文件。
func TestWriteGeneratedSourceRejectsSymlinkEscapeAndInvalidGo(t *testing.T) {
	basePath := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(basePath, "app"), 0o700); err != nil {
		t.Fatalf("创建 app 目录失败: %v", err)
	}
	symlinkPath := filepath.Join(basePath, "app", "controller")
	if err := os.Symlink(outside, symlinkPath); err != nil {
		t.Skipf("当前环境无法创建目录符号链接: %v", err)
	}
	if err := writeGeneratedSource(basePath, filepath.Join("app", "controller"), "escape.go", []byte("package controller\n")); err == nil {
		t.Fatal("指向根目录外的符号链接应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("符号链接目标不应收到文件，实际错误为 %v", err)
	}

	invalidDir := filepath.Join("app", "invalid")
	if err := writeGeneratedSource(basePath, invalidDir, "broken.go", []byte("package {")); err == nil {
		t.Fatal("无效 Go 源码应返回错误")
	}
	if _, err := os.Stat(filepath.Join(basePath, invalidDir, "broken.go")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("格式化失败不应留下文件，实际错误为 %v", err)
	}
}

// TestMakeCommandsRejectUnsafeNames 验证生成器拒绝路径穿越和非法 Go 标识符。
func TestMakeCommandsRejectUnsafeNames(t *testing.T) {
	generators := []struct {
		name string
		cmd  interface {
			SetApp(*framework.App)
			Execute(*console.Input, *console.Output) error
		}
	}{
		{name: "controller", cmd: &MakeController{}},
		{name: "model", cmd: &MakeModel{}},
		{name: "command", cmd: &MakeCommand{}},
		{name: "validate", cmd: &MakeValidate{}},
		{name: "event", cmd: &MakeEvent{}},
		{name: "listener", cmd: &MakeListener{}},
	}

	for _, generator := range generators {
		t.Run(generator.name, func(t *testing.T) {
			basePath := t.TempDir()
			app := &framework.App{BasePath: basePath}
			generator.cmd.SetApp(app)
			if err := generator.cmd.Execute(&console.Input{Args: []string{"../Unsafe"}}, generatorTestOutput()); err == nil {
				t.Fatal("非法生成器名称应返回错误")
			}

			assertNoGoFiles(t, basePath)
		})
	}
}

// TestMakeValidateGeneratesCompilableValidator 验证验证器模板使用当前 Validator API 和 required 规则。
func TestMakeValidateGeneratesCompilableValidator(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeValidate{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"UserProfile"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成验证器失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "validate", "userprofile.go")
	if !strings.Contains(content, "validate.Validator") {
		t.Fatalf("验证器模板应嵌入 validate.Validator，实际为:\n%s", content)
	}
	if strings.Contains(content, "validate.Validate") || strings.Contains(content, `"require|max`) {
		t.Fatalf("验证器模板不应使用过期 API 或未知 require 规则，实际为:\n%s", content)
	}
	discovery := readGeneratedFile(t, basePath, "app", "index", applicationDiscoveryFilename)
	if !strings.Contains(discovery, `ValidatorFactories: map[string]interface{}{`) ||
		!strings.Contains(discovery, `"UserProfile": func() interface{} { return applicationValidate.NewUserProfile() }`) {
		t.Fatalf("新验证器未进入自动发现入口:\n%s", discovery)
	}
}

// TestMakeControllerUsesThinkPHPStylePlainController 验证控制器模板与 ThinkPHP
// 一样不强制继承基础控制器，并自动进入控制器发现清单。
func TestMakeControllerUsesThinkPHPStylePlainController(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeController{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"Account"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成控制器失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "controller", "account.go")
	if strings.Contains(content, "BaseController") || strings.Contains(content, "framework.Controller") || strings.Contains(content, "MustRegisterController") {
		t.Fatalf("控制器模板不得强制继承基础控制器或暴露底层注册:\n%s", content)
	}
	discovery := readGeneratedFile(t, basePath, "app", "index", applicationDiscoveryFilename)
	if !strings.Contains(discovery, `"Account"`) || !strings.Contains(discovery, "applicationController.Account") {
		t.Fatalf("新控制器未进入自动发现清单:\n%s", discovery)
	}
}

// TestMakeControllerAppliesThinkPHPRouteSuffixes 验证控制器名称和资源动作名称
// 使用与 ThinkPHP make:controller 相同的 route 后缀配置。
func TestMakeControllerAppliesThinkPHPRouteSuffixes(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	app := framework.NewAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Config().Set("route.controller_suffix", true); err != nil {
		t.Fatalf("设置控制器后缀失败: %v", err)
	}
	if err := app.Config().Set("route.action_suffix", "Action"); err != nil {
		t.Fatalf("设置动作后缀失败: %v", err)
	}
	cmd := &MakeController{}
	cmd.SetApp(app)

	if err := cmd.Execute(&console.Input{Args: []string{"Account"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成带路由后缀的控制器失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "controller", "accountcontroller.go")
	for _, method := range []string{"IndexAction", "CreateAction", "SaveAction", "ReadAction", "EditAction", "UpdateAction", "DeleteAction"} {
		if !strings.Contains(content, "func (controller *AccountController) "+method+"(") {
			t.Errorf("生成控制器缺少动作 %s:\n%s", method, content)
		}
	}
}

// TestMakeControllerUsesNativeDefaultApplication 验证只有一个业务应用时仍使用
// app/index/controller，且不会创建旧式 app/application.go。
func TestMakeControllerUsesNativeDefaultApplication(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	applicationPath := filepath.Join(basePath, "app")
	if err := os.MkdirAll(applicationPath, 0o755); err != nil {
		t.Fatalf("创建原生应用根目录失败: %v", err)
	}
	cmd := &MakeController{}
	cmd.SetApp(&framework.App{
		BasePath:        basePath,
		ApplicationPath: applicationPath,
	})

	if err := cmd.Execute(&console.Input{Args: []string{"Account"}}, generatorTestOutput()); err != nil {
		t.Fatalf("单一原生应用生成控制器失败: %v", err)
	}
	controllerSource := readGeneratedFile(t, basePath, "app", "index", "controller", "account.go")
	if !strings.Contains(controllerSource, "type Account struct") {
		t.Fatalf("单一原生应用控制器内容错误:\n%s", controllerSource)
	}
	updatedDiscovery := readGeneratedFile(t, basePath, "app", "index", applicationDiscoveryFilename)
	if !strings.Contains(updatedDiscovery, `"Account"`) || !strings.Contains(updatedDiscovery, "applicationController.Account") {
		t.Fatalf("单一原生应用控制器未进入发现清单:\n%s", updatedDiscovery)
	}
	if _, err := os.Stat(filepath.Join(basePath, "app", "application.go")); !os.IsNotExist(err) {
		t.Fatalf("生成控制器不得创建旧式 application.go: %v", err)
	}
}

// TestMakeEventAndListenerImplementFrameworkInterfaces 验证事件和监听器模板满足框架接口。
func TestMakeEventAndListenerImplementFrameworkInterfaces(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	eventCmd := &MakeEvent{}
	eventCmd.SetApp(&framework.App{BasePath: basePath})
	if err := eventCmd.Execute(&console.Input{Args: []string{"UserRegistered"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成事件失败: %v", err)
	}

	listenerCmd := &MakeListener{}
	listenerCmd.SetApp(&framework.App{BasePath: basePath})
	if err := listenerCmd.Execute(&console.Input{Args: []string{"SendWelcomeMail"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成监听器失败: %v", err)
	}

	eventContent := readGeneratedFile(t, basePath, "app", "index", "event", "userregistered.go")
	if !strings.Contains(eventContent, "func (e *UserRegistered) Name() string") {
		t.Fatalf("事件模板应实现 Name() string，实际为:\n%s", eventContent)
	}

	listenerContent := readGeneratedFile(t, basePath, "app", "index", "listener", "sendwelcomemail.go")
	if !strings.Contains(listenerContent, `"github.com/zhuhanxin0308/thinkgo/v3/event"`) ||
		!strings.Contains(listenerContent, "Handle(currentEvent event.Event) error") {
		t.Fatalf("监听器模板应使用 event.Event 接口，实际为:\n%s", listenerContent)
	}
}

// TestMakeCommandGeneratesNonPlaceholderImplementation 验证命令模板不再输出占位描述和伪执行文案。
func TestMakeCommandGeneratesNonPlaceholderImplementation(t *testing.T) {
	basePath := t.TempDir()
	writeDiscoveryModuleFixture(t, basePath, "example.com/commands")
	cmd := &MakeCommand{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"ReportDaily"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成命令失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "command", "reportdaily.go")
	if strings.Contains(content, "Command description") || strings.Contains(content, "executed") {
		t.Fatalf("命令模板不应包含占位描述或伪执行文案，实际为:\n%s", content)
	}
	if !strings.Contains(content, `c.Description = "应用命令 ReportDaily"`) {
		t.Fatalf("命令模板应生成明确描述，实际为:\n%s", content)
	}
}

// TestMakeServiceDoesNotEmitCommentOnlyLifecycle 验证服务模板的生命周期方法不是只有注释。
func TestMakeServiceDoesNotEmitCommentOnlyLifecycle(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeService{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"Billing"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成服务失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "service", "billing.go")
	if strings.Contains(content, "Register service") || strings.Contains(content, "Boot service") {
		t.Fatalf("服务模板不应包含注释型生命周期占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, `return app.Instance("service.Billing", s)`) {
		t.Fatalf("服务模板应返回实际注册错误，实际为:\n%s", content)
	}
	if !strings.Contains(content, "framework.Service") ||
		!strings.Contains(content, "Register() error") ||
		!strings.Contains(content, "Boot() error") ||
		strings.Contains(content, "Register(app *framework.App)") {
		t.Fatalf("服务模板必须使用 ThinkPHP 风格的无参数生命周期，实际为:\n%s", content)
	}
}

// TestMakeSubscriberGeneratesConcreteListener 验证订阅者模板会注册自身并记录事件名。
func TestMakeSubscriberGeneratesConcreteListener(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeSubscribe{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"Audit"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成订阅者失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "subscribe", "audit.go")
	if strings.Contains(content, `// dispatcher.Listen`) {
		t.Fatalf("订阅者模板不应包含注释型监听占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, `dispatcher.Listen("*", s)`) ||
		!strings.Contains(content, "Subscribe(dispatcher *event.Dispatcher) error") ||
		!strings.Contains(content, "Handle(currentEvent event.Event) error") ||
		!strings.Contains(content, "LastEventName") {
		t.Fatalf("订阅者模板应生成具体监听行为，实际为:\n%s", content)
	}
}

// TestMakeMiddlewareGeneratesNilSafeMiddleware 验证中间件模板不再是注释型透传。
func TestMakeMiddlewareGeneratesNilSafeMiddleware(t *testing.T) {
	basePath := t.TempDir()
	writeGeneratorTestModule(t, basePath)
	cmd := &MakeMiddleware{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	if err := cmd.Execute(&console.Input{Args: []string{"Audit"}}, generatorTestOutput()); err != nil {
		t.Fatalf("生成中间件失败: %v", err)
	}

	content := readGeneratedFile(t, basePath, "app", "index", "middleware", "audit.go")
	if strings.Contains(content, "Before request") || strings.Contains(content, "After request") {
		t.Fatalf("中间件模板不应包含注释型占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, "if next == nil") {
		t.Fatalf("中间件模板应包含 next 为空时的防护，实际为:\n%s", content)
	}
}

func generatorTestOutput() *console.Output {
	return console.NewOutputWithWriters(io.Discard, io.Discard, false)
}

func writeGeneratorTestModule(t *testing.T, basePath string) {
	t.Helper()
	writeDiscoveryModuleFixture(t, basePath, "example.com/project")
}

func readGeneratedFile(t *testing.T, basePath string, parts ...string) string {
	t.Helper()
	pathParts := append([]string{basePath}, parts...)
	content, err := os.ReadFile(filepath.Join(pathParts...))
	if err != nil {
		t.Fatalf("读取生成文件失败: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pathParts...), content, parser.AllErrors); err != nil {
		t.Fatalf("生成的 Go 源码语法无效: %v", err)
	}
	return string(content)
}

func assertNoGoFiles(t *testing.T, basePath string) {
	t.Helper()
	err := filepath.WalkDir(basePath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Fatalf("非法名称不应生成 Go 文件，实际生成 %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描生成目录失败: %v", err)
	}
}
