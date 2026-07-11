package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid listener name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "listener")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create listener directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Listener %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package listener

import (
	"fmt"
	"thinkgo/framework/event"
)

// %s 监听器。
type %s struct{}

// Handle 处理框架事件。
func (l *%s) Handle(event event.Event) {
	fmt.Printf("Event received: %%s\n", event.Name())
}
`, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create listener: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Listener %s created successfully.", name))
}
