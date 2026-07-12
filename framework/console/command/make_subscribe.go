package command

import (
	"fmt"
	"path/filepath"

	"thinkgo/framework/console"
)

// MakeSubscribe 生成事件订阅者源码。
type MakeSubscribe struct {
	console.Command
}

// Configure 配置订阅者生成命令。
func (c *MakeSubscribe) Configure() {
	c.Signature = "make:subscribe"
	c.Description = "Create a new subscriber class"
	c.AddArgument("name", "Subscriber type name", true)
}

// Execute 校验名称后安全创建订阅者文件。
func (c *MakeSubscribe) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(c.App, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid subscriber name: %w", err)
	}

	// 模板实现可返回错误的订阅和监听接口。
	content := fmt.Sprintf(`package subscribe

import (
	"thinkgo/framework/event"
)

// %s 订阅者。
type %s struct {
	LastEventName string
}

// Subscribe 注册订阅者自身，默认监听全部事件并记录最近一次事件名。
func (s *%s) Subscribe(dispatcher *event.Dispatcher) error {
	if dispatcher == nil {
		return event.ErrInvalidSubscriber
	}
	return dispatcher.Listen("*", s)
}

// Handle 记录最近一次收到的事件名称。
func (s *%s) Handle(currentEvent event.Event) error {
	if currentEvent == nil {
		return event.ErrInvalidEvent
	}
	s.LastEventName = currentEvent.Name()
	return nil
}
`, name, name, name, name)

	if err := writeGeneratedAppSource(c.App, filepath.Join("app", "subscribe"), lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create subscriber %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Subscriber %s created successfully.", name))
	return nil
}
