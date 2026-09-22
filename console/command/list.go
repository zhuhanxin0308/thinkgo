package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// List command
type List struct {
	console.Command
	Console *console.Console
}

func (c *List) Configure() {
	c.Signature = "list"
	c.Description = "List available commands"
	c.AddArgument("namespace", "The namespace name", false)
	c.AddBoolOption("raw", "", "To output raw command list")
}

func (c *List) Execute(input *console.Input, output *console.Output) error {
	if c.Console == nil {
		return fmt.Errorf("命令行实例不可用")
	}
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	return c.Console.ShowCommandList(input.GetArgument(0), input.GetOption("raw") == "true")
}
