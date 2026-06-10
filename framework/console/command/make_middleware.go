package command

import (
	"fmt"
	"os"
	"strings"
	"thinkgo/framework/console"
)

// MakeMiddleware command
type MakeMiddleware struct {
	console.Command
}

func (c *MakeMiddleware) Configure() {
	c.Signature = "make:middleware"
	c.Description = "Create a new middleware class"
}

func (c *MakeMiddleware) Execute(input *console.Input, output *console.Output) {
	name := input.GetArgument(0)
	if name == "" {
		output.Error("Middleware name is required.")
		return
	}

	// Capitalize
	name = strings.Title(name)

	// Ensure directory exists
	dir := fmt.Sprintf("%s/app/middleware", c.App.BasePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		os.MkdirAll(dir, 0755)
	}

	// File path
	filename := fmt.Sprintf("%s/%s.go", dir, strings.ToLower(name))
	
	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Middleware %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package middleware

import (
	"thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

// %s middleware
type %s struct{}

func (m *%s) Handle(req *context.Request, next middleware.Next) *context.Response {
	// Before request
	
	resp := next(req)
	
	// After request
	
	return resp
}
`, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create middleware: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Middleware %s created successfully.", name))
}
