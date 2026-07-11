package console

import "thinkgo/framework"

// ICommand interface
type ICommand interface {
	Configure()
	Execute(input *Input, output *Output)
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

// Execute executes the command
func (c *Command) Execute(input *Input, output *Output) {
	if output == nil {
		return
	}
	signature := c.Signature
	if signature == "" {
		signature = "unknown"
	}
	// 基础命令不提供业务行为，具体命令必须覆盖 Execute；这里显式报错避免静默成功。
	output.Error("command " + signature + " missing implementation")
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
	c.optionDefs = append(c.optionDefs, OptionDefinition{
		Name:        name,
		Short:       short,
		Description: description,
		Default:     def,
	})
	return c
}

// AddBoolOption 声明布尔开关选项（出现即为 true，不消费后续位置参数）。
func (c *Command) AddBoolOption(name, short, description string) *Command {
	c.optionDefs = append(c.optionDefs, OptionDefinition{
		Name:        name,
		Short:       short,
		Description: description,
		Bool:        true,
	})
	return c
}

// AddArgument 声明命令支持的位置参数，供帮助展示与 Input.Parse 解析使用。
func (c *Command) AddArgument(name, description string, required bool) *Command {
	c.argDefs = append(c.argDefs, ArgumentDefinition{
		Name:        name,
		Description: description,
		Required:    required,
	})
	return c
}

// GetOptionDefinitions 返回命令声明的选项定义。
func (c *Command) GetOptionDefinitions() []OptionDefinition {
	return c.optionDefs
}

// GetArgumentDefinitions 返回命令声明的位置参数定义。
func (c *Command) GetArgumentDefinitions() []ArgumentDefinition {
	return c.argDefs
}
