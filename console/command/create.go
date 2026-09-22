package command

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

const scaffoldFrameworkModule = "github.com/zhuhanxin0308/thinkgo/v3"

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Create 在当前目录的空子目录内创建完整、可编译的独立项目。
type Create struct{ console.Command }

// Configure 声明必填的项目目录名称。
func (command *Create) Configure() {
	command.Signature = "create"
	command.Description = "Create a complete project in a new or empty directory"
	command.AddArgument("name", "Project directory name", true)
}

// Execute 完成依赖解析和业务清单生成后才交付项目目录。
func (command *Create) Execute(input *console.Input, output *console.Output) error {
	if input == nil {
		return console.ErrInvalidInput
	}
	if output == nil {
		return console.ErrInvalidOutput
	}
	if command == nil || command.App == nil {
		return framework.ErrNilApplication
	}
	prepare := func(ctx context.Context, directory, module string) error {
		return prepareCreatedProject(ctx, directory, module, output)
	}
	name := input.GetArgument(0)
	if err := createProject(input.Context(), command.App.BasePath, name, prepare); err != nil {
		return err
	}
	output.Success("项目已创建: " + filepath.Join(command.App.BasePath, name))
	output.Info("进入项目目录后可运行 thinkgo list、thinkgo build 或 go test ./...")
	return output.Err()
}

type projectPreparer func(context.Context, string, string) error

func createProject(ctx context.Context, base, name string, prepare projectPreparer) (returnErr error) {
	if err := validateProjectName(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := projectTargetEmpty(root, name); err != nil {
		return err
	}
	stageName := ".thinkgo-create-" + rand.Text()
	if err := root.Mkdir(stageName, 0755); err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.RemoveAll(stageName)) }()
	sources, err := projectSources(name)
	if err != nil {
		return err
	}
	for path, content := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(stageName, path)
		if err := root.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := root.WriteFile(target, content, 0644); err != nil {
			return err
		}
	}
	for _, directory := range []string{"runtime", "app/index/config", "app/index/lang", "app/index/model", "app/index/validate", "app/index/middleware", "app/index/service"} {
		if err := root.MkdirAll(filepath.Join(stageName, directory), 0755); err != nil {
			return err
		}
	}
	if err := prepare(ctx, filepath.Join(base, stageName), name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	exists, err := projectTargetEmpty(root, name)
	if err != nil {
		return err
	}
	if exists {
		// Remove 只能移除仍为空的目录，不会递归删除并发写入的用户文件。
		if err := root.Remove(name); err != nil {
			return fmt.Errorf("项目目录已发生变化，拒绝覆盖: %w", err)
		}
	}
	if err := root.Rename(stageName, name); err != nil {
		if exists {
			return errors.Join(err, root.Mkdir(name, 0755))
		}
		return err
	}
	return nil
}

func projectTargetEmpty(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return true, fmt.Errorf("目标 %q 必须是普通空目录", name)
	}
	entries, err := fs.ReadDir(root.FS(), name)
	if err != nil {
		return true, err
	}
	if len(entries) != 0 {
		return true, fmt.Errorf("目标目录 %q 非空，不能创建项目骨架", name)
	}
	return true, nil
}

func validateProjectName(name string) error {
	if !projectNamePattern.MatchString(name) {
		return fmt.Errorf("项目名必须以字母或数字开头，且仅包含字母、数字、下划线或短横线")
	}
	upper := strings.ToUpper(name)
	if upper == "CON" || upper == "PRN" || upper == "AUX" || upper == "NUL" ||
		len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) && upper[3] >= '1' && upper[3] <= '9' {
		return fmt.Errorf("项目名 %q 是操作系统保留名称", name)
	}
	return nil
}

func prepareCreatedProject(ctx context.Context, directory, module string, output *console.Output) error {
	writer := buildOutputWriter{output: output}
	environment := buildTarget{os: runtime.GOOS, arch: runtime.GOARCH}.environment()
	if err := runBuildProcess(ctx, directory, environment, writer, "go", "get", scaffoldFrameworkModule+"@"+scaffoldFrameworkVersion()); err != nil {
		return fmt.Errorf("获取框架依赖失败: %w", err)
	}
	application := framework.NewConsoleAppUninitialized(directory)
	defer application.Close()
	if err := RefreshControllerDiscovery(application); err != nil {
		return err
	}
	if err := runBuildProcess(ctx, directory, environment, writer, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("解析项目依赖失败: %w", err)
	}
	return nil
}

func scaffoldFrameworkVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		modules := append([]*debug.Module{&info.Main}, info.Deps...)
		for _, module := range modules {
			if module.Path == scaffoldFrameworkModule && module.Replace == nil && strings.HasPrefix(module.Version, "v") {
				return module.Version
			}
		}
	}
	// 本地开发入口没有发布版本，交由 Go 解析最新公开版本，不能伪造不存在的版本号。
	return "latest"
}
