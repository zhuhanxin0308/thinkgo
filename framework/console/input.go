package console

import (
	"os"
	"strconv"
	"strings"
)

// Input handles console input
type Input struct {
	Args        []string
	Options     map[string]string
	Arguments   map[string]string
	positionals []string // Parse 解析出的位置参数（已剔除选项）
	parsed      bool     // 是否已调用 Parse
}

// NewInput creates a new Input
func NewInput() *Input {
	return &Input{
		Args:      os.Args[1:],
		Options:   make(map[string]string),
		Arguments: make(map[string]string),
	}
}

// Parse 根据声明的参数/选项定义解析命令行，填充 Options、Arguments 与位置参数。
// 支持的写法：--name value、--name=value、-s value、-s=value、布尔开关（--flag）；
// 短选项会按定义解析为其规范长名。未声明的选项同样会被收集（按出现的名称）。
func (i *Input) Parse(definitions []ArgumentDefinition, optionDefinitions []OptionDefinition) {
	known := make(map[string]OptionDefinition, len(optionDefinitions))
	shortToName := make(map[string]string, len(optionDefinitions))
	for _, opt := range optionDefinitions {
		known[opt.Name] = opt
		if opt.Short != "" {
			shortToName[opt.Short] = opt.Name
		}
		if opt.Default != "" {
			i.Options[opt.Name] = opt.Default
		}
	}

	positionals := make([]string, 0, len(i.Args))
	args := i.Args
	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]
		if arg == "--" {
			positionals = append(positionals, args[idx+1:]...)
			break
		}

		var body string
		switch {
		case strings.HasPrefix(arg, "--"):
			body = arg[2:]
		case len(arg) > 1 && strings.HasPrefix(arg, "-"):
			body = arg[1:]
			if canonical, ok := shortToName[strings.SplitN(body, "=", 2)[0]]; ok {
				// 用规范长名替换短名（保留可能存在的 =value 部分）。
				if _, after, found := strings.Cut(body, "="); found {
					body = canonical + "=" + after
				} else {
					body = canonical
				}
			}
		default:
			positionals = append(positionals, arg)
			continue
		}

		name, inlineValue, hasInline := strings.Cut(body, "=")
		if hasInline {
			i.Options[name] = inlineValue
			continue
		}

		// 布尔开关不消费后续 token；取值型选项只在后一个 token 是已声明选项时停止消费，
		// 因此 --offset -1 这类负数值不会被误判为新选项。
		def, isKnown := known[name]
		if !(isKnown && def.Bool) && idx+1 < len(args) && args[idx+1] != "--" && !isKnownOptionToken(args[idx+1], known, shortToName) {
			i.Options[name] = args[idx+1]
			idx++
		} else {
			i.Options[name] = "true"
		}
	}

	for idx, def := range definitions {
		if idx < len(positionals) {
			i.Arguments[def.Name] = positionals[idx]
		}
	}
	i.positionals = positionals
	i.parsed = true
}

// GetArgument gets a positional argument by index.
// 已调用 Parse 时返回剔除选项后的位置参数；否则回退到原始参数序列。
func (i *Input) GetArgument(index int) string {
	if i.parsed {
		if index >= 0 && index < len(i.positionals) {
			return i.positionals[index]
		}
		return ""
	}
	if index >= 0 && index < len(i.Args) {
		return i.Args[index]
	}
	return ""
}

// GetOption gets an option value.
// 优先返回 Parse 解析出的结果；未命中时回退到对原始参数的即时扫描，
// 以兼容未声明选项或未调用 Parse 的调用方（支持 --name value / --name=value / -s value / -s=value）。
func (i *Input) GetOption(name string) string {
	if value, ok := i.Options[name]; ok {
		return value
	}

	prefixes := []string{"--", "-"}
	for _, prefix := range prefixes {
		for idx, arg := range i.Args {
			if arg == "--" {
				break
			}
			if arg == prefix+name {
				if idx+1 < len(i.Args) && i.Args[idx+1] != "--" && (!looksLikeOptionToken(i.Args[idx+1]) || isNegativeNumber(i.Args[idx+1])) {
					return i.Args[idx+1]
				}
				return "true" // Boolean flag
			}
			if len(arg) > len(name)+len(prefix)+1 && arg[:len(name)+len(prefix)+1] == prefix+name+"=" {
				return arg[len(name)+len(prefix)+1:]
			}
		}
	}
	return ""
}

// isKnownOptionToken 判断 token 是否为已声明选项，避免把负数值误判成选项。
func isKnownOptionToken(token string, known map[string]OptionDefinition, shortToName map[string]string) bool {
	if token == "" || token == "-" || isNegativeNumber(token) {
		return false
	}
	if strings.HasPrefix(token, "--") {
		name := strings.TrimPrefix(token, "--")
		name = strings.SplitN(name, "=", 2)[0]
		_, ok := known[name]
		return ok
	}
	if strings.HasPrefix(token, "-") {
		name := strings.TrimPrefix(token, "-")
		name = strings.SplitN(name, "=", 2)[0]
		if _, ok := known[name]; ok {
			return true
		}
		_, ok := shortToName[name]
		return ok
	}
	return false
}

// looksLikeOptionToken 判断 token 是否具有选项形态，用于未 Parse 时的兼容扫描。
func looksLikeOptionToken(token string) bool {
	return strings.HasPrefix(token, "-") && token != "-"
}

// isNegativeNumber 判断 token 是否为负数值，允许 --offset -1 这类输入。
func isNegativeNumber(token string) bool {
	if len(token) < 2 || token[0] != '-' {
		return false
	}
	_, err := strconv.ParseFloat(token, 64)
	return err == nil
}

// ArgumentDefinition defines an argument
type ArgumentDefinition struct {
	Name        string
	Description string
	Required    bool
}

// OptionDefinition defines an option
type OptionDefinition struct {
	Name        string
	Short       string
	Description string
	Default     string
	// Bool 标记该选项为布尔开关：出现即为 true，且不消费其后的位置参数
	// （避免 `--force User` 把 User 误当作 --force 的取值）。
	Bool bool
}
