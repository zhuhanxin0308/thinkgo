package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	frameworkImportPath           = "github.com/zhuhanxin0308/thinkgo/framework"
	frameworkMiddlewareImportPath = frameworkImportPath + "/middleware"
)

type applicationSemanticContext struct {
	basePath         string
	modulePath       string
	fileSet          *token.FileSet
	externalImporter types.Importer
	packages         map[string]*types.Package
	loading          map[string]bool
	exportFilesOnce  sync.Once
	exportFiles      map[string]string
	exportFilesErr   error
	sourceFilesOnce  sync.Once
	sourcePackages   map[string]commentPackage
	sourceFilesErr   error
	sourceSizes      types.Sizes
}

type applicationPackageSemantics struct {
	hasSource bool
	packageGo *types.Package
}

// newApplicationSemanticContext 创建一次发现批次共享的类型上下文，保证跨包比较
// 使用同一组类型对象，并复用外部包导入缓存。
func newApplicationSemanticContext(basePath, modulePath string) *applicationSemanticContext {
	fileSet := token.NewFileSet()
	context := &applicationSemanticContext{
		basePath:   basePath,
		modulePath: strings.TrimSuffix(modulePath, "/"),
		fileSet:    fileSet,
		packages:   make(map[string]*types.Package),
		loading:    make(map[string]bool),
	}
	context.externalImporter = importer.ForCompiler(fileSet, "gc", context.lookupExportData)
	return context
}

func newApplicationSemanticContextFromModule(basePath string) (*applicationSemanticContext, error) {
	modulePath, err := readApplicationModulePath(basePath)
	if err != nil {
		return nil, err
	}
	return newApplicationSemanticContext(basePath, modulePath), nil
}

// inspectApplicationPackage 解析当前目标有效的非测试源码，并交给 go/types
// 建立包级对象关系；生成文件被排除，避免旧结果反向影响新发现。
func (context *applicationSemanticContext) inspectApplicationPackage(directory, packageName string) (applicationPackageSemantics, error) {
	files, hasSource, err := context.parseApplicationFiles(directory, packageName)
	if err != nil || !hasSource {
		return applicationPackageSemantics{hasSource: hasSource}, err
	}
	files = applicationSemanticFiles(files)
	packagePath := context.packagePath(directory)
	configuration := types.Config{
		IgnoreFuncBodies:         true,
		DisableUnusedImportCheck: true,
		Importer:                 context,
		Sizes:                    context.sourceSizes,
	}
	checkedPackage, checkErr := configuration.Check(packagePath, context.fileSet, files, nil)
	if checkErr != nil {
		return applicationPackageSemantics{}, fmt.Errorf("类型检查应用包 %q 失败: %w", directory, checkErr)
	}
	return applicationPackageSemantics{hasSource: true, packageGo: checkedPackage}, nil
}

// applicationSemanticFiles 只保留导入、常量、类型和函数声明。包级变量的
// 初始化表达式不参与发现契约，避免无关运行时装配干扰类型和签名判定；
// 常量仍需保留，供数组长度和泛型约束等类型表达式解析。
func applicationSemanticFiles(files []*ast.File) []*ast.File {
	semanticFiles := make([]*ast.File, 0, len(files))
	for _, file := range files {
		semanticFile := *file
		semanticFile.Decls = make([]ast.Decl, 0, len(file.Decls))
		for _, declaration := range file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				semanticFile.Decls = append(semanticFile.Decls, typed)
			case *ast.GenDecl:
				if typed.Tok == token.IMPORT || typed.Tok == token.CONST || typed.Tok == token.TYPE {
					semanticFile.Decls = append(semanticFile.Decls, typed)
				}
			}
		}
		semanticFiles = append(semanticFiles, &semanticFile)
	}
	return semanticFiles
}

