package command

import (
	"fmt"

	"thinkgo/framework/console"
)

// MakeMiddleware command
type MakeMiddleware struct {
	console.Command
}

func (c *MakeMiddleware) Configure() {
	c.Signature = "make:middleware"
	c.Description = "Create a new middleware class"
	c.AddArgument("name", "Middleware type name", true)
	configureApplicationOption(&c.Command)
}

func (c *MakeMiddleware) Execute(input *console.Input, output *console.Output) error {
	name, err := normalizedGeneratorInput(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid middleware name: %w", err)
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

	if err := writeAndRegisterGeneratedAppSource(c.App, "middleware", lowerGoFilename(name), []byte(content), applicationRegistrationMiddleware, name); err != nil {
		return fmt.Errorf("create and register middleware %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Middleware %s created successfully.", name))
	return nil
}
