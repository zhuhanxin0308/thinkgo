package command

import (
	"thinkgo/framework/console"
)

// Version command
type Version struct {
	console.Command
}

func (c *Version) Configure() {
	c.Signature = "version"
	c.Description = "Show version information"
}

func (c *Version) Execute(input *console.Input, output *console.Output) {
	output.Info("ThinkGo Framework v1.0.0")
}
