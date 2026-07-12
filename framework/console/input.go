package console

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Input 保存一次命令执行的原始参数和严格解析结果。
type Input struct {
	Args        []string
	Options     map[string]string
	Arguments   map[string]string
	positionals []string
	parsed      bool
}

// NewInput 创建不依赖全局 os.Args 的命令输入。
func NewInput(args ...string) *Input {
	return &Input{
		Args:      append([]string(nil), args...),
		Options:   make(map[string]string),
		Arguments: make(map[string]string),
	}
}

// Parse 按命令声明严格解析参数；未知、重复、缺值和数量不匹配都会返回错误。
func (i *Input) Parse(definitions []ArgumentDefinition, optionDefinitions []OptionDefinition) error {
	if i == nil {
		return fmt.Errorf("%w: 输入不能为空", ErrInvalidInput)
	}
	known, shortToName, defaults, err := validateInputDefinitions(definitions, optionDefinitions)
	if err != nil {
		i.resetParsedState()
		return err
	}

	parsedOptions := defaults
	seenOptions := make(map[string]struct{}, len(optionDefinitions))
	positionals := make([]string, 0, len(i.Args))
	for index := 0; index < len(i.Args); index++ {
		token := i.Args[index]
		if token == "--" {
			positionals = append(positionals, i.Args[index+1:]...)
			break
		}
		if token == "-" || !strings.HasPrefix(token, "-") {
			positionals = append(positionals, token)
			continue
		}

		longForm := strings.HasPrefix(token, "--")
		body := strings.TrimPrefix(token, "-")
		if longForm {
			body = strings.TrimPrefix(body, "-")
		}
		name, inlineValue, hasInlineValue := strings.Cut(body, "=")
		if !longForm {
			canonical, exists := shortToName[name]
			if !exists {
				return i.failParse("未知短选项 %q", token)
			}
			name = canonical
		}
		definition, exists := known[name]
		if !exists {
			return i.failParse("未知选项 %q", token)
		}
		if _, duplicated := seenOptions[name]; duplicated {
			return i.failParse("选项 %q 重复", name)
		}
		seenOptions[name] = struct{}{}

		if definition.Bool {
			value := "true"
			if hasInlineValue {
				parsed, parseErr := strconv.ParseBool(inlineValue)
				if parseErr != nil {
					return i.failParse("布尔选项 %q 的值必须为 true 或 false", name)
				}
				value = strconv.FormatBool(parsed)
			}
			parsedOptions[name] = value
			continue
		}

		if hasInlineValue {
			parsedOptions[name] = inlineValue
			continue
		}
		if index+1 >= len(i.Args) || i.Args[index+1] == "--" ||
			(looksLikeOptionToken(i.Args[index+1]) && !isNegativeNumber(i.Args[index+1])) {
			return i.failParse("选项 %q 缺少值", name)
		}
		index++
		parsedOptions[name] = i.Args[index]
	}

	if len(positionals) > len(definitions) {
		return i.failParse("收到 %d 个位置参数，但命令最多接受 %d 个", len(positionals), len(definitions))
	}
	parsedArguments := make(map[string]string, len(definitions))
	for index, definition := range definitions {
		if index < len(positionals) {
			parsedArguments[definition.Name] = positionals[index]
			continue
		}
		if definition.Required {
			return i.failParse("缺少必填参数 %q", definition.Name)
		}
	}

	i.Options = parsedOptions
	i.Arguments = parsedArguments
	i.positionals = append([]string(nil), positionals...)
	i.parsed = true
	return nil
}

// GetArgument 返回解析后的位置参数；未解析时兼容读取原始参数。
func (i *Input) GetArgument(index int) string {
	if i == nil || index < 0 {
		return ""
	}
	if i.parsed {
		if index < len(i.positionals) {
			return i.positionals[index]
		}
		return ""
	}
	if index < len(i.Args) {
		return i.Args[index]
	}
	return ""
}

