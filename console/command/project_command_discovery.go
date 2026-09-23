package command

import (
	"context"
	"errors"
	"fmt"
	"go/format"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const projectCommandDiscoveryPath = "cmd/think/commandautoload/autoload_generated.go"

// ProjectCommandType 标识 Go 类型检查确认可以实例化的项目命令，不保存猜测的运行时签名。
type ProjectCommandType struct {
	ImportPath string
	TypeName   string
}

// DiscoverProjectCommands 从各应用的 command 包及其子包发现真实 ICommand 实现。
// 非项目目录返回空清单；项目源码或依赖损坏时明确失败，不读取旧生成结果兜底。
func DiscoverProjectCommands(basePath string) ([]ProjectCommandType, error) {
	return DiscoverProjectCommandsContext(context.Background(), basePath)
}

// DiscoverProjectCommandsContext 将调用方取消信号传递至源码选择和依赖类型检查。
func DiscoverProjectCommandsContext(ctx context.Context, basePath string) ([]ProjectCommandType, error) {
	if ctx == nil {
		return nil, fmt.Errorf("项目命令发现上下文不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Stat("go.mod"); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	modulePath, err := readApplicationModulePath(basePath)
	if err != nil {
		return nil, err
	}
	return discoverProjectCommandsWithContext(newApplicationSemanticContextWithContext(ctx, basePath, modulePath))
}

func discoverProjectCommandsWithContext(semantic *applicationSemanticContext) ([]ProjectCommandType, error) {
	root, err := os.OpenRoot(semantic.basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := readProjectCommandDirectory(root, "app")
	if err != nil {
		return nil, err
	}
	var result []ProjectCommandType
	for _, entry := range entries {
		if err := semantic.runContext.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Name() == "internal" || entry.Name() == "testdata" {
			continue
		}
		directory := filepath.Join("app", entry.Name(), "command")
		commands, err := inspectProjectCommandDirectory(root, semantic, directory)
		if err != nil {
			return nil, err
		}
		result = append(result, commands...)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].ImportPath != result[right].ImportPath {
			return result[left].ImportPath < result[right].ImportPath
		}
		return result[left].TypeName < result[right].TypeName
	})
	return result, nil
}

func readProjectCommandDirectory(root *os.Root, directory string) ([]os.DirEntry, error) {
	info, err := root.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("命令目录 %q 必须是项目内的真实目录", directory)
	}
	opened, err := root.Open(directory)
	if err != nil {
		return nil, err
	}
	entries, readErr := opened.ReadDir(-1)
	return entries, errors.Join(readErr, opened.Close())
}

func inspectProjectCommandDirectory(root *os.Root, semantic *applicationSemanticContext, directory string) ([]ProjectCommandType, error) {
	entries, err := readProjectCommandDirectory(root, directory)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	inspected, err := semantic.inspectApplicationPackage(directory, "")
	if err != nil {
		return nil, err
	}
	var result []ProjectCommandType
	if inspected.hasSource {
		commandType, err := semantic.frameworkType(frameworkImportPath+"/console", "ICommand")
		if err != nil {
			return nil, err
		}
		contract, ok := commandType.Underlying().(*types.Interface)
		if !ok {
			return nil, fmt.Errorf("console.ICommand 不是接口")
		}
		for _, name := range inspected.packageGo.Scope().Names() {
			object, ok := inspected.packageGo.Scope().Lookup(name).(*types.TypeName)
			if !ok || !object.Exported() {
				continue
			}
			current := types.Unalias(object.Type())
			if named, ok := current.(*types.Named); ok && named.TypeParams().Len() > 0 && named.TypeArgs().Len() == 0 {
				continue
			}
			if _, ok := current.Underlying().(*types.Struct); !ok || !types.Implements(types.NewPointer(current), contract) {
				continue
			}
			result = append(result, ProjectCommandType{ImportPath: semantic.packagePath(directory), TypeName: name})
		}
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("命令源码路径 %q 不能是符号链接", filepath.Join(directory, entry.Name()))
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "internal" {
			continue
		}
		nested, err := inspectProjectCommandDirectory(root, semantic, filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		result = append(result, nested...)
	}
	return result, nil
}

// ProjectCommandImportsAndValues 生成静态导入和类型字面量，供持久清单与临时命令宿主共用。
func ProjectCommandImportsAndValues(commands []ProjectCommandType) (string, string) {
	var imports, values strings.Builder
	aliases := make(map[string]string)
	for _, command := range commands {
		alias, exists := aliases[command.ImportPath]
		if !exists {
			alias = fmt.Sprintf("projectCommand%d", len(aliases))
			aliases[command.ImportPath] = alias
			fmt.Fprintf(&imports, "\t%s %q\n", alias, command.ImportPath)
		}
		fmt.Fprintf(&values, "\t\t&%s.%s{},\n", alias, command.TypeName)
	}
	return imports.String(), values.String()
}

func projectCommandDiscoverySource(semantic *applicationSemanticContext) (generatedApplicationSource, error) {
	commands, err := discoverProjectCommandsWithContext(semantic)
	if err != nil {
		return generatedApplicationSource{}, err
	}
	imports, values := ProjectCommandImportsAndValues(commands)
	source, err := format.Source([]byte("// 此文件由 service:discover 生成，按静态类型注册项目命令。\n" +
		"package commandautoload\n\nimport (\n\t\"" + frameworkImportPath + "/console\"\n" + imports +
		")\n\n// Commands 每次创建独立命令，避免多个控制台共享可变执行状态。\n" +
		"func Commands() []console.ICommand {\n\treturn []console.ICommand{\n" + values + "\t}\n}\n"))
	if err != nil {
		return generatedApplicationSource{}, fmt.Errorf("生成静态命令清单失败: %w", err)
	}
	return generatedApplicationSource{relativePath: filepath.FromSlash(projectCommandDiscoveryPath), source: source}, nil
}
