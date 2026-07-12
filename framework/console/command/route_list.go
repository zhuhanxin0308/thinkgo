package command

import (
	"fmt"

	"thinkgo/framework"
	"thinkgo/framework/console"
)

// RouteList command
type RouteList struct {
	console.Command
}

func (c *RouteList) Configure() {
	c.Signature = "route:list"
	c.Description = "List all registered routes"
}

func (c *RouteList) Execute(_ *console.Input, output *console.Output) error {
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	if c.App.Route == nil {
		return fmt.Errorf("应用路由不可用")
	}
	routes, err := c.App.Route.Routes()
	if err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	output.Info(fmt.Sprintf("%-10s %-30s %s", "Method", "Path", "Handler"))
	output.Info("------------------------------------------------------------")

	for _, registered := range routes {
		handler := "Closure"
		if h, ok := registered.Handler.(string); ok {
			handler = h
		}
		output.Writeln(fmt.Sprintf("%-10s %-30s %s", registered.Method, registered.Path, handler))
	}
	return nil
}
