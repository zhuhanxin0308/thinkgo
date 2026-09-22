package command

import (
	"fmt"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
)

// MakeMiddleware command
type MakeMiddleware struct {
	console.Command
}

func (c *MakeMiddleware) Configure() {
	c.Signature = "make:middleware"
	c.Description = "Create a new middleware class"
	c.AddArgument("name", "Middleware type name", true)
	addApplicationSelectionOption(&c.Command)
}

func (c *MakeMiddleware) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid middleware name: %w", err)
	}
	name := target.name

	// Content
	content := fmt.Sprintf(`package middleware

import (
	"net/http"
	"github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/middleware"
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

	if err := writeGeneratedAppSource(c.App, target, "middleware", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create middleware %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Middleware %s created successfully.", name))
	return nil
}
