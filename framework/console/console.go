package console

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework"
)

// Console manages commands
type Console struct {
	App      *framework.App
	commands map[string]ICommand
}

// NewConsole creates a new Console
func NewConsole(app *framework.App) *Console {
	return &Console{
		App:      app,
		commands: make(map[string]ICommand),
	}
}

// Register registers a command
func (c *Console) Register(cmd ICommand) {
	cmd.Configure()
	// Inject App if possible
	if baseCmd, ok := cmd.(interface{ SetApp(*framework.App) }); ok {
		baseCmd.SetApp(c.App)
	}
	// Also check if it's a pointer to a struct embedding Command
	// Go interfaces are tricky with embedded structs.
	// For now, we assume commands implement ICommand.
	
	// Parse signature to get command name
	// Signature might be "make:controller {name}"
	sig := cmd.GetSignature()
	parts := strings.Split(sig, " ")
	name := parts[0]
	
	c.commands[name] = cmd
}

// Run executes the console application
func (c *Console) Run() {
	args := os.Args[1:]
	if len(args) == 0 {
		c.ShowHelp()
		return
	}

	name := args[0]
	
	if name == "list" {
		c.ShowHelp()
		return
	}

	if cmd, ok := c.commands[name]; ok {
		input := NewInput()
		// Pass args excluding command name
		input.Args = args[1:]
		// 依据命令声明的参数/选项定义解析输入，填充 Options/Arguments。
		input.Parse(cmd.GetArgumentDefinitions(), cmd.GetOptionDefinitions())

		output := NewOutput()
		cmd.Execute(input, output)
	} else {
		fmt.Printf("Command \"%s\" not found.\n", name)
	}
}

// ShowHelp shows available commands
func (c *Console) ShowHelp() {
	fmt.Println("ThinkGo Console Tool")
	fmt.Println("Usage:")
	fmt.Println("  command [arguments]")
	fmt.Println("\nAvailable commands:")
	for _, cmd := range c.commands {
		// Extract name from signature
		sig := cmd.GetSignature()
		parts := strings.Split(sig, " ")
		name := parts[0]

		fmt.Printf("  %-20s %s\n", name, cmd.GetDescription())

		// 展示命令声明的选项（如 run 的 --port），让帮助不再隐藏可用参数。
		for _, opt := range cmd.GetOptionDefinitions() {
			flag := "--" + opt.Name
			if opt.Short != "" {
				flag = "-" + opt.Short + ", " + flag
			}
			fmt.Printf("    %-18s %s\n", flag, opt.Description)
		}
	}
}
