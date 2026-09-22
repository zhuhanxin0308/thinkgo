package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeValidate 生成可直接配置规则的验证器源码。
type MakeValidate struct {
	console.Command
}

// Configure 配置验证器生成命令。
func (c *MakeValidate) Configure() {
	c.Signature = "make:validate"
	c.Description = "Create a new validator class"
	c.AddArgument("name", "Validator type name", true)
	addApplicationSelectionOption(&c.Command)
}

// Execute 校验名称后安全创建验证器文件。
func (c *MakeValidate) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid validator name: %w", err)
	}
	name := target.name

	// 模板直接使用无共享调用状态的新验证器 API。
	content := fmt.Sprintf(`package validate

import (
	"github.com/zhuhanxin0308/thinkgo/v3/validate"
)

// %s 验证器
type %s struct {
	validate.Validator
}

// New%s 创建验证器并复制规则与消息配置。
func New%s() *%s {
	v := &%s{}
	v.SetRules(map[string]string{
		"name": "required|max:25",
	})
	v.SetMessages(map[string]string{
		"name.required": "名称不能为空",
		"name.max":      "名称长度不能超过 25",
	})
	return v
}
`, name, name, name, name, name, name)

	if err := writeAndRefreshGeneratedApplicationSource(c.App, target, "validate", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create validator %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Validator %s created successfully.", name))
	return nil
}
