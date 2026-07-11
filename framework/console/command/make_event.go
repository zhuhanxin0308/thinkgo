package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid event name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "event")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create event directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Event %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package event

// %s 事件。
type %s struct {
	Payload map[string]interface{}
}

// Name 返回事件名称。
func (e *%s) Name() string {
	return "%s"
}
`, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create event: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Event %s created successfully.", name))
}