func (context *applicationSemanticContext) parseApplicationFiles(directory, packageName string) ([]*ast.File, bool, error) {
	root, err := os.OpenRoot(context.basePath)
	if err != nil {
		return nil, false, fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	openedDirectory, err := root.Open(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("打开应用目录 %q 失败: %w", directory, err)
	}
	entries, readErr := openedDirectory.ReadDir(-1)
	closeErr := openedDirectory.Close()
	if readErr != nil || closeErr != nil {
		return nil, false, errors.Join(fmt.Errorf("读取应用目录 %q 失败: %w", directory, readErr), closeErr)
	}
	candidates := entries[:0]
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || strings.HasSuffix(name, "_test.go") || name == applicationDiscoveryFilename {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, false, fmt.Errorf("应用源码 %q 不能是符号链接", name)
		}
		candidates = append(candidates, entry)
	}
	if len(candidates) == 0 {
		return nil, false, nil
	}
	files := make([]*ast.File, 0, len(candidates))
	selected, err := context.selectedApplicationFiles(directory, packageName)
	if err != nil {
		return nil, false, err
	}
	for _, entry := range candidates {
		name := entry.Name()
		if !selected[name] {
			continue
		}
		source, readSourceErr := root.ReadFile(filepath.Join(directory, name))
		if readSourceErr != nil {
			return nil, false, fmt.Errorf("读取应用源码 %q 失败: %w", name, readSourceErr)
		}
		file, parseErr := parser.ParseFile(context.fileSet, filepath.Join(directory, name), source, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil, false, fmt.Errorf("解析应用源码 %q 失败: %w", name, parseErr)
		}
		if file.Name == nil || packageName != "" && file.Name.Name != packageName {
			return nil, false, fmt.Errorf("应用源码 %q 的包名必须为 %s", name, packageName)
		}
		if len(files) > 0 && file.Name.Name != files[0].Name.Name {
			return nil, false, fmt.Errorf("应用源码 %q 的包名 %s 与同目录源码包名 %s 不一致", name, file.Name.Name, files[0].Name.Name)
		}
		files = append(files, file)
	}
	return files, len(files) > 0, nil
}

func (context *applicationSemanticContext) packagePath(directory string) string {
	relative := filepath.ToSlash(filepath.Clean(directory))
	if relative == "." || relative == "" {
		return context.modulePath
	}
	return context.modulePath + "/" + strings.TrimPrefix(relative, "/")
}

// Import 优先加载项目内包，框架公共签名使用稳定的最小类型模型；其余依赖
// 读取 go list 提供的编译导出数据，避免依靠名称字符串猜测类型身份。
func (context *applicationSemanticContext) Import(importPath string) (*types.Package, error) {
	if imported, exists := context.packages[importPath]; exists {
		return imported, nil
	}
	if context.isLocalImport(importPath) {
		return context.importLocalPackage(importPath)
	}
	return context.externalImporter.Import(importPath)
}

func (context *applicationSemanticContext) isLocalImport(importPath string) bool {
	// 框架包始终从当前目标模块图的编译导出数据读取，避免发现器维护第二套类型定义。
	if importPath == frameworkImportPath || strings.HasPrefix(importPath, frameworkImportPath+"/") {
		return false
	}
	return importPath == context.modulePath || strings.HasPrefix(importPath, context.modulePath+"/")
}

func (context *applicationSemanticContext) importLocalPackage(importPath string) (*types.Package, error) {
	if context.loading[importPath] {
		return nil, fmt.Errorf("应用源码存在循环导入 %q", importPath)
	}
	context.loading[importPath] = true
	defer delete(context.loading, importPath)
	relative := strings.TrimPrefix(strings.TrimPrefix(importPath, context.modulePath), "/")
	directory := filepath.FromSlash(relative)
	files, hasSource, err := context.parseApplicationFiles(directory, "")
	if err != nil {
		return nil, err
	}
	if !hasSource {
		return nil, fmt.Errorf("项目内导入包 %q 没有当前目标可用的源码", importPath)
	}
	files = applicationSemanticFiles(files)
	configuration := types.Config{
		IgnoreFuncBodies:         true,
		DisableUnusedImportCheck: true,
		Importer:                 context,
		Sizes:                    context.sourceSizes,
	}
	checkedPackage, checkErr := configuration.Check(importPath, context.fileSet, files, nil)
	if checkErr != nil {
		return nil, fmt.Errorf("类型检查项目内导入包 %q 失败: %w", importPath, checkErr)
	}
	context.packages[importPath] = checkedPackage
	return checkedPackage, nil
}

func (context *applicationSemanticContext) lookupExportData(importPath string) (io.ReadCloser, error) {
	context.exportFilesOnce.Do(func() {
		context.exportFiles, context.exportFilesErr = context.loadExportFiles()
	})
	if context.exportFilesErr != nil {
		return nil, context.exportFilesErr
	}
	exportPath := context.exportFiles[importPath]
	if exportPath == "" {
		loadedFiles, err := context.loadNamedExportFiles(importPath)
		if err != nil {
			return nil, err
		}
		for loadedImportPath, loadedExportPath := range loadedFiles {
			context.exportFiles[loadedImportPath] = loadedExportPath
		}
		exportPath = context.exportFiles[importPath]
		if exportPath == "" {
			return nil, fmt.Errorf("依赖包 %q 没有可用的编译导出数据", importPath)
		}
	}
	file, err := openApplicationExportFile(exportPath)
	if err != nil {
		return nil, fmt.Errorf("打开依赖包 %q 的导出数据失败: %w", importPath, err)
	}
	return file, nil
}

type applicationExportPackage struct {
	ImportPath string
	Export     string
}

