package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeEvent command
type MakeEvent struct {
	console.Command
}

func (c *MakeEvent) Configure() {
	c.Signature = "make:event"
	c.Description = "Create a new event class"
}

func (c *MakeEvent) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Event name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/event", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Event %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package event

// %s event
type %s struct {
	// Event data
}
`, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create event: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Event %s created successfully.", name))
}
