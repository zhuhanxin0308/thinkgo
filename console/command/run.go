package command

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
	"github.com/zhuhanxin0308/thinkgo/v3/http"
)

const (
	applicationServerHostEnvironment = "APP_SERVER_HOST"
	applicationServerPortEnvironment = "APP_SERVER_PORT"
	applicationPublicPathEnvironment = "APP_PUBLIC_PATH"
	defaultRunHost                   = "0.0.0.0"
	defaultRunPort                   = "8000"
)

// Run 启动 HTTP 服务或热重载进程。
type Run struct {
	console.Command
}

// runOperations 隔离进程环境、可执行文件、HTTP 内核和子进程边界，
// 使启动编排可以完整验证而不实际占用端口或运行外部程序。
type runOperations struct {
	lookupEnvironment func(string) (string, bool)
	setEnvironment    func(string, string) error
	unsetEnvironment  func(string) error
	lookupExecutable  func(string) (string, error)
	newHTTPKernel     func(*framework.App) (framework.Kernel, error)
	runApplication    func(*framework.App) error
	runAir            func(string, string) error
}

type runEnvironmentVariable struct {
	name  string
	value string
}

type runEnvironmentSnapshot struct {
	variable runEnvironmentVariable
	previous string
	existed  bool
}

// runEnvironmentTransaction 保存写入前的完整环境基线和已尝试写入数量。
// 已尝试项包含返回错误的当前项，因为底层写入可能已部分生效。
type runEnvironmentTransaction struct {
	operations runOperations
	snapshots  []runEnvironmentSnapshot
	attempted  int
}

// Configure 配置服务启动命令及端口选项。
func (c *Run) Configure() {
	c.Signature = "run"
	c.Description = "Go Development Server for ThinkGo"
	c.AddOption("host", "H", "The host to serve the application on", defaultRunHost)
	c.AddOption("port", "p", "The port to serve the application on", defaultRunPort)
	c.AddOption("root", "r", "The document root of the application", "")
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
	host := strings.TrimSpace(input.GetOption("host"))
	if host == "" {
		host = defaultRunHost
	}
	port := strings.TrimSpace(input.GetOption("port"))
	if port == "" {
		port = defaultRunPort
	}
	if !isValidRunHost(host) {
		return fmt.Errorf("invalid host %q", host)
	}
	if !isValidPort(port) {
		return fmt.Errorf("invalid port %q: expected an integer in 1-65535", port)
	}
	if startupErr := c.App.StartupError(); startupErr != nil {
		return fmt.Errorf("application startup failed: %w", startupErr)
	}
	root, err := resolveRunDocumentRoot(c.App, input.GetOption("root"))
	if err != nil {
		return err
	}
	if err := applyRunConfiguration(c.App, operations, host, port, root); err != nil {
		return err
	}
	writeRunStartupMessage(output, host, port, root)

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

func applyRunConfiguration(app *framework.App, operations runOperations, host, port, root string) error {
	transaction, err := applyRunEnvironment(operations, host, port, root)
	if err != nil {
		return err
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return joinRunEnvironmentRollback(fmt.Errorf("解析开发服务器端口失败: %w", err), transaction)
	}
	if err := app.ApplyProjectApplicationOverrides(map[string]interface{}{
		"server.host": host,
		"server.port": portNumber,
		"public_path": root,
	}); err != nil {
		return joinRunEnvironmentRollback(fmt.Errorf("设置开发服务器配置失败: %w", err), transaction)
	}
	return nil
}

func applyRunEnvironment(operations runOperations, host, port, root string) (*runEnvironmentTransaction, error) {
	variables := []struct {
		name  string
		value string
	}{
		{name: applicationServerHostEnvironment, value: host},
		{name: applicationPublicPathEnvironment, value: root},
		{name: applicationServerPortEnvironment, value: port},
	}
	transaction := &runEnvironmentTransaction{
		operations: operations,
		snapshots:  make([]runEnvironmentSnapshot, len(variables)),
	}
	// 必须在首次写入前捕获全部变量，避免中途失败时基线混入新值。
	for index, variable := range variables {
		previous, existed := operations.lookupEnvironment(variable.name)
		transaction.snapshots[index] = runEnvironmentSnapshot{
			variable: runEnvironmentVariable{name: variable.name, value: variable.value},
			previous: previous,
			existed:  existed,
		}
	}
	for index, snapshot := range transaction.snapshots {
		transaction.attempted = index + 1
		variable := snapshot.variable
		if err := operations.setEnvironment(variable.name, variable.value); err != nil {
			primaryErr := fmt.Errorf("set %s: %w", variable.name, err)
			return nil, joinRunEnvironmentRollback(primaryErr, transaction)
		}
	}
	return transaction, nil
}

// rollback 严格按照写入尝试的逆序恢复；单项失败只参与聚合，不能阻断其余项。
func (transaction *runEnvironmentTransaction) rollback() error {
	if transaction == nil {
		return nil
	}
	var rollbackErr error
	for index := transaction.attempted - 1; index >= 0; index-- {
		snapshot := transaction.snapshots[index]
		var err error
		if snapshot.existed {
			err = transaction.operations.setEnvironment(snapshot.variable.name, snapshot.previous)
		} else {
			err = transaction.operations.unsetEnvironment(snapshot.variable.name)
		}
		if err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("恢复 %s 失败: %w", snapshot.variable.name, err))
		}
	}
	return rollbackErr
}