// loadExportFiles 使用固定参数一次性枚举框架公共类型及其完整依赖图。
func (context *applicationSemanticContext) loadExportFiles() (map[string]string, error) {
	command := exec.Command(
		"go", "list", "-mod=readonly", "-deps", "-export", "-json", "--",
		frameworkImportPath, frameworkMiddlewareImportPath,
	)
	return context.executeExportList(command)
}

// loadNamedExportFiles 只在应用源码引用框架依赖图之外的包时执行。
// 包路径先按 Go 导入路径的安全子集校验，再追加到固定命令参数；全过程不经过 shell。
func (context *applicationSemanticContext) loadNamedExportFiles(importPath string) (map[string]string, error) {
	if err := validateApplicationImportPath(importPath); err != nil {
		return nil, err
	}
	command := exec.Command("go", "list", "-mod=readonly", "-deps", "-export", "-json")
	command.Args = append(command.Args, "--", importPath)
	return context.executeExportList(command)
}

func (context *applicationSemanticContext) executeExportList(command *exec.Cmd) (map[string]string, error) {
	command.Dir = context.basePath
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		var exitError *exec.ExitError
		details := ""
		if errors.As(err, &exitError) {
			details = strings.TrimSpace(string(exitError.Stderr))
		}
		if details != "" {
			return nil, fmt.Errorf("枚举依赖包导出数据失败: %w: %s", err, details)
		}
		return nil, fmt.Errorf("枚举依赖包导出数据失败: %w", err)
	}
	exportFiles := make(map[string]string)
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		listedPackage := applicationExportPackage{}
		decodeErr := decoder.Decode(&listedPackage)
		if errors.Is(decodeErr, io.EOF) {
			break
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("解析依赖包导出数据失败: %w", decodeErr)
		}
		importPath := strings.TrimSpace(listedPackage.ImportPath)
		exportPath := strings.TrimSpace(listedPackage.Export)
		if importPath != "" && exportPath != "" {
			exportFiles[importPath] = exportPath
		}
	}
	return exportFiles, nil
}

func validateApplicationImportPath(importPath string) error {
	if importPath == "" || strings.TrimSpace(importPath) != importPath || strings.HasPrefix(importPath, "/") {
		return fmt.Errorf("依赖包导入路径无效: %q", importPath)
	}
	segments := strings.Split(importPath, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("依赖包导入路径无效: %q", importPath)
		}
		for _, character := range segment {
			valid := character >= 'a' && character <= 'z' ||
				character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' ||
				strings.ContainsRune("._~-+", character)
			if !valid {
				return fmt.Errorf("依赖包导入路径包含非法字符: %q", importPath)
			}
		}
	}
	return nil
}

// openApplicationExportFile 通过受限目录句柄打开 go list 返回的单个归档文件。
// 即使归档名异常，也不能借助路径穿越或符号链接逃出其已解析父目录。
func openApplicationExportFile(exportPath string) (io.ReadCloser, error) {
	cleanedPath := filepath.Clean(strings.TrimSpace(exportPath))
	if cleanedPath == "." || !filepath.IsAbs(cleanedPath) {
		return nil, fmt.Errorf("导出数据路径必须是绝对路径")
	}
	directory, err := filepath.EvalSymlinks(filepath.Dir(cleanedPath))
	if err != nil {
		return nil, fmt.Errorf("解析导出数据目录失败: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("打开导出数据目录失败: %w", err)
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(cleanedPath))
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("读取导出数据文件状态失败: %w", err)
		}
		return nil, fmt.Errorf("导出数据必须是普通文件")
	}
	return file, nil
}

func (context *applicationSemanticContext) frameworkType(importPath, name string) (types.Type, error) {
	semanticPackage, err := context.Import(importPath)
	if err != nil {
		return nil, fmt.Errorf("导入框架语义包 %q 失败: %w", importPath, err)
	}
	typeObject, ok := semanticPackage.Scope().Lookup(name).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("缺少框架语义类型 %s.%s", semanticPackage.Name(), name)
	}
	return typeObject.Type(), nil
}

func discoverApplicationStructTypesWithContext(context *applicationSemanticContext, directory, packageName string) ([]string, bool, error) {
	inspected, err := context.inspectApplicationPackage(directory, packageName)
	if err != nil || !inspected.hasSource {
		return nil, inspected.hasSource, err
	}
	typeNames := make([]string, 0)
	for _, name := range inspected.packageGo.Scope().Names() {
		if !token.IsExported(name) {
			continue
		}
		typeObject, ok := inspected.packageGo.Scope().Lookup(name).(*types.TypeName)
		if !ok || !applicationTypeCanUseEmptyComposite(typeObject.Type()) {
			continue
		}
		typeNames = append(typeNames, name)
	}
	sort.Strings(typeNames)
	return typeNames, true, nil
}

