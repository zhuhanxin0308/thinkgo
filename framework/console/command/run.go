package command

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
	c.AddOption("port", "p", "HTTP port to listen on (overrides config/.env)", "")
}

func (c *Run) Execute(input *console.Input, output *console.Output) {
	port := input.GetOption("p")
	if port == "" {
		port = input.GetOption("port")
	}

	if port != "" {
		if !isValidPort(port) {
			output.Error("Invalid port: " + port + " (expected an integer in 1-65535)")
			return
		}

		// 注入到进程环境，使热重载模式下重新构建的子进程也能读到该端口
		// （env.Get 已让真实环境变量优先于 .env 文件，因此此处可生效）。
		os.Setenv("SERVER_PORT", port)
		output.Info("Starting server on port " + port)

		// 同时更新当前进程的内存配置，供普通模式（不经子进程）直接生效。
		// 用点路径写入，保留 host/tls 等同级配置。
		c.App.Config.Set("app.server.port", port)
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

		// 普通模式直接在本进程驱动内核，需补齐 App.Run 里完成、而本路径此前绕过的步骤：
		// 1) 暴露初始化错误（此前被静默吞掉）；开发用 run 保持宽容，仅告警不中止，
		//    以便数据库未就绪时仍能调试非数据库路由；
		// 2) 启动服务提供者（BootProviders）。
		if startErr := c.App.StartupError(); startErr != nil {
			output.Warning("Startup warning: " + startErr.Error())
		}
		c.App.BootProviders()

		kernel := http.NewHttp(c.App)
		if err := kernel.Run(); err != nil {
			output.Error("Server error: " + err.Error())
		}
		return
	}

	// Run with air（热重载模式：构建出的二进制是独立进程，端口经环境变量传递）
	buildCmd := "go build -o " + serverBinaryPath() + " main.go"
	cmd := exec.Command(path,
		"--build.cmd", buildCmd,
		"--build.bin", "./"+serverBinaryPath(),
	)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		output.Error("Air exited with error: " + err.Error())
	}
}

// isValidPort 校验端口为 1-65535 范围内的整数。
func isValidPort(port string) bool {
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535
}

// serverBinaryPath 返回构建产物路径，按平台选择是否带 .exe 后缀。
func serverBinaryPath() string {
	if runtime.GOOS == "windows" {
		return "bin/server.exe"
	}
	return "bin/server"
}
