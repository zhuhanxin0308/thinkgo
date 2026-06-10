package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeSubscribe command
type MakeSubscribe struct {
	console.Command
}

func (c *MakeSubscribe) Configure() {
	c.Signature = "make:subscribe"
	c.Description = "Create a new subscriber class"
}

func (c *MakeSubscribe) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Subscriber name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/subscribe", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Subscriber %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package subscribe

import (
	"thinkgo/framework/event"
)

// %s subscriber
type %s struct{}

func (s *%s) Subscribe(dispatcher *event.Dispatcher) {
	// dispatcher.Listen("EventName", Listener)
}
`, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create subscriber: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Subscriber %s created successfully.", name))
}
