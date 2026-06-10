package command

import (
	"os"
	"thinkgo/framework/console"
)

// Clear command
type Clear struct {
	console.Command
}

func (c *Clear) Configure() {
	c.Signature = "clear"
	c.Description = "Clear application cache"
}

func (c *Clear) Execute(input *console.Input, output *console.Output) {
	// Clear runtime directory
	runtimePath := c.App.BasePath + "/runtime"
	if err := os.RemoveAll(runtimePath); err != nil {
		output.Error("Failed to clear cache: " + err.Error())
		return
	}
	// Recreate runtime directory
	os.MkdirAll(runtimePath, 0755)
	
	output.Success("Cache cleared successfully.")
}
