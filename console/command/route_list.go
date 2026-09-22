package command

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// RouteList command
type RouteList struct {
	console.Command
}

func (c *RouteList) Configure() {
	c.Signature = "route:list"
	c.Description = "show route list."
	c.AddArgument("style", "the style of the table.", false)
	addApplicationSelectionOption(&c.Command)
	c.AddOption("sort", "s", "order by rule name.", "0")
	c.AddBoolOption("more", "m", "show route options.")
}

func (c *RouteList) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	applications, err := compiledApplicationsForCommand(c.App, input.GetOption("app"), false)
	if err != nil {
		return err
	}
	application := applications[0].application
	if err := application.LoadRoutes(); err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}
	router, err := resolveApplicationRoute(application)
	if err != nil {
		return fmt.Errorf("应用路由不可用: %w", err)
	}
	routes, err := router.Routes()
	if err != nil {
		return fmt.Errorf("failed to load routes: %w", err)
	}

	rows := make([]routeListRow, 0, len(routes))
	for _, registered := range routes {
		rows = append(rows, newRouteListRow(registered))
	}
	if err := sortRouteListRows(rows, input.GetOption("sort")); err != nil {
		return err
	}
	if input.GetOption("more") == "true" {
		output.Info(fmt.Sprintf("%-30s %-30s %-10s %-20s %-20s %-32s %s", "Rule", "Route", "Method", "Name", "Domain", "Option", "Pattern"))
		for _, row := range rows {
			output.Writeln(fmt.Sprintf("%-30s %-30s %-10s %-20s %-20s %-32s %s", row.rule, row.handler, row.method, row.name, row.domain, row.option, row.pattern))
		}
		return nil
	}
	output.Info(fmt.Sprintf("%-30s %-30s %-10s %s", "Rule", "Route", "Method", "Name"))
	for _, row := range rows {
		output.Writeln(fmt.Sprintf("%-30s %-30s %-10s %s", row.rule, row.handler, row.method, row.name))
	}
	return nil
}

type routeListRow struct {
	rule    string
	handler string
	method  string
	name    string
	domain  string
	option  string
	pattern string
}

func newRouteListRow(registered route.RouteInfo) routeListRow {
	handler := "<Closure>"
	if named, ok := registered.Handler.(string); ok {
		handler = named
	}
	options, _ := json.Marshal(map[string]interface{}{
		"auto":             registered.Auto,
		"extension":        registered.Extension,
		"middleware_count": registered.MiddlewareCount,
	})
	return routeListRow{
		rule:    registered.Path,
		handler: handler,
		method:  registered.Method,
		name:    registered.Name,
		domain:  registered.Domain,
		option:  string(options),
		pattern: "{}",
	}
}

func sortRouteListRows(rows []routeListRow, configured string) error {
	configured = strings.ToLower(strings.TrimSpace(configured))
	if configured == "" {
		configured = "0"
	}
	columns := map[string]int{"rule": 0, "route": 1, "method": 2, "name": 3, "domain": 4}
	column, exists := columns[configured]
	if !exists {
		parsed, err := strconv.Atoi(configured)
		if err != nil || parsed < 0 || parsed > 4 {
			return fmt.Errorf("route:list sort 必须是 rule、route、method、name、domain 或 0-4")
		}
		column = parsed
	}
	value := func(row routeListRow) string {
		switch column {
		case 0:
			return row.rule
		case 1:
			return row.handler
		case 2:
			return row.method
		case 3:
			return row.name
		default:
			return row.domain
		}
	}
	sort.SliceStable(rows, func(left, right int) bool {
		return strings.ToLower(value(rows[left])) < strings.ToLower(value(rows[right]))
	})
	return nil
}
