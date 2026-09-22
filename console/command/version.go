package command

import (
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
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
