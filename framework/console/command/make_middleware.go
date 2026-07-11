package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid middleware name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "middleware")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create middleware directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

	// Check if exists
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		output.Error(fmt.Sprintf("Middleware %s already exists.", name))
		return
	}

	// Content
	content := fmt.Sprintf(`package middleware

import (
	"net/http"
	"thinkgo/framework/context"
	"thinkgo/framework/middleware"
)

// %s 中间件。
type %s struct{}

// Handle 处理请求链路，并在 next 缺失时返回明确错误响应。
func (m *%s) Handle(req *context.Request, next middleware.Next) *context.Response {
	if next == nil {
		return context.NewResponse().Code(http.StatusInternalServerError).Content(http.StatusText(http.StatusInternalServerError))
	}
	return next(req)
}
`, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create middleware: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Middleware %s created successfully.", name))
}
