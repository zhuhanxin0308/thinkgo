package command

import (
	"fmt"
	"path/filepath"

	"thinkgo/framework/console"
)

// MakeListener 生成事件监听器源码。
type MakeListener struct {
	console.Command
}

// Configure 配置监听器生成命令。
func (c *MakeListener) Configure() {
	c.Signature = "make:listener"
	c.Description = "Create a new listener class"
	c.AddArgument("name", "Listener type name", true)
}

// Execute 校验名称后安全创建监听器文件。
func (c *MakeListener) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(c.App, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid listener name: %w", err)
	}

	// 模板实现可返回错误的事件监听器接口。
	content := fmt.Sprintf(`package listener

import (
	"fmt"
	"thinkgo/framework/event"
)

// %s 监听器。
type %s struct{}

// Handle 处理框架事件。
func (l *%s) Handle(currentEvent event.Event) error {
	if currentEvent == nil {
		return event.ErrInvalidEvent
	}
	fmt.Printf("Event received: %%s\n", currentEvent.Name())
	return nil
}
`, name, name, name)

	if err := writeGeneratedAppSource(c.App, filepath.Join("app", "listener"), lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create listener %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Listener %s created successfully.", name))
	return nil
}
