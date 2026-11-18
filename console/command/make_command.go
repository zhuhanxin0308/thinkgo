package command

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// MakeCommand 创建可以注册到应用控制台的命令类型。
type MakeCommand struct {
	console.Command
}

func (c *MakeCommand) Configure() {
	c.Signature = "make:command"
	c.Description = "Create a new console command class"
	c.AddArgument("name", "Command type name", true)
	c.AddArgument("commandName", "The name of the command", false)
	addApplicationSelectionOption(&c.Command)
}

func (c *MakeCommand) Execute(input *console.Input, output *console.Output) error {
	target, err := normalizedGeneratorTarget(&c.Command, input, output, "")
	if err != nil {
		return fmt.Errorf("invalid command name: %w", err)
	}
	name := target.name
	signature := input.GetArgument(1)
	if signature == "" {
		signature = strings.ToLower(name)
	}
	if err := console.ValidateCommandName(signature); err != nil {
		return err
	}
	if err := validateGeneratedCommandSignature(input.Context(), c.App.BasePath, name, signature); err != nil {
		return err
	}

	// 命令签名独立于 Go 类型名，并使用 Go 字符串字面量编码。
	content := fmt.Sprintf(`package command

import (
	"github.com/zhuhanxin0308/thinkgo/v3/console"
)

// %s 是应用自定义命令。
type %s struct {
	console.Command
}

func (c *%s) Configure() {
	c.Signature = %q
	c.Description = "应用命令 %s"
}

func (c *%s) Execute(input *console.Input, output *console.Output) error {
	output.Info(c.GetSignature() + " " + c.GetDescription())
	return nil
}
`, name, name, name, signature, name, name)

	if err := writeAndRefreshGeneratedApplicationSource(c.App, target, "command", lowerGoFilename(name), []byte(content)); err != nil {
		return fmt.Errorf("create command %s: %w", name, err)
	}

	output.Success(fmt.Sprintf("Command %s created successfully.", name))
	return nil
}

// BuiltinCommands 返回框架全部内置 CLI 命令实例，供宿主注册与生成器防冲突检查共用。
func BuiltinCommands(cli *console.Console) []console.ICommand {
	return []console.ICommand{
		&Version{},
		&Help{Console: cli},
		&List{Console: cli},
		&Build{},
		&Create{},
		&MakeApp{},
		&Clear{},
		&Run{},
		&MakeController{},
		&MakeModel{},
		&MakeCRUD{},
		&OpenAPIGenerate{},
		&MakeCommand{},
		&MakeValidate{},
		&MakeMiddleware{},
		&MakeEvent{},
		&MakeListener{},
		&MakeSubscribe{},
		&MakeService{},
		&Optimize{},
		&OptimizeConfig{},
		&OptimizeRoute{},
		&OptimizeSchema{},
		&RouteExport{},
		&SchemaValidate{},
		&RouteList{},
		&ServiceDiscover{},
		&VendorPublish{},
		&ConfigDump{},
		&Migrate{},
		&MigrateRollback{},
		&MigrateStatus{},
		&DeployCheck{},
	}
}

func validateGeneratedCommandSignature(ctx context.Context, basePath, typeName, signature string) error {
	for _, builtin := range BuiltinCommands(nil) {
		builtin.Configure()
		if builtin.GetSignature() == signature {
			return fmt.Errorf("create command %s: %w: %q 与内置命令冲突", typeName, console.ErrDuplicateCommand, signature)
		}
	}
	existing, err := existingProjectCommandSignatures(ctx, basePath)
	if err != nil {
		return err
	}
	if declaredIn, duplicated := existing[signature]; duplicated {
		return fmt.Errorf("create command %s: %w: %q 已在 %s 声明", typeName, console.ErrDuplicateCommand, signature, declaredIn)
	}
	return nil
}

func existingProjectCommandSignatures(ctx context.Context, basePath string) (map[string]string, error) {
	signatures := make(map[string]string)
	if strings.TrimSpace(basePath) == "" {
		return signatures, nil
	}
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	appEntries, err := readProjectCommandDirectory(root, "app")
	if err != nil || len(appEntries) == 0 {
		return signatures, err
	}
	var semantic *applicationSemanticContext
	if _, modErr := root.Lstat("go.mod"); modErr == nil {
		semantic, err = newApplicationSemanticContextFromModule(basePath)
		if err != nil {
			return nil, err
		}
		semantic.runContext = ctx
	}
	for _, entry := range appEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("应用目录 %q 不能是符号链接", entry.Name())
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "internal" || entry.Name() == "testdata" {
			continue
		}
		if err := collectCommandSignaturesInDirectory(ctx, root, semantic, filepath.Join("app", entry.Name(), "command"), signatures); err != nil {
			return nil, err
		}
	}
	return signatures, nil
}

func collectCommandSignaturesInDirectory(ctx context.Context, root *os.Root, semantic *applicationSemanticContext, directory string, signatures map[string]string) error {
	entries, err := readProjectCommandDirectory(root, directory)
	if err != nil || len(entries) == 0 {
		return err
	}
	files, err := parseProjectCommandASTFiles(root, semantic, directory, entries)
	if err != nil {
		return err
	}
	for path, file := range files {
		for _, signature := range extractConfiguredCommandSignatures(file) {
			if _, exists := signatures[signature]; !exists {
				signatures[signature] = path
			}
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("命令源码路径 %q 不能是符号链接", filepath.Join(directory, entry.Name()))
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "testdata" || entry.Name() == "internal" {
			continue
		}
		if err := collectCommandSignaturesInDirectory(ctx, root, semantic, filepath.Join(directory, entry.Name()), signatures); err != nil {
			return err
		}
	}
	return nil
}

func parseProjectCommandASTFiles(root *os.Root, semantic *applicationSemanticContext, directory string, entries []os.DirEntry) (map[string]*ast.File, error) {
	result := make(map[string]*ast.File)
	if semantic != nil {
		parsedFiles, hasSource, err := semantic.parseApplicationFiles(directory, "")
		if err != nil || !hasSource {
			return result, err
		}
		for _, file := range parsedFiles {
			position := semantic.fileSet.Position(file.Pos())
			result[filepath.ToSlash(position.Filename)] = file
		}
		return result, nil
	}
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == applicationDiscoveryFilename {
			continue
		}
		relativePath := filepath.Join(directory, name)
		source, err := root.ReadFile(relativePath)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fileSet, filepath.ToSlash(relativePath), source, parser.SkipObjectResolution)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		result[filepath.ToSlash(relativePath)] = file
	}
	return result, nil
}

func extractConfiguredCommandSignatures(file *ast.File) []string {
	var found []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || fn.Name == nil || fn.Name.Name != "Configure" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for index, lhs := range assign.Lhs {
				selector, ok := lhs.(*ast.SelectorExpr)
				if !ok || selector.Sel == nil || selector.Sel.Name != "Signature" || index >= len(assign.Rhs) {
					continue
				}
				literal, ok := assign.Rhs[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				if unquoted, err := strconv.Unquote(literal.Value); err == nil && unquoted != "" {
					found = append(found, unquoted)
				}
			}
			return true
		})
	}
	return found
}
