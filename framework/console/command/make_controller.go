package command

import (
	"fmt"
	"os"
	"path/filepath"
	"thinkgo/framework/console"
)

// MakeController command
type MakeController struct {
	console.Command
}

func (c *MakeController) Configure() {
	c.Signature = "make:controller"
	c.Description = "Create a new controller class"
}

func (c *MakeController) Execute(input *console.Input, output *console.Output) {
	name, err := normalizeGeneratorName(input.GetArgument(0), "Controller")
	if err != nil {
		output.Error("Invalid controller name: " + err.Error())
		return
	}

	dir := filepath.Join(c.App.BasePath, "app", "controller")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create controller directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Controller %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package controller

import (
	"thinkgo/framework"
)

func init() {
	framework.RegisterController("%s", &%s{})
}

type %s struct {
	framework.Controller
}

func (c *%s) Index() string {
	return "Hello %s"
}
`, name, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create controller: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Controller %s created successfully.", name))
}
