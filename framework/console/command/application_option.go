package command

import "thinkgo/framework/console"

const applicationOptionName = "app"

// configureApplicationOption 为需要应用上下文的命令统一声明 --app/-a 选项。
func configureApplicationOption(command *console.Command) {
	if command == nil {
		return
	}
	command.AddOption(applicationOptionName, "a", "Target application name", "")
}
