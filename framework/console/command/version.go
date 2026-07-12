package command

import (
	"thinkgo/framework/console"
	frameworkVersion "thinkgo/framework/version"
)

// Version command
type Version struct {
	console.Command
}

func (c *Version) Configure() {
	c.Signature = "version"
	c.Description = "Show version information"
}

func (c *Version) Execute(_ *console.Input, output *console.Output) error {
	if output == nil {
		return console.ErrInvalidOutput
	}
	output.Info(frameworkVersion.Console)
	return nil
}
