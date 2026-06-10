package console

import (
	"flag"
	"os"
)

// Input handles console input
type Input struct {
	Args      []string
	Options   map[string]string
	Arguments map[string]string
}

// NewInput creates a new Input
func NewInput() *Input {
	return &Input{
		Args:      os.Args[1:],
		Options:   make(map[string]string),
		Arguments: make(map[string]string),
	}
}

// Parse parses arguments and options
// This is a simplified parser. In a real app, we might use pflag or similar.
func (i *Input) Parse(definitions []ArgumentDefinition, optionDefinitions []OptionDefinition) {
	// Reset flags
	flagSet := flag.NewFlagSet("think", flag.ContinueOnError)
	
	// Register options
	for _, opt := range optionDefinitions {
		flagSet.String(opt.Name, opt.Default, opt.Description)
	}
	
	// Parse
	// We need to handle the command name which is usually the first arg
	// But Input is initialized with os.Args[1:], so the first element is the command name.
	// However, if we are running `go run main.go command arg --opt val`, 
	// os.Args[1] is "command".
	
	// Let's assume the caller (Console) handles the command name dispatch
	// and passes the remaining args to the command's input.
	// But here we are just defining the Input struct.
	
	// For now, let's just provide helper methods to access args/opts
}

// GetArgument gets an argument by index or name
func (i *Input) GetArgument(index int) string {
	if index < len(i.Args) {
		return i.Args[index]
	}
	return ""
}

// GetOption gets an option value
func (i *Input) GetOption(name string) string {
	// Simple parsing for --name=value or --name value or -name value
	prefixes := []string{"--", "-"}
	
	for _, prefix := range prefixes {
		for idx, arg := range i.Args {
			if arg == prefix+name {
				if idx+1 < len(i.Args) {
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
}
