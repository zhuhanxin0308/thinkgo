package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thinkgo/framework"
	"thinkgo/framework/console"
)

// TestMakeCommandsRejectUnsafeNames 验证生成器拒绝路径穿越和非法 Go 标识符。
func TestMakeCommandsRejectUnsafeNames(t *testing.T) {
	generators := []struct {
		name string
		cmd  interface {
			SetApp(*framework.App)
			Execute(*console.Input, *console.Output)
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
			generator.cmd.Execute(&console.Input{Args: []string{"../Unsafe"}}, &console.Output{})

			assertNoGoFiles(t, basePath)
		})
	}
}

// TestMakeValidateGeneratesCompilableValidator 验证验证器模板使用当前 Validator API 和 required 规则。
func TestMakeValidateGeneratesCompilableValidator(t *testing.T) {
	basePath := t.TempDir()
	cmd := &MakeValidate{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	cmd.Execute(&console.Input{Args: []string{"UserProfile"}}, &console.Output{})

	content := readGeneratedFile(t, basePath, "app", "validate", "userprofile.go")
	if !strings.Contains(content, "validate.Validator") {
		t.Fatalf("验证器模板应嵌入 validate.Validator，实际为:\n%s", content)
	}
	if strings.Contains(content, "validate.Validate") || strings.Contains(content, `"require|max`) {
		t.Fatalf("验证器模板不应使用过期 API 或未知 require 规则，实际为:\n%s", content)
	}
}

// TestMakeEventAndListenerImplementFrameworkInterfaces 验证事件和监听器模板满足框架接口。
func TestMakeEventAndListenerImplementFrameworkInterfaces(t *testing.T) {
	basePath := t.TempDir()
	eventCmd := &MakeEvent{}
	eventCmd.SetApp(&framework.App{BasePath: basePath})
	eventCmd.Execute(&console.Input{Args: []string{"UserRegistered"}}, &console.Output{})

	listenerCmd := &MakeListener{}
	listenerCmd.SetApp(&framework.App{BasePath: basePath})
	listenerCmd.Execute(&console.Input{Args: []string{"SendWelcomeMail"}}, &console.Output{})

	eventContent := readGeneratedFile(t, basePath, "app", "event", "userregistered.go")
	if !strings.Contains(eventContent, "func (e *UserRegistered) Name() string") {
		t.Fatalf("事件模板应实现 Name() string，实际为:\n%s", eventContent)
	}

	listenerContent := readGeneratedFile(t, basePath, "app", "listener", "sendwelcomemail.go")
	if !strings.Contains(listenerContent, `"thinkgo/framework/event"`) ||
		!strings.Contains(listenerContent, "Handle(event event.Event)") {
		t.Fatalf("监听器模板应使用 event.Event 接口，实际为:\n%s", listenerContent)
	}
}

// TestMakeCommandGeneratesNonPlaceholderImplementation 验证命令模板不再输出占位描述和伪执行文案。
func TestMakeCommandGeneratesNonPlaceholderImplementation(t *testing.T) {
	basePath := t.TempDir()
	cmd := &MakeCommand{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	cmd.Execute(&console.Input{Args: []string{"ReportDaily"}}, &console.Output{})

	content := readGeneratedFile(t, basePath, "app", "command", "reportdaily.go")
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
	cmd := &MakeService{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	cmd.Execute(&console.Input{Args: []string{"Billing"}}, &console.Output{})

	content := readGeneratedFile(t, basePath, "app", "service", "billing.go")
	if strings.Contains(content, "Register service") || strings.Contains(content, "Boot service") {
		t.Fatalf("服务模板不应包含注释型生命周期占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, `app.Instance("service.Billing", s)`) {
		t.Fatalf("服务模板应提供实际注册行为，实际为:\n%s", content)
	}
}

// TestMakeSubscriberGeneratesConcreteListener 验证订阅者模板会注册自身并记录事件名。
func TestMakeSubscriberGeneratesConcreteListener(t *testing.T) {
	basePath := t.TempDir()
	cmd := &MakeSubscribe{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	cmd.Execute(&console.Input{Args: []string{"Audit"}}, &console.Output{})

	content := readGeneratedFile(t, basePath, "app", "subscribe", "audit.go")
	if strings.Contains(content, `// dispatcher.Listen`) {
		t.Fatalf("订阅者模板不应包含注释型监听占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, `dispatcher.Listen("*", s)`) ||
		!strings.Contains(content, "LastEventName") {
		t.Fatalf("订阅者模板应生成具体监听行为，实际为:\n%s", content)
	}
}

// TestMakeMiddlewareGeneratesNilSafeMiddleware 验证中间件模板不再是注释型透传。
func TestMakeMiddlewareGeneratesNilSafeMiddleware(t *testing.T) {
	basePath := t.TempDir()
	cmd := &MakeMiddleware{}
	cmd.SetApp(&framework.App{BasePath: basePath})

	cmd.Execute(&console.Input{Args: []string{"Audit"}}, &console.Output{})

	content := readGeneratedFile(t, basePath, "app", "middleware", "audit.go")
	if strings.Contains(content, "Before request") || strings.Contains(content, "After request") {
		t.Fatalf("中间件模板不应包含注释型占位，实际为:\n%s", content)
	}
	if !strings.Contains(content, "if next == nil") {
		t.Fatalf("中间件模板应包含 next 为空时的防护，实际为:\n%s", content)
	}
}

func readGeneratedFile(t *testing.T, basePath string, parts ...string) string {
	t.Helper()
	pathParts := append([]string{basePath}, parts...)
	content, err := os.ReadFile(filepath.Join(pathParts...))
	if err != nil {
		t.Fatalf("读取生成文件失败: %v", err)
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
