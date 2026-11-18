package console

import "strings"

// NormalizeInformationalArguments 将全局帮助和版本选项转换为信息命令。
// 宿主必须在编译业务包、加载配置或连接外部服务前调用；分隔符后的值保持原样。
func NormalizeInformationalArguments(args []string) []string {
	result := append([]string{}, args...)
	command := ""
	help := false
	version := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
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
			if command != "" {
				continue
			}
			if looksLikeOptionToken(argument) && !isNegativeNumber(argument) {
				if !strings.Contains(argument, "=") && preCommandOptionConsumesNext(args, index) {
					index++
				}
				continue
			}
			if validCommandName(argument) {
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

func preCommandOptionConsumesNext(args []string, index int) bool {
	if index+1 >= len(args) {
		return false
	}
	next := args[index+1]
	if next == "--" || isInformationalFlag(next) {
		return false
	}
	if looksLikeOptionToken(next) && !isNegativeNumber(next) {
		return false
	}
	if !validCommandName(next) {
		return true
	}
	for rest := index + 2; rest < len(args); rest++ {
		candidate := args[rest]
		if candidate == "--" {
			break
		}
		if isInformationalFlag(candidate) {
			continue
		}
		if looksLikeOptionToken(candidate) && !isNegativeNumber(candidate) {
			continue
		}
		if validCommandName(candidate) {
			return true
		}
	}
	return false
}

func isInformationalFlag(token string) bool {
	switch token {
	case "--help", "-h", "--version", "-V", "-v":
		return true
	default:
		return false
	}
}
