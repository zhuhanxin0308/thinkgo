package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeService command
type MakeService struct {
	console.Command
}

func (c *MakeService) Configure() {
	c.Signature = "make:service"
	c.Description = "Create a new service class"
}

func (c *MakeService) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Service name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/service", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Service %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package service

import (
	"thinkgo/framework"
)

// %s service
type %s struct {
	App *framework.App
}

func New%s(app *framework.App) *%s {
	return &%s{App: app}
}

func (s *%s) Register() {
	// Register service
}

func (s *%s) Boot() {
	// Boot service
}
`, name, name, name, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create service: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Service %s created successfully.", name))
}