// GetOption 返回规范长名对应的选项值；未解析时保留有限的兼容扫描。
func (i *Input) GetOption(name string) string {
	if i == nil {
		return ""
	}
	if value, exists := i.Options[name]; exists {
		return value
	}
	if i.parsed {
		return ""
	}
	for _, prefix := range []string{"--", "-"} {
		for index, token := range i.Args {
			if token == "--" {
				break
			}
			if token == prefix+name {
				if index+1 < len(i.Args) && i.Args[index+1] != "--" &&
					(!looksLikeOptionToken(i.Args[index+1]) || isNegativeNumber(i.Args[index+1])) {
					return i.Args[index+1]
				}
				return "true"
			}
			if strings.HasPrefix(token, prefix+name+"=") {
				return strings.TrimPrefix(token, prefix+name+"=")
			}
		}
	}
	return ""
}

func (i *Input) failParse(format string, values ...interface{}) error {
	i.resetParsedState()
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, values...))
}

func (i *Input) resetParsedState() {
	i.Options = make(map[string]string)
	i.Arguments = make(map[string]string)
	i.positionals = nil
	i.parsed = false
}

func validateInputDefinitions(arguments []ArgumentDefinition, options []OptionDefinition) (map[string]OptionDefinition, map[string]string, map[string]string, error) {
	known := make(map[string]OptionDefinition, len(options))
	shortToName := make(map[string]string, len(options))
	defaults := make(map[string]string, len(options))
	optionalSeen := false
	argumentNames := make(map[string]struct{}, len(arguments))
	for _, definition := range arguments {
		if !isCLIIdentifier(definition.Name, false) {
			return nil, nil, nil, fmt.Errorf("%w: 参数名 %q 非法", ErrInvalidInput, definition.Name)
		}
		if _, duplicated := argumentNames[definition.Name]; duplicated {
			return nil, nil, nil, fmt.Errorf("%w: 参数名 %q 重复", ErrInvalidInput, definition.Name)
		}
		argumentNames[definition.Name] = struct{}{}
		if !definition.Required {
			optionalSeen = true
		} else if optionalSeen {
			return nil, nil, nil, fmt.Errorf("%w: 必填参数 %q 不能位于可选参数之后", ErrInvalidInput, definition.Name)
		}
	}
	for _, definition := range options {
		if !isCLIIdentifier(definition.Name, false) {
			return nil, nil, nil, fmt.Errorf("%w: 选项名 %q 非法", ErrInvalidInput, definition.Name)
		}
		if _, duplicated := known[definition.Name]; duplicated {
			return nil, nil, nil, fmt.Errorf("%w: 选项名 %q 重复", ErrInvalidInput, definition.Name)
		}
		if definition.Short != "" {
			if !isCLIIdentifier(definition.Short, true) {
				return nil, nil, nil, fmt.Errorf("%w: 短选项 %q 非法", ErrInvalidInput, definition.Short)
			}
			if _, duplicated := shortToName[definition.Short]; duplicated {
				return nil, nil, nil, fmt.Errorf("%w: 短选项 %q 重复", ErrInvalidInput, definition.Short)
			}
			shortToName[definition.Short] = definition.Name
		}
		if definition.Bool && definition.Default != "" {
			parsed, parseErr := strconv.ParseBool(definition.Default)
			if parseErr != nil {
				return nil, nil, nil, fmt.Errorf("%w: 布尔选项 %q 的默认值非法", ErrInvalidInput, definition.Name)
			}
			defaults[definition.Name] = strconv.FormatBool(parsed)
		} else if definition.Default != "" {
			defaults[definition.Name] = definition.Default
		}
		known[definition.Name] = definition
	}
	return known, shortToName, defaults, nil
}

func isCLIIdentifier(value string, short bool) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	runes := []rune(value)
	if short && len(runes) != 1 {
		return false
	}
	for index, current := range runes {
		if unicode.IsLetter(current) || index > 0 && (unicode.IsDigit(current) || current == '-') || short && unicode.IsDigit(current) {
			continue
		}
		return false
	}
	return true
}

func looksLikeOptionToken(token string) bool {
	return strings.HasPrefix(token, "-") && token != "-"
}

func isNegativeNumber(token string) bool {
	if len(token) < 2 || token[0] != '-' {
		return false
	}
	_, err := strconv.ParseFloat(token, 64)
	return err == nil
}

// ArgumentDefinition 描述一个位置参数。
type ArgumentDefinition struct {
	Name        string
	Description string
	Required    bool
}

// OptionDefinition 描述一个长选项及可选短别名。
type OptionDefinition struct {
	Name        string
	Short       string
	Description string
	Default     string
	Bool        bool
}
