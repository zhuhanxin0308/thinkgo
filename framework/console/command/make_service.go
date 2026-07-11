package command

import (
	"fmt"
	"os"
	"path/filepath"
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
	name, err := normalizeGeneratorName(input.GetArgument(0), "")
	if err != nil {
		output.Error("Invalid service name: " + err.Error())
		return
	}

	// Ensure directory exists
	dir := filepath.Join(c.App.BasePath, "app", "service")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			output.Error(fmt.Sprintf("Failed to create service directory: %v", err))
			return
		}
	}

	// File path
	filename := filepath.Join(dir, lowerGoFilename(name))

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

// %s 服务。
type %s struct {
	App *framework.App
}

// New%s 创建服务实例。
func New%s(app *framework.App) *%s {
	return &%s{App: app}
}

// Register 将服务实例注册到容器，供控制器、命令和其它服务复用。
func (s *%s) Register(app *framework.App) {
	if app == nil {
		return
	}
	s.App = app
	app.Instance("service.%s", s)
}

// Boot 确认服务持有当前应用实例。
func (s *%s) Boot(app *framework.App) {
	if app != nil {
		s.App = app
	}
}
`, name, name, name, name, name, name, name, name, name)

	// Write file
	if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
		output.Error(fmt.Sprintf("Failed to create service: %v", err))
		return
	}

	output.Success(fmt.Sprintf("Service %s created successfully.", name))
}