func joinRunEnvironmentRollback(primaryErr error, transaction *runEnvironmentTransaction) error {
	rollbackErr := transaction.rollback()
	if rollbackErr == nil {
		return primaryErr
	}
	return errors.Join(primaryErr, fmt.Errorf("回滚 run 命令环境失败: %w", rollbackErr))
}

func resolveRunDocumentRoot(app *framework.App, configured string) (string, error) {
	if app == nil {
		return "", framework.ErrNilApplication
	}
	path := strings.TrimSpace(configured)
	if path == "" {
		path = app.ProjectPublicPath()
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(app.BasePath, path)
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("解析文档根目录失败: %w", err)
	}
	information, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("文档根目录不可用: %w", err)
	}
	if !information.IsDir() {
		return "", fmt.Errorf("文档根目录必须是目录: %s", absolute)
	}
	return absolute, nil
}

func writeRunStartupMessage(output *console.Output, host, port, root string) {
	output.Info(fmt.Sprintf("ThinkPHP Development server is started On <http://%s:%s/>", host, port))
	output.Info("You can exit with `CTRL-C`")
	output.Info("Document root is: " + root)
}

func isValidRunHost(host string) bool {
	if host == "" || strings.ContainsAny(host, ":/\\@?#\x00\r\n\t ") {
		return false
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return true
	}
	for _, label := range strings.Split(strings.ToLower(host), ".") {
		if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func defaultRunOperations() runOperations {
	return runOperations{
		lookupEnvironment: os.LookupEnv,
		setEnvironment:    os.Setenv,
		unsetEnvironment:  os.Unsetenv,
		lookupExecutable:  exec.LookPath,
		newHTTPKernel: func(app *framework.App) (framework.Kernel, error) {
			httpKernel, err := http.NewHttp(app)
			if err != nil {
				return nil, err
			}
			return http.NewServer(httpKernel), nil
		},
		runApplication: func(app *framework.App) error {
			return app.Run()
		},
		runAir: runAirProcess,
	}
}

func (operations runOperations) validate() error {
	return operations.validateBase()
}

func (operations runOperations) validateBase() error {
	if operations.lookupEnvironment == nil || operations.setEnvironment == nil || operations.unsetEnvironment == nil || operations.lookupExecutable == nil || operations.newHTTPKernel == nil || operations.runApplication == nil || operations.runAir == nil {
		return fmt.Errorf("run 命令操作依赖不完整")
	}
	return nil
}

var airExcludedDirectories = []string{
	"assets",
	"bin",
	"cmd",
	"docs",
	"framework",
	"public",
	"runtime",
	"testdata",
	"tmp",
	"vendor",
}

func airCommandArguments(binaryPath string) []string {
	return []string{
		"-c", os.DevNull,
		"--build.pre_cmd", "go run ./cmd/think service:discover",
		"--build.cmd", "go build -o " + binaryPath + " .",
		"--build.entrypoint", "./" + binaryPath,
		"--build.exclude_dir", strings.Join(airExcludedDirectories, ","),
	}
}

// runAirProcess 使用当前进程标准流运行 air，保留交互输入与实时构建日志。
func runAirProcess(path, basePath string) error {
	binaryPath := serverBinaryPath()
	// #nosec G204 -- path 已由 LookPath 解析，构建命令与输出路径均为框架固定值。
	cmd := exec.Command(path, airCommandArguments(binaryPath)...)
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

func serverBinaryPathForOS(goos string) string {
	if goos == "windows" {
		return "bin/server.exe"
	}
	return "bin/server"
}

// serverBinaryPath returns the platform-specific build artifact path.
func serverBinaryPath() string {
	return serverBinaryPathForOS(runtime.GOOS)
}

func ensureServerBinaryDir(basePath string) error {
	if strings.TrimSpace(basePath) == "" {
		return fmt.Errorf("应用根目录不能为空")
	}
	// #nosec G301 -- bin 仅保存本地开发构建产物，不包含凭据或运行时数据。
	return os.MkdirAll(filepath.Join(basePath, filepath.Dir(serverBinaryPath())), 0755)
}
