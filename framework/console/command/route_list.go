package command

import (
	"fmt"
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

func (c *RouteList) Execute(input *console.Input, output *console.Output) {
	routes := c.App.Route.GetRoutes()
	
	output.Info(fmt.Sprintf("%-10s %-30s %s", "Method", "Path", "Handler"))
	output.Info("------------------------------------------------------------")
	
	for _, route := range routes {
		handler := "Closure"
		if h, ok := route.Handler.(string); ok {
			handler = h
		}
		output.Writeln(fmt.Sprintf("%-10s %-30s %s", route.Method, route.Path, handler))
	}
}
