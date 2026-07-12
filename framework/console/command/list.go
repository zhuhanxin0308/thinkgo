package command

import (
	"fmt"

	"thinkgo/framework/console"
)

// List command
type List struct {
	console.Command
	Console *console.Console
}

func (c *List) Configure() {
	c.Signature = "list"
	c.Description = "List available commands"
}

func (c *List) Execute(_ *console.Input, _ *console.Output) error {
	if c.Console == nil {
		return fmt.Errorf("命令行实例不可用")
	}
	return c.Console.ShowHelp()
}
