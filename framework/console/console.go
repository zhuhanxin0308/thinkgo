package console

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"thinkgo/framework"
	"thinkgo/framework/config"
)

// Console 管理命令注册、严格输入解析、执行和确定性帮助输出。
type Console struct {
	App *framework.App

	mu        sync.RWMutex
	commands  map[string]ICommand
	output    *Output
	manager   *framework.ApplicationManager
	name      string
	version   string
	user      string
	autoPath  string
	configErr error
}

// NewConsole 创建命令行应用。
func NewConsole(app *framework.App) *Console {
	console := &Console{
		App:      app,
		commands: make(map[string]ICommand),
		output:   NewOutput(),
		name:     "ThinkGo Console Tool",
	}
	console.applyAppConfig()
	return console
}

// NewConsoleWithManager 创建绑定应用管理器的控制台，默认使用管理器的默认应用。
func NewConsoleWithManager(manager *framework.ApplicationManager) *Console {
	if manager == nil {
		return NewConsole(nil)
	}
	console := &Console{
		App:      manager.DefaultApplication(),
		manager:  manager,
		commands: make(map[string]ICommand),
		output:   NewOutput(),
	}
	console.name = "ThinkGo Console Tool"
	console.applyAppConfig()
	return console
}

// Name 返回控制台显示名称。
func (c *Console) Name() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.name
}

// Version 返回控制台显示版本。
func (c *Console) Version() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// User 返回控制台配置中的执行用户标识；框架不会擅自切换操作系统用户。
func (c *Console) User() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.user
}

// AutoPath 返回控制台命令自动路径，代码生成器据此选择命令输出目录。
func (c *Console) AutoPath() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.autoPath
}

// applyAppConfig 应用 console.json，并把非法字段保存为可观测错误。
func (c *Console) applyAppConfig() {
	if c == nil || c.App == nil {
		return
	}
	configuration, err := framework.ResolveServiceAs[*config.Config](c.App, framework.ServiceConfig)
	if err != nil {
		c.mu.Lock()
		c.configErr = fmt.Errorf("%w: 应用配置服务不可用: %v", ErrInvalidConfig, err)
		c.mu.Unlock()
		return
	}
	values := configuration.GetMap("console")
	if len(values) == 0 {
		return
	}
	var configErr error
	for key := range values {
		switch key {
		case "name", "version", "user", "auto_path":
		default:
			configErr = errors.Join(configErr, fmt.Errorf("console 配置包含未知字段 %q", key))
		}
	}
	name := c.name
	version := c.version
	user := ""
	autoPath := ""
	if raw, exists := values["name"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasConsoleControl(value) {
			configErr = errors.Join(configErr, fmt.Errorf("console.name 必须是非空字符串"))
		} else {
			name = value
		}
	}
	if raw, exists := values["version"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" || hasConsoleControl(value) {
			configErr = errors.Join(configErr, fmt.Errorf("console.version 必须是非空字符串"))
		} else {
			version = value
		}
	}
	if raw, exists := values["user"]; exists && raw != nil {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != value || hasConsoleControl(value) {
			configErr = errors.Join(configErr, fmt.Errorf("console.user 必须是安全字符串或 null"))
		} else {
			user = value
		}
	}
	if raw, exists := values["auto_path"]; exists {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != value || hasConsoleControl(value) {
			configErr = errors.Join(configErr, fmt.Errorf("console.auto_path 必须是安全路径字符串"))
		} else {
			autoPath = value
			if value != "" && strings.TrimSpace(c.App.BasePath) != "" && !consolePathWithinBase(c.App.BasePath, value) {
				configErr = errors.Join(configErr, fmt.Errorf("console.auto_path 必须位于项目根目录下: %s", value))
			}
		}
	}
	c.mu.Lock()
	c.name = name
	c.version = version
	c.user = user
	c.autoPath = autoPath
	if configErr != nil {
		c.configErr = fmt.Errorf("%w: %v", ErrInvalidConfig, configErr)
	}
	c.mu.Unlock()
}

// configurationError 返回控制台配置错误快照。
func (c *Console) configurationError() error {
	if c == nil {
		return ErrInvalidConfig
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.configErr
}

// SetApplicationManager 为控制台和已注册命令设置应用管理器。
func (c *Console) SetApplicationManager(manager *framework.ApplicationManager) error {
	if c == nil || manager == nil {
		return fmt.Errorf("%w: 应用管理器不能为空", ErrInvalidCommand)
	}
	defaultApp := manager.DefaultApplication()
	if defaultApp == nil {
		return fmt.Errorf("%w: 默认应用不可用", ErrInvalidCommand)
	}
	c.mu.Lock()
	c.manager = manager
	c.App = defaultApp
	for _, command := range c.commands {
		if managerAware, ok := command.(interface {
			SetApplicationManager(*framework.ApplicationManager)
		}); ok {
			managerAware.SetApplicationManager(manager)
		}
	}
	c.mu.Unlock()
	return nil
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
	if managerAware, ok := command.(interface {
		SetApplicationManager(*framework.ApplicationManager)
	}); ok {
		managerAware.SetApplicationManager(c.manager)
	}
	c.commands[signature] = command
	return nil
}

// Run 执行给定参数；空参数展示帮助。所有解析、命令和输出错误都会返回调用方。
func (c *Console) Run(args ...string) error {
	if c == nil {
		return fmt.Errorf("%w: Console 不能为空", ErrInvalidCommand)
	}
	if err := c.configurationError(); err != nil {
		return err
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
	if applicationAware, ok := command.(interface{ SelectApplication(*Input) error }); ok {
		if err := applicationAware.SelectApplication(input); err != nil {
			return fmt.Errorf("命令 %q 选择应用失败: %w", name, err)
		}
	}
	executionErr := command.Execute(input, output)
	return errors.Join(executionErr, output.Err())
}

// ShowHelp 按命令名排序展示帮助，保证输出可复现。
func (c *Console) ShowHelp() error {
	if c == nil {
		return fmt.Errorf("%w: Console 不能为空", ErrInvalidCommand)
	}
	if err := c.configurationError(); err != nil {
		return err
	}
	c.mu.RLock()
	output := c.output
	name := c.name
	version := c.version
	user := c.user
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

	header := name
	if version != "" {
		header += " v" + version
	}
	output.Writeln(header)
	if user != "" {
		output.Writeln("User: " + escapeTerminalControls(user))
	}
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

// hasConsoleControl 检查控制台元数据是否包含不可安全输出的控制字符。
func hasConsoleControl(value string) bool {
	if !utf8.ValidString(value) {
		return true
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// consolePathWithinBase 限制命令生成目录在项目根目录内，避免配置路径造成文件写出项目边界。
func consolePathWithinBase(basePath, configuredPath string) bool {
	baseAbsolute, err := filepath.Abs(strings.TrimSpace(basePath))
	if err != nil {
		return false
	}
	candidate := configuredPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(baseAbsolute, candidate)
	}
	candidate, err = filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(baseAbsolute, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
