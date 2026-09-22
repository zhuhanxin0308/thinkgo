package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeEvent command
type MakeEvent struct {
	console.Command
}

func (c *MakeEvent) Configure() {
	c.Signature = "make:event"
	c.Description = "Create a new event class"
	c.AddArgument("name", "Event type name", true)
	addApplicationSelectionOption(&c.Command)
}

func (c *MakeEvent) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid event name: %w", err)
	}
	name := target.name

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

	if err := writeGeneratedAppSource(c.App, target, "event", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create event %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Event %s created successfully.", name))
	return nil
}
