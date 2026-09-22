package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
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
	addApplicationSelectionOption(&c.Command)
}

// Execute 校验名称后安全创建服务文件。
func (c *MakeService) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid service name: %w", err)
	}
	name := target.name

	// 模板使用与 ThinkPHP AppService 一致的无参数服务生命周期。
	content := fmt.Sprintf(`package service

import "github.com/zhuhanxin0308/thinkgo/framework"

// %s 服务。
type %s struct {
	framework.Service
}

// Register 将服务实例注册到容器，供控制器、命令和其它服务复用。
func (s *%s) Register() error {
	app := s.App()
	if app == nil {
		return framework.ErrNilApplication
	}
	return app.Instance("service.%s", s)
}

// Boot 确认服务持有当前应用实例。
func (s *%s) Boot() error {
	if s.App() == nil {
		return framework.ErrNilApplication
	}
	return nil
}
`, name, name, name, name, name)

	if err := writeGeneratedAppSource(c.App, target, "service", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create service %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Service %s created successfully.", name))
	return nil
}
