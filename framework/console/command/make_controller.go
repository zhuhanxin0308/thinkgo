package command

import (
	"fmt"
	"os"
	"strings"
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
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Controller name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)
	if !strings.HasSuffix(name, "Controller") {
		name += "Controller"
	}

	// File path
	filename := fmt.Sprintf("%s/app/controller/%s.go", c.App.BasePath, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Controller %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package controller

import (
	"thinkgo/framework"
	"thinkgo/framework/context"
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
