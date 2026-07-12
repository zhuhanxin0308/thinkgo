package command

import (
	"fmt"
	"path/filepath"

	"thinkgo/framework/console"
)

// MakeController 生成控制器源码。
type MakeController struct {
	console.Command
}

// Configure 配置控制器生成命令。
func (c *MakeController) Configure() {
	c.Signature = "make:controller"
	c.Description = "Create a new controller class"
	c.AddArgument("name", "Controller type name", true)
}

// Execute 校验名称后安全创建控制器文件。
func (c *MakeController) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(c.App, input, output, "Controller")
	if err != nil {
		return fmt.Errorf("invalid controller name: %w", err)
	}

	// 模板使用失败即停止的控制器注册 API。
	content := fmt.Sprintf(`package controller

import (
	"thinkgo/framework"
)

// init 注册控制器类型，重复名称会在启动阶段立即失败。
func init() {
	framework.MustRegisterController("%s", &%s{})
}

// %s 控制器。
type %s struct {
	framework.Controller
}

// Index 返回控制器默认响应。
func (c *%s) Index() string {
	return "Hello %s"
}
`, name, name, name, name, name, name)

	// 在应用根目录约束内独占创建，禁止符号链接逃逸与覆盖竞态。
	if err := writeGeneratedAppSource(c.App, filepath.Join("app", "controller"), lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create controller %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Controller %s created successfully.", name))
	return nil
}
