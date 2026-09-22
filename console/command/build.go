package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// SupportedBuildTargets 是经过框架验证的无 CGO 发布目标。
const SupportedBuildTargets = "linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 darwin/amd64 darwin/arm64"

type buildTarget struct {
	os, arch, arm string
}

// Build 编译项目并打包独立运行所需的资源。
type Build struct{ console.Command }

// Configure 声明发布平台及 ARM 指令集选项。
func (command *Build) Configure() {
	command.Signature = "build"
	command.Description = "Build distribution packages: " + SupportedBuildTargets
	command.AddArgument("target", "GOOS/GOARCH; defaults to the current platform", false)
	command.AddOption("goarm", "", "ARM version: 5, 6 or 7 (default 7)", "")
}

// Execute 仅使用项目源码，不启动业务服务或数据库。
func (command *Build) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	target, err := parseBuildTarget(input.GetArgument(0), input.GetOption("goarm"))
	if err != nil {
		return err
	}
	compiler := func(ctx context.Context, base, executable string, target buildTarget) error {
		return compileDistribution(ctx, base, executable, target, output)
	}
	if err := buildDistribution(input.Context(), command.App.BasePath, target, compiler); err != nil {
		return err
	}
	output.Success("发布包已生成: " + filepath.Join(command.App.BasePath, "dist", target.directory()))
	return output.Err()
}

func parseBuildTarget(value, arm string) (buildTarget, error) {
	if value == "" {
		value = runtime.GOOS + "/" + runtime.GOARCH
	}
	value = strings.ToLower(value)
	valid := false
	for _, candidate := range strings.Fields(SupportedBuildTargets) {
		valid = valid || value == candidate
	}
	if !valid {
		return buildTarget{}, fmt.Errorf("不支持构建目标 %q，可选: %s", value, SupportedBuildTargets)
	}
	osName, architecture, _ := strings.Cut(value, "/")
	if architecture == "arm" {
		if arm == "" {
			arm = "7"
		}
		if arm != "5" && arm != "6" && arm != "7" {
			return buildTarget{}, fmt.Errorf("GOARM 只能为 5、6 或 7")
		}
	} else if arm != "" {
		return buildTarget{}, fmt.Errorf("只有 ARM 目标支持 --goarm")
	}
	return buildTarget{os: osName, arch: architecture, arm: arm}, nil
}

func (target buildTarget) directory() string {
	name := target.os + "-" + target.arch
	if target.arm != "" {
		name += "v" + target.arm
	}
	return name
}

func (target buildTarget) binary() string {
	if target.os == "windows" {
		return "thinkgo-nocgo.exe"
	}
	return "thinkgo-nocgo"
}

func (target buildTarget) environment() []string {
	values := map[string]string{"GOOS": target.os, "GOARCH": target.arch, "GOARM": target.arm, "CGO_ENABLED": "0", "GOWORK": "off"}
	environment := make([]string, 0, len(os.Environ())+len(values))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := values[strings.ToUpper(key)]; !replaced {
			environment = append(environment, item)
		}
	}
	for _, key := range []string{"GOOS", "GOARCH", "GOARM", "CGO_ENABLED", "GOWORK"} {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

type buildOutputWriter struct{ output *console.Output }

func (writer buildOutputWriter) Write(content []byte) (int, error) {
	writer.output.Write(string(content))
	return len(content), writer.output.Err()
}

func compileDistribution(ctx context.Context, base, executable string, target buildTarget, output *console.Output) (returnErr error) {
	directory, err := os.MkdirTemp("", "thinkgo-build-*")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(directory)) }()
	host := buildTarget{os: runtime.GOOS, arch: runtime.GOARCH}
	generator := filepath.Join(directory, host.binary())
	writer := buildOutputWriter{output: output}
	// 生成器始终按宿主编译，然后在目标环境中扫描带平台约束的业务源码。
	if err := runBuildProcess(ctx, base, host.environment(), writer, "go", "build", "-mod=readonly", "-o", generator, "./cmd/think"); err != nil {
		return fmt.Errorf("编译源码生成器失败: %w", err)
	}
	if err := runBuildProcess(ctx, base, target.environment(), writer, generator, "service:discover"); err != nil {
		return fmt.Errorf("刷新目标平台业务清单失败: %w", err)
	}
	if err := runBuildProcess(ctx, base, target.environment(), writer, "go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", executable, "."); err != nil {
		return fmt.Errorf("编译 %s 失败: %w", target.directory(), err)
	}
	return nil
}

func runBuildProcess(ctx context.Context, base string, environment []string, output io.Writer, executable string, args ...string) error {
	// #nosec G204 G702 -- 内部调用只执行 Go 工具或本次独占生成器，使用参数数组且不经过 shell。
	process := exec.CommandContext(ctx, executable, args...)
	process.Dir, process.Env, process.Stdout, process.Stderr = base, environment, output, output
	return errors.Join(process.Run(), ctx.Err())
}
