package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid validator name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "validate")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create validator directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

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
	validate.Validator
}

func New%s() *%s {
	v := &%s{}
	v.Rule = map[string]string{
		"name": "required|max:25",
	}
	v.Message = map[string]string{
		"name.required": "名称不能为空",
		"name.max":      "名称长度不能超过 25",
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
