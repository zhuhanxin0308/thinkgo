package console

import "strings"

// NormalizeInformationalArguments 将全局帮助和版本选项转换为信息命令。
// 宿主必须在编译业务包、加载配置或连接外部服务前调用；分隔符后的值保持原样。
func NormalizeInformationalArguments(args []string) []string {
	result := append([]string{}, args...)
	command := ""
	help := false
	version := false
	for _, argument := range args {
		if argument == "--" {
			break
		}
		switch argument {
		case "--help", "-h":
			help = true
		case "--version", "-V":
			version = true
		case "-v":
			// 命令之后的小写短选项仍归命令所有，兼容已有 verbose 声明。
			if command == "" {
				version = true
			}
		default:
			if command == "" && !strings.HasPrefix(argument, "-") {
				command = argument
			}
		}
	}
	if version {
		return []string{"version"}
	}
	if help {
		if command == "" {
			return []string{"help"}
		}
		return []string{"help", command}
	}
	return result
}