func applicationTypeCanUseEmptyComposite(candidate types.Type) bool {
	unalias := types.Unalias(candidate)
	if named, ok := unalias.(*types.Named); ok {
		if named.TypeParams().Len() > 0 && named.TypeArgs().Len() == 0 {
			return false
		}
		_, ok = named.Underlying().(*types.Struct)
		return ok
	}
	_, ok := unalias.(*types.Struct)
	return ok
}

func discoverApplicationConstructorsWithContext(context *applicationSemanticContext, directory, packageName string, typeNames []string) (map[string]bool, error) {
	constructors := make(map[string]bool, len(typeNames))
	if len(typeNames) == 0 {
		return constructors, nil
	}
	inspected, err := context.inspectApplicationPackage(directory, packageName)
	if err != nil {
		return nil, err
	}
	if !inspected.hasSource {
		return constructors, nil
	}
	for _, name := range typeNames {
		typeObject, ok := inspected.packageGo.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		function, ok := inspected.packageGo.Scope().Lookup("New" + name).(*types.Func)
		if !ok {
			continue
		}
		signature, ok := function.Type().(*types.Signature)
		if !ok || signature.Recv() != nil || signature.TypeParams().Len() != 0 || signature.Variadic() || signature.Params().Len() != 0 || signature.Results().Len() != 1 {
			continue
		}
		if types.Identical(signature.Results().At(0).Type(), types.NewPointer(typeObject.Type())) {
			constructors[name] = true
		}
	}
	return constructors, nil
}

func discoverApplicationEntryPointsWithContext(context *applicationSemanticContext, directory, packageName string) (applicationEntryPoints, error) {
	inspected, err := context.inspectApplicationPackage(directory, packageName)
	if err != nil {
		return applicationEntryPoints{}, err
	}
	if !inspected.hasSource {
		return applicationEntryPoints{}, nil
	}
	emptyInterface := types.NewInterfaceType(nil, nil)
	emptyInterface.Complete()
	eventDefinition, err := context.frameworkType(frameworkImportPath, "EventDefinition")
	if err != nil {
		return applicationEntryPoints{}, err
	}
	middlewareHandler, err := context.frameworkType(frameworkMiddlewareImportPath, "Handler")
	if err != nil {
		return applicationEntryPoints{}, err
	}
	wanted := []struct {
		name       string
		resultType types.Type
		set        func(*applicationEntryPoints)
		expected   string
	}{
		{name: "Services", resultType: types.NewSlice(emptyInterface), set: func(points *applicationEntryPoints) { points.services = true }, expected: "[]interface{}"},
		{name: "Events", resultType: eventDefinition, set: func(points *applicationEntryPoints) { points.events = true }, expected: "framework.EventDefinition"},
		{name: "Middleware", resultType: types.NewSlice(middlewareHandler), set: func(points *applicationEntryPoints) { points.middleware = true }, expected: "[]middleware.Handler"},
		{name: "Providers", resultType: types.NewMap(types.Typ[types.String], emptyInterface), set: func(points *applicationEntryPoints) { points.providers = true }, expected: "map[string]interface{}"},
	}
	points := applicationEntryPoints{}
	for _, target := range wanted {
		object := inspected.packageGo.Scope().Lookup(target.name)
		if object == nil {
			continue
		}
		function, ok := object.(*types.Func)
		if !ok {
			continue
		}
		signature, ok := function.Type().(*types.Signature)
		if !ok || signature.Recv() != nil || signature.TypeParams().Len() != 0 || signature.Variadic() || signature.Params().Len() != 0 || signature.Results().Len() != 1 || !types.Identical(signature.Results().At(0).Type(), target.resultType) {
			return applicationEntryPoints{}, fmt.Errorf("应用入口 %s.%s 必须声明为 func() %s", packageName, target.name, target.expected)
		}
		target.set(&points)
	}
	return points, nil
}

func applicationPackageExistsWithContext(context *applicationSemanticContext, directory, packageName string) (bool, error) {
	inspected, err := context.inspectApplicationPackage(directory, packageName)
	if err != nil || !inspected.hasSource {
		return false, err
	}
	routeType, err := context.frameworkType(frameworkImportPath, "Route")
	if err != nil {
		return false, err
	}
	function, ok := inspected.packageGo.Scope().Lookup("Load").(*types.Func)
	if !ok {
		return false, fmt.Errorf("路由入口 %s.Load 必须声明为 func(*framework.Route)", packageName)
	}
	signature, ok := function.Type().(*types.Signature)
	valid := ok && signature.Recv() == nil && signature.TypeParams().Len() == 0 && !signature.Variadic() && signature.Params().Len() == 1 && signature.Results().Len() == 0
	if valid {
		valid = types.Identical(signature.Params().At(0).Type(), types.NewPointer(routeType))
	}
	if !valid {
		return false, fmt.Errorf("路由入口 %s.Load 必须声明为 func(*framework.Route)", packageName)
	}
	return true, nil
}
