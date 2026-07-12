package console

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"thinkgo/framework"
)

// Console 管理命令注册、严格输入解析、执行和确定性帮助输出。
type Console struct {
	App *framework.App

	mu       sync.RWMutex
	commands map[string]ICommand
	output   *Output
}

// NewConsole 创建命令行应用。
func NewConsole(app *framework.App) *Console {
	return &Console{
		App:      app,
		commands: make(map[string]ICommand),
		output:   NewOutput(),
	}
}

// SetOutput 设置命令标准流，主要用于嵌入和测试。
func (c *Console) SetOutput(output *Output) error {
	if c == nil || output == nil {
		return ErrInvalidOutput
	}
	if err := output.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	c.mu.Lock()
	c.output = output
	c.mu.Unlock()
	return nil
}

// Register 注册命令；非法或重复命令返回错误，不再静默覆盖。
func (c *Console) Register(command ICommand) error {
	if c == nil || isNilCommand(command) {
		return fmt.Errorf("%w: 命令不能为空", ErrInvalidCommand)
	}
	command.Configure()
	signature := command.GetSignature()
	description := command.GetDescription()
	if !validCommandName(signature) {
		return fmt.Errorf("%w: 签名 %q 非法", ErrInvalidCommand, signature)
	}
	if strings.TrimSpace(description) == "" || escapeTerminalControls(description) != description {
		return fmt.Errorf("%w: 命令 %q 的描述为空或包含控制字符", ErrInvalidCommand, signature)
	}
	if _, _, _, err := validateInputDefinitions(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		return fmt.Errorf("%w: 命令 %q 的参数声明错误: %v", ErrInvalidCommand, signature, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.commands == nil {
		c.commands = make(map[string]ICommand)
	}
	if _, duplicated := c.commands[signature]; duplicated {
		return fmt.Errorf("%w: %q", ErrDuplicateCommand, signature)
	}
	if appAware, ok := command.(interface{ SetApp(*framework.App) }); ok {
		appAware.SetApp(c.App)
	}
	c.commands[signature] = command
	return nil
}

// Run 执行给定参数；空参数展示帮助。所有解析、命令和输出错误都会返回调用方。
func (c *Console) Run(args ...string) error {
	if c == nil {
		return fmt.Errorf("%w: Console 不能为空", ErrInvalidCommand)
	}
	if len(args) == 0 {
		return c.ShowHelp()
	}
	name := args[0]
	c.mu.RLock()
	command, exists := c.commands[name]
	output := c.output
	c.mu.RUnlock()
	if !exists {
		return fmt.Errorf("%w: %q", ErrCommandNotFound, name)
	}
	if output == nil {
		return ErrInvalidOutput
	}

	input := NewInput(args[1:]...)
	if err := input.Parse(command.GetArgumentDefinitions(), command.GetOptionDefinitions()); err != nil {
		return fmt.Errorf("命令 %q: %w", name, err)
	}
	executionErr := command.Execute(input, output)
	return errors.Join(executionErr, output.Err())
}

// ShowHelp 按命令名排序展示帮助，保证输出可复现。
func (c *Console) ShowHelp() error {
	if c == nil {
		return fmt.Errorf("%w: Console 不能为空", ErrInvalidCommand)
	}
	c.mu.RLock()
	output := c.output
	names := make([]string, 0, len(c.commands))
	commands := make(map[string]ICommand, len(c.commands))
	for name, command := range c.commands {
		names = append(names, name)
		commands[name] = command
	}
	c.mu.RUnlock()
	if output == nil {
		return ErrInvalidOutput
	}
	sort.Strings(names)

	output.Writeln("ThinkGo Console Tool")
	output.Writeln("Usage:")
	output.Writeln("  think <command> [arguments] [options]")
	output.Writeln("")
	output.Writeln("Available commands:")
	for _, name := range names {
		command := commands[name]
		output.Writeln(fmt.Sprintf("  %-20s %s", name, command.GetDescription()))
		for _, argument := range command.GetArgumentDefinitions() {
			required := "optional"
			if argument.Required {
				required = "required"
			}
			output.Writeln(fmt.Sprintf("    %-18s %s (%s)", "<"+argument.Name+">", escapeTerminalControls(argument.Description), required))
		}
		for _, option := range command.GetOptionDefinitions() {
			flag := "--" + option.Name
			if option.Short != "" {
				flag = "-" + option.Short + ", " + flag
			}
			description := escapeTerminalControls(option.Description)
			if option.Default != "" {
				description += " (default: " + escapeTerminalControls(option.Default) + ")"
			}
			output.Writeln(fmt.Sprintf("    %-18s %s", flag, description))
		}
	}
	return output.Err()
}

func isNilCommand(command ICommand) bool {
	if command == nil {
		return true
	}
	value := reflect.ValueOf(command)
	return value.Kind() == reflect.Ptr && value.IsNil()
}

func validCommandName(name string) bool {
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	for _, segment := range strings.Split(name, ":") {
		if segment == "" {
			return false
		}
		for index, current := range segment {
			if current >= 'a' && current <= 'z' || index > 0 && (current >= '0' && current <= '9' || current == '-') {
				continue
			}
			return false
		}
	}
	return true
}
