package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeValidate command
type MakeValidate struct {
	console.Command
}

func (c *MakeValidate) Configure() {
	c.Signature = "make:validate"
	c.Description = "Create a new validator class"
}

func (c *MakeValidate) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Validator name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/validate", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Validator %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package validate

import (
	"thinkgo/framework/validate"
)

// %s validator
type %s struct {
	validate.Validate
}

func New%s() *%s {
	v := &%s{}
	v.Rule = map[string]string{
		"name": "require|max:25",
	}
	v.Message = map[string]string{
		"name.require": "Name is required",
		"name.max":     "Name max length is 25",
	}
	return v
}
`, name, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create validator: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Validator %s created successfully.", name))
}
