package command

import (
	"os"
	"os/exec"
	"thinkgo/framework/console"
	"thinkgo/framework/http"
)

// Run command
type Run struct {
	console.Command
}

func (c *Run) Configure() {
	c.Signature = "run"
	c.Description = "Start the HTTP server"
}

func (c *Run) Execute(input *console.Input, output *console.Output) {
	port := input.GetOption("p")
	if port == "" {
		port = input.GetOption("port")
	}

	if port != "" {
		os.Setenv("SERVER_PORT", port)
		output.Info("Starting server on port " + port)
		
		// Update config for current process (normal mode)
		if serverConfig, ok := c.App.Config.Get("app.server").(map[string]interface{}); ok {
			serverConfig["port"] = port
			c.App.Config.Set("app.server", serverConfig)
		} else {
			// If not exists, create it
			serverConfig = make(map[string]interface{})
			serverConfig["port"] = port
			c.App.Config.Set("app.server", serverConfig)
		}
	} else {
		output.Info("Starting server using configuration...")
	}

	// Check if air is installed
	path, err := exec.LookPath("air")
	if err != nil {
		output.Warning("Air (Hot Reload) not found.")
		output.Info("To enable hot reload, please install air:")
		output.Info("go install github.com/air-verse/air@latest")
		output.Info("Starting server in normal mode...")
		
		kernel := http.NewHttp(c.App)
		if err := kernel.Run(); err != nil {
			output.Error("Server error: " + err.Error())
		}
		return
	}

	// Run with air
	cmd := exec.Command(path, 
		"--build.cmd", "go build -o bin/server.exe main.go",
		"--build.bin", "./bin/server.exe",
	)
	
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	if err := cmd.Run(); err != nil {
		output.Error("Air exited with error: " + err.Error())
	}
}
