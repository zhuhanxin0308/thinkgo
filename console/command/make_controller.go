package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeController 生成控制器源码。
type MakeController struct {
	console.Command
}

// Configure 配置控制器生成命令。
func (c *MakeController) Configure() {
	c.Signature = "make:controller"
	c.Description = "Create a new resource controller class"
	c.AddArgument("name", "Controller type name", true)
	addApplicationSelectionOption(&c.Command)
	c.AddBoolOption("api", "", "Generate an API resource controller")
	c.AddBoolOption("plain", "", "Generate an empty controller")
}

// Execute 校验名称后安全创建控制器文件。
func (c *MakeController) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid controller name: %w", err)
	}
	name := target.name
	if c.App != nil && c.App.Config() != nil && c.App.Config().GetBool("route.controller_suffix", false) {
		name += "Controller"
	}
	actionSuffix := ""
	if c.App != nil && c.App.Config() != nil {
		rawSuffix := c.App.Config().GetString("route.action_suffix", "")
		if rawSuffix != "" {
			actionSuffix, err = normalizeGeneratorName(rawSuffix, "")
			if err != nil {
				return fmt.Errorf("invalid route.action_suffix: %w", err)
			}
		}
	}
	content := controllerSource(name, actionSuffix, input.GetOption("api") == "true", input.GetOption("plain") == "true")

	// 在应用根目录约束内独占创建，禁止符号链接逃逸与覆盖竞态。
	if err := writeAndRefreshGeneratedControllerSource(c.App, target, lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create controller %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Controller %s created successfully.", name))
	return nil
}

func controllerSource(name, actionSuffix string, api, plain bool) string {
	if plain && !api {
		return fmt.Sprintf(`package controller

// %s 控制器。
type %s struct {
}
`, name, name)
	}
	resourceMethods := `
// Index 显示资源列表。
func (controller *%[1]s) Index%[2]s() {}

// Save 保存新建资源。
func (controller *%[1]s) Save%[2]s(request *framework.Request) {}

// Read 显示指定资源。
func (controller *%[1]s) Read%[2]s(id int) {}

// Update 保存更新资源。
func (controller *%[1]s) Update%[2]s(request *framework.Request, id int) {}

// Delete 删除指定资源。
func (controller *%[1]s) Delete%[2]s(id int) {}
`
	if !api {
		resourceMethods = `
// Index 显示资源列表。
func (controller *%[1]s) Index%[2]s() {}

// Create 显示创建资源表单。
func (controller *%[1]s) Create%[2]s() {}

// Save 保存新建资源。
func (controller *%[1]s) Save%[2]s(request *framework.Request) {}

// Read 显示指定资源。
func (controller *%[1]s) Read%[2]s(id int) {}

// Edit 显示编辑资源表单。
func (controller *%[1]s) Edit%[2]s(id int) {}

// Update 保存更新资源。
func (controller *%[1]s) Update%[2]s(request *framework.Request, id int) {}

// Delete 删除指定资源。
func (controller *%[1]s) Delete%[2]s(id int) {}
`
	}
	return fmt.Sprintf(`package controller

import framework "github.com/zhuhanxin0308/thinkgo/v3"

// %s 控制器。
type %s struct {
}
%s`, name, name, fmt.Sprintf(resourceMethods, name, actionSuffix))
}
