package console

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3"
)

// ICommand interface
type ICommand interface {
	Configure()
	Execute(input *Input, output *Output) error
	GetSignature() string
	GetDescription() string
	GetOptionDefinitions() []OptionDefinition
	GetArgumentDefinitions() []ArgumentDefinition
}

// Command base struct
type Command struct {
	Signature   string
	Description string
	App         *framework.App
	Input       *Input
	Output      *Output
	optionDefs  []OptionDefinition
	argDefs     []ArgumentDefinition
}

// Configure configures the command
func (c *Command) Configure() {}

// Execute 返回未实现错误，具体命令必须覆盖该方法。
func (c *Command) Execute(_ *Input, _ *Output) error {
	signature := c.Signature
	if signature == "" {
		signature = "unknown"
	}
	return fmt.Errorf("%w: %q", ErrCommandNotImplemented, signature)
}

// GetSignature returns the signature
func (c *Command) GetSignature() string {
	return c.Signature
}

// GetDescription returns the description
func (c *Command) GetDescription() string {
	return c.Description
}

// SetApp sets the app instance
func (c *Command) SetApp(app *framework.App) {
	c.App = app
}

// AddOption 声明命令支持的选项，供帮助展示与 Input.Parse 解析使用。
func (c *Command) AddOption(name, short, description, def string) *Command {
	return c.addOptionDefinition(OptionDefinition{
		Name:        name,
		Short:       short,
		Description: description,
		Default:     def,
	})
}

// AddBoolOption 声明布尔开关选项（出现即为 true，不消费后续位置参数）。
func (c *Command) AddBoolOption(name, short, description string) *Command {
	return c.addOptionDefinition(OptionDefinition{
		Name:        name,
		Short:       short,
		Description: description,
		Bool:        true,
	})
}

// AddArgument 声明命令支持的位置参数，供帮助展示与 Input.Parse 解析使用。
func (c *Command) AddArgument(name, description string, required bool) *Command {
	definition := ArgumentDefinition{
		Name:        name,
		Description: description,
		Required:    required,
	}
	for _, existing := range c.argDefs {
		if existing == definition {
			return c
		}
	}
	c.argDefs = append(c.argDefs, definition)
	return c
}

// addOptionDefinition 让重复 Configure 对完全相同的声明保持幂等；
// 同名但内容冲突的声明仍会保留，并由注册校验明确拒绝。
func (c *Command) addOptionDefinition(definition OptionDefinition) *Command {
	for _, existing := range c.optionDefs {
		if existing == definition {
			return c
		}
	}
	c.optionDefs = append(c.optionDefs, definition)
	return c
}

// GetOptionDefinitions 返回命令声明的选项定义。
func (c *Command) GetOptionDefinitions() []OptionDefinition {
	return append([]OptionDefinition(nil), c.optionDefs...)
}

// GetArgumentDefinitions 返回命令声明的位置参数定义。
func (c *Command) GetArgumentDefinitions() []ArgumentDefinition {
	return append([]ArgumentDefinition(nil), c.argDefs...)
}
