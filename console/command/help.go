package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// Help 展示指定命令帮助，对应 ThinkPHP 内置 help 命令。
type Help struct {
	console.Command
	Console *console.Console
}

// Configure 声明 help [command_name] [--raw] 调用契约。
func (command *Help) Configure() {
	command.Signature = "help"
	command.Description = "Displays help for a command"
	command.AddArgument("command_name", "The command name", false)
	command.AddBoolOption("raw", "", "To output raw command help")
}

// Execute 输出指定命令的帮助；省略命令名时展示 help 自身。
func (command *Help) Execute(input *console.Input, output *console.Output) error {
	if command.Console == nil {
		return fmt.Errorf("命令行实例不可用")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	name := "help"
	if input != nil && input.GetArgument(0) != "" {
		name = input.GetArgument(0)
	}
	raw := input != nil && input.GetOption("raw") == "true"
	return command.Console.ShowCommandHelp(name, raw)
}
