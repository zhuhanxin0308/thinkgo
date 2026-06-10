package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeCommand command
type MakeCommand struct {
	console.Command
}

func (c *MakeCommand) Configure() {
	c.Signature = "make:command"
	c.Description = "Create a new console command class"
}

func (c *MakeCommand) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Command name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/command", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Command %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package command

import (
	"thinkgo/framework/console"
)

// %s command
type %s struct {
	console.Command
}

func (c *%s) Configure() {
	c.Signature = "app:%s"
	c.Description = "Command description"
}

func (c *%s) Execute(input *console.Input, output *console.Output) {
	output.Info("Command %s executed")
}
`, name, name, name, strings.ToLower(name), name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create command: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Command %s created successfully.", name))
}
