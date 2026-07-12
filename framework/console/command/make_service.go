package command

import (
	"fmt"
	"path/filepath"

	"thinkgo/framework/console"
)

// MakeService 生成服务提供者源码。
type MakeService struct {
	console.Command
}

// Configure 配置服务生成命令。
func (c *MakeService) Configure() {
	c.Signature = "make:service"
	c.Description = "Create a new service class"
	c.AddArgument("name", "Service type name", true)
}

// Execute 校验名称后安全创建服务文件。
func (c *MakeService) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(c.App, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid service name: %w", err)
	}

	// 模板完整实现可失败的 Provider 生命周期。
	content := fmt.Sprintf(`package service

import (
	"thinkgo/framework"
)

// %s 服务。
type %s struct {
	App *framework.App
}

// New%s 创建服务实例。
func New%s(app *framework.App) *%s {
	return &%s{App: app}
}

// Register 将服务实例注册到容器，供控制器、命令和其它服务复用。
func (s *%s) Register(app *framework.App) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	s.App = app
	app.Instance("service.%s", s)
	return nil
}

// Boot 确认服务持有当前应用实例。
func (s *%s) Boot(app *framework.App) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	s.App = app
	return nil
}
`, name, name, name, name, name, name, name, name, name)

	if err := writeGeneratedAppSource(c.App, filepath.Join("app", "service"), lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create service %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Service %s created successfully.", name))
	return nil
}
