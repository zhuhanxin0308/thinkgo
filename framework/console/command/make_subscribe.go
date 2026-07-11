package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid subscriber name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "subscribe")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create subscriber directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

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

// %s 订阅者。
type %s struct {
	LastEventName string
}

// Subscribe 注册订阅者自身，默认监听全部事件并记录最近一次事件名。
func (s *%s) Subscribe(dispatcher *event.Dispatcher) {
	if dispatcher == nil {
		return
	}
	dispatcher.Listen("*", s)
}

// Handle 记录最近一次收到的事件名称。
func (s *%s) Handle(event event.Event) {
	if event != nil {
		s.LastEventName = event.Name()
	}
}
`, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create subscriber: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Subscriber %s created successfully.", name))
}
