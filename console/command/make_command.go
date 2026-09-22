package command

import (
	"fmt"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeCommand 创建可以注册到应用控制台的命令类型。
type MakeCommand struct {
	console.Command
}

func (c *MakeCommand) Configure() {
	c.Signature = "make:command"
	c.Description = "Create a new console command class"
	c.AddArgument("name", "Command type name", true)
	c.AddArgument("commandName", "The name of the command", false)
	addApplicationSelectionOption(&c.Command)
}

func (c *MakeCommand) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid command name: %w", err)
	}
	name := target.name
	signature := input.GetArgument(1)
	if signature == "" {
		signature = strings.ToLower(name)
	}
	if err := console.ValidateCommandName(signature); err != nil {
		return err
	}

	// 命令签名独立于 Go 类型名，并使用 Go 字符串字面量编码。
	content := fmt.Sprintf(`package command

import (
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// %s 是应用自定义命令。
type %s struct {
	console.Command
}

func (c *%s) Configure() {
	c.Signature = %q
	c.Description = "应用命令 %s"
}

func (c *%s) Execute(input *console.Input, output *console.Output) error {
	output.Info(c.GetSignature() + " " + c.GetDescription())
	return nil
}
`, name, name, name, signature, name, name)

	if err := writeGeneratedAppSource(c.App, target, "command", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create command %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Command %s created successfully.", name))
	return nil
}
