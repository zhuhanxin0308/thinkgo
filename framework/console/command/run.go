package command

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"thinkgo/framework"
	"thinkgo/framework/console"
	"thinkgo/framework/http"
)

// Run 启动 HTTP 服务或热重载进程。
type Run struct {
	console.Command
}

// runOperations 隔离进程环境、可执行文件、HTTP 内核和子进程边界，
// 使启动编排可以完整验证而不实际占用端口或运行外部程序。
type runOperations struct {
	setEnvironment   func(string, string) error
	lookupExecutable func(string) (string, error)
	newHTTPKernel    func(*framework.App) (framework.Kernel, error)
	runApplication   func(*framework.App) error
	runAir           func(string, string) error
}

// Configure 配置服务启动命令及端口选项。
func (c *Run) Configure() {
	c.Signature = "run"
	c.Description = "Start the HTTP server"
	c.AddOption("port", "p", "HTTP port to listen on (overrides config/.env)", "")
}

// Execute 校验端口并选择普通启动或热重载模式。
func (c *Run) Execute(input *console.Input, output *console.Output) error {
	return c.executeWithOperations(input, output, defaultRunOperations())
}

func (c *Run) executeWithOperations(input *console.Input, output *console.Output, operations runOperations) error {
	if input == nil {
		return fmt.Errorf("命令输入不能为空")
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if c.App == nil {
		return framework.ErrNilApplication
	}
	if err := operations.validate(); err != nil {
		return err
	}
	port := input.GetOption("port")
	if port != "" && !isValidPort(port) {
		return fmt.Errorf("invalid port %q: expected an integer in 1-65535", port)
	}
	if c.App.Config == nil {
		return fmt.Errorf("应用配置不可用")
	}
	if startupErr := c.App.StartupError(); startupErr != nil {
		return fmt.Errorf("application startup failed: %w", startupErr)
	}

	if port != "" {
		// 注入到进程环境，使热重载模式下重新构建的子进程也能读到该端口
		// （env.Get 已让真实环境变量优先于 .env 文件，因此此处可生效）。
		if err := operations.setEnvironment("SERVER_PORT", port); err != nil {
			return fmt.Errorf("set SERVER_PORT: %w", err)
		}
		output.Info("Starting server on port " + port)

		// 同时更新当前进程的内存配置，供普通模式（不经子进程）直接生效。
		// 用点路径写入，保留 host/tls 等同级配置。
		c.App.Config.Set("app.server.port", port)
	} else {
		output.Info("Starting server using configuration...")
	}

	// 检查 air 是否可用。
	path, err := operations.lookupExecutable("air")
	if err != nil {
		output.Warning("Air (Hot Reload) not found.")
		output.Info("To enable hot reload, please install air:")
		output.Info("go install github.com/air-verse/air@latest")
		output.Info("Starting server in normal mode...")

		// 统一交给 App.Run 执行启动校验、Provider 生命周期和资源关闭。
		kernel, kernelErr := operations.newHTTPKernel(c.App)
		if kernelErr != nil {
			return fmt.Errorf("HTTP configuration error: %w", kernelErr)
		}
		c.App.Kernel = kernel
		if err := operations.runApplication(c.App); err != nil {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}

	// 使用 air 热重载；构建出的二进制是独立进程，端口经环境变量传递。
	if err := ensureServerBinaryDir(c.App.BasePath); err != nil {
		return fmt.Errorf("failed to prepare server binary directory: %w", err)
	}
	if err := operations.runAir(path, c.App.BasePath); err != nil {
		return fmt.Errorf("air exited with error: %w", err)
	}
	return nil
}

func defaultRunOperations() runOperations {
	return runOperations{
		setEnvironment:   os.Setenv,
		lookupExecutable: exec.LookPath,
		newHTTPKernel: func(app *framework.App) (framework.Kernel, error) {
			return http.NewHttp(app)
		},
		runApplication: func(app *framework.App) error {
			return app.Run()
		},
		runAir: runAirProcess,
	}
}

func (operations runOperations) validate() error {
	if operations.setEnvironment == nil || operations.lookupExecutable == nil ||
		operations.newHTTPKernel == nil || operations.runApplication == nil || operations.runAir == nil {
		return fmt.Errorf("run 命令操作依赖不完整")
	}
	return nil
}

// runAirProcess 使用当前进程标准流运行 air，保留交互输入与实时构建日志。
func runAirProcess(path, basePath string) error {
	buildCmd := "go build -o " + serverBinaryPath() + " main.go"
	cmd := exec.Command(path,
		"--build.cmd", buildCmd,
		"--build.bin", "./"+serverBinaryPath(),
	)
	cmd.Dir = basePath
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func ensureServerBinaryDir(basePath string) error {
	if strings.TrimSpace(basePath) == "" {
		return fmt.Errorf("应用根目录不能为空")
	}
	return os.MkdirAll(filepath.Join(basePath, filepath.Dir(serverBinaryPath())), 0755)
}
