package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeListener command
type MakeListener struct {
	console.Command
}

func (c *MakeListener) Configure() {
	c.Signature = "make:listener"
	c.Description = "Create a new listener class"
}

func (c *MakeListener) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Listener name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/listener", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Listener %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package listener

import (
	"fmt"
)

// %s listener
type %s struct{}

func (l *%s) Handle(event interface{}) {
	fmt.Printf("Event received: %%v\n", event)
}
`, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create listener: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Listener %s created successfully.", name))
}
