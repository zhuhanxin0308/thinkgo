package command

import (
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

func (c *List) Execute(input *console.Input, output *console.Output) {
	if c.Console != nil {
		c.Console.ShowHelp()
	}
}
