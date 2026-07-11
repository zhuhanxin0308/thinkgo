package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid command name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "command")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create command directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

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
	c.Description = "应用命令 %s"
}

func (c *%s) Execute(input *console.Input, output *console.Output) {
	output.Info(c.GetSignature() + " " + c.GetDescription())
}
`, name, name, name, strings.ToLower(name), name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create command: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Command %s created successfully.", name))
}
