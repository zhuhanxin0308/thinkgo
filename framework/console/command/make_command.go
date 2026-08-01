package command

import (
	"fmt"
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
	c.AddArgument("name", "Command type name", true)
	configureApplicationOption(&c.Command)
}

func (c *MakeCommand) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid command name: %w", err)
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

func (c *%s) Execute(input *console.Input, output *console.Output) error {
	output.Info(c.GetSignature() + " " + c.GetDescription())
	return nil
}
`, name, name, name, strings.ToLower(name), name, name)

	if err := writeGeneratedAppSource(c.App, "command", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create command %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Command %s created successfully.", name))
	return nil
}
