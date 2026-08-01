package command

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"thinkgo/framework"
)

const applicationModulePath = "thinkgo"

type applicationRegistrationKind string

const (
	applicationRegistrationController applicationRegistrationKind = "controller"
	applicationRegistrationMiddleware applicationRegistrationKind = "middleware"
	applicationRegistrationProvider   applicationRegistrationKind = "provider"
	applicationRegistrationListener   applicationRegistrationKind = "listener"
	applicationRegistrationSubscriber applicationRegistrationKind = "subscriber"
)

// updateApplicationRegistration 更新指定应用的组件注册入口，保证生成结果可以重复执行。
func updateApplicationRegistration(app *framework.App, kind applicationRegistrationKind, typeName string) (returnErr error) {
	if app == nil {
		return framework.ErrNilApplication
	}
	if strings.TrimSpace(typeName) == "" || !isExportedIdentifier(typeName) {
		return fmt.Errorf("组件类型名无效: %q", typeName)
	}
	componentDirectory, err := applicationComponentDirectory(kind)
	if err != nil {
		return err
	}
	applicationDirectory, err := applicationRelativeDirectory(app)
	if err != nil {
		return err
	}
	registrationLock, err := acquireApplicationRegistrationLock(app.BasePath, applicationDirectory)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, registrationLock.Close())
	}()
	applicationSourcePath := filepath.Join(applicationDirectory, "application.go")
	importPath := filepath.ToSlash(filepath.Join(applicationModulePath, applicationDirectory, componentDirectory))

	source, exists, err := readApplicationSource(app.BasePath, applicationSourcePath)
	if err != nil {
		return err
	}
	if !exists {
		packageName := applicationPackageName(app)
		source = []byte(fmt.Sprintf(`package %s

import "thinkgo/framework"

// Register 注册当前应用的组件。
func Register(app *framework.App) error {
	if app == nil {
		return framework.ErrNilApplication
	}
	return nil
}
`, packageName))
	}

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, applicationSourcePath, source, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("解析应用注册文件失败: %w", err)
	}
	register, err := findApplicationRegister(file)
	if err != nil {
		return err
	}
	importAlias, importChanged, err := ensureApplicationImport(file, importPath)
	if err != nil {
		return err
	}
	eventImportAlias := "event"
	if kind == applicationRegistrationListener || kind == applicationRegistrationSubscriber {
		var eventImportChanged bool
		eventImportAlias, eventImportChanged, err = ensureApplicationImport(file, "thinkgo/framework/event")
		if err != nil {
			return err
		}
		importChanged = importChanged || eventImportChanged
	}
	registrationStatement, err := newRegistrationStatement(kind, importAlias, typeName, eventImportAlias)
	if err != nil {
		return err
	}
	registrationChanged, err := appendRegistrationStatement(register, registrationStatement)
	if err != nil {
		return err
	}
	registrationChanged = normalizeRegistrationStatements(register) || registrationChanged

	var formatted bytes.Buffer
	if err := format.Node(&formatted, fileSet, file); err != nil {
		return fmt.Errorf("格式化应用注册文件失败: %w", err)
	}
	formattedSource, err := format.Source(formatted.Bytes())
	if err != nil {
		return fmt.Errorf("校验应用注册文件格式失败: %w", err)
	}
	if !exists || importChanged || registrationChanged || !bytes.Equal(source, formattedSource) {
		if err := writeApplicationSource(app.BasePath, applicationSourcePath, formattedSource); err != nil {
			return err
		}
	}
	return nil
}

func applicationComponentDirectory(kind applicationRegistrationKind) (string, error) {
	switch kind {
	case applicationRegistrationController:
		return "controller", nil
	case applicationRegistrationMiddleware:
		return "middleware", nil
	case applicationRegistrationProvider:
		return "service", nil
	case applicationRegistrationListener:
		return "listener", nil
	case applicationRegistrationSubscriber:
		return "subscribe", nil
	default:
		return "", fmt.Errorf("不支持的应用注册类型: %q", kind)
	}
}

func applicationPackageName(app *framework.App) string {
	rawName := ""
	if app != nil {
		rawName = strings.TrimSpace(app.ApplicationName)
		if rawName == "" {
			rawName = filepath.Base(filepath.Clean(app.ApplicationPath))
		}
	}
	var builder strings.Builder
	for _, character := range strings.ToLower(rawName) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			builder.WriteRune(character)
			continue
		}
		builder.WriteByte('_')
	}
	name := strings.Trim(builder.String(), "_")
	if name == "" {
		return "app"
	}
	first, _ := utf8FirstRune(name)
	if unicode.IsDigit(first) {
		return "app_" + name
	}
	return name
}

func utf8FirstRune(value string) (rune, int) {
	for _, character := range value {
		return character, len(string(character))
	}
	return 0, 0
}

func readApplicationSource(basePath, relativePath string) ([]byte, bool, error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, false, fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer root.Close()
	source, err := root.ReadFile(relativePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("读取应用注册文件失败: %w", err)
	}
	return source, true, nil
}

func findApplicationRegister(file *ast.File) (*ast.FuncDecl, error) {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name == nil || function.Name.Name != "Register" {
			continue
		}
		if function.Body == nil {
			return nil, fmt.Errorf("应用 Register 函数缺少函数体")
		}
		return function, nil
	}
	return nil, fmt.Errorf("应用注册文件缺少 Register 函数")
}

func ensureApplicationImport(file *ast.File, importPath string) (string, bool, error) {
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			return "", false, fmt.Errorf("解析应用导入路径失败: %w", err)
		}
		if path != importPath {
			continue
		}
		if specification.Name != nil {
			if specification.Name.Name == "_" || specification.Name.Name == "." {
				return "", false, fmt.Errorf("应用组件导入别名不可用于注册: %q", specification.Name.Name)
			}
			return specification.Name.Name, false, nil
		}
		return filepath.Base(importPath), false, nil
	}

	alias := filepath.Base(importPath)
	if !isGoPackageIdentifier(alias) {
		return "", false, fmt.Errorf("应用组件包名无效: %q", alias)
	}
	specification := &ast.ImportSpec{
		Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importPath)},
	}
	var importDeclaration *ast.GenDecl
	for _, declaration := range file.Decls {
		candidate, ok := declaration.(*ast.GenDecl)
		if ok && candidate.Tok == token.IMPORT {
			importDeclaration = candidate
			break
		}
	}
	if importDeclaration == nil {
		importDeclaration = &ast.GenDecl{Tok: token.IMPORT, Lparen: token.Pos(1)}
		file.Decls = append([]ast.Decl{importDeclaration}, file.Decls...)
	}
	if len(importDeclaration.Specs) > 0 && !importDeclaration.Lparen.IsValid() {
		importDeclaration.Lparen = token.Pos(1)
	}
	importDeclaration.Specs = append(importDeclaration.Specs, specification)
	sortImportSpecs(importDeclaration)
	file.Imports = append(file.Imports, specification)
	return alias, true, nil
}

func sortImportSpecs(declaration *ast.GenDecl) {
	sort.SliceStable(declaration.Specs, func(left, right int) bool {
		leftImport, _ := declaration.Specs[left].(*ast.ImportSpec)
		rightImport, _ := declaration.Specs[right].(*ast.ImportSpec)
		leftPath := ""
		rightPath := ""
		if leftImport != nil && leftImport.Path != nil {
			leftPath, _ = strconv.Unquote(leftImport.Path.Value)
		}
		if rightImport != nil && rightImport.Path != nil {
			rightPath, _ = strconv.Unquote(rightImport.Path.Value)
		}
		if leftPath == rightPath {
			return importAlias(leftImport) < importAlias(rightImport)
		}
		return leftPath < rightPath
	})
}

func importAlias(specification *ast.ImportSpec) string {
	if specification == nil || specification.Name == nil {
		return ""
	}
	return specification.Name.Name
}

func newRegistrationStatement(kind applicationRegistrationKind, importAlias, typeName, eventImportAlias string) (ast.Stmt, error) {
	if !isGoPackageIdentifier(importAlias) {
		return nil, fmt.Errorf("应用组件导入别名无效: %q", importAlias)
	}
	var call string
	switch kind {
	case applicationRegistrationController:
		call = fmt.Sprintf(`app.RegisterController(%s, &%s.%s{})`, strconv.Quote(typeName), importAlias, typeName)
	case applicationRegistrationMiddleware:
		call = fmt.Sprintf("app.RegisterGlobalMiddleware(%s.%s)", importAlias, typeName)
	case applicationRegistrationProvider:
		call = fmt.Sprintf("app.RegisterProvider(%s.New%s(app))", importAlias, typeName)
	case applicationRegistrationListener:
		call = fmt.Sprintf(`func() error {
	%sDispatcher, err := framework.ResolveServiceAs[*%s.Dispatcher](app, framework.ServiceEvent)
	if err != nil {
		return err
	}
	if err := %sDispatcher.Listen("*", &%s.%s{}); err != nil {
		return err
	}
	return nil
	}()`, eventImportAlias, eventImportAlias, eventImportAlias, importAlias, typeName)
	case applicationRegistrationSubscriber:
		call = fmt.Sprintf(`func() error {
	%sDispatcher, err := framework.ResolveServiceAs[*%s.Dispatcher](app, framework.ServiceEvent)
	if err != nil {
		return err
	}
	if err := %sDispatcher.Subscribe(&%s.%s{}); err != nil {
		return err
	}
	return nil
	}()`, eventImportAlias, eventImportAlias, eventImportAlias, importAlias, typeName)
	default:
		return nil, fmt.Errorf("不支持的应用注册类型: %q", kind)
	}
	return parseRegistrationStatement(fmt.Sprintf("if err := %s; err != nil {\n\treturn err\n}", call))
}

func parseRegistrationStatement(source string) (ast.Stmt, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "registration.go", []byte("package application\n\nfunc register() error {\n"+source+"\n}\n"), 0)
	if err != nil {
		return nil, fmt.Errorf("生成应用注册语句失败: %w", err)
	}
	function, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok || function.Body == nil || len(function.Body.List) != 1 {
		return nil, fmt.Errorf("生成应用注册语句失败: 语句数量无效")
	}
	return function.Body.List[0], nil
}

func appendRegistrationStatement(register *ast.FuncDecl, candidate ast.Stmt) (bool, error) {
	if register == nil || register.Body == nil {
		return false, fmt.Errorf("应用 Register 函数不可用")
	}
	candidateCall, ok := applicationRegistrationCall(candidate)
	if !ok {
		return false, fmt.Errorf("应用注册语句格式无效")
	}
	candidateKey := applicationRegistrationCallKey(candidateCall)
	for _, statement := range register.Body.List {
		call, exists := applicationRegistrationCall(statement)
		if exists && applicationRegistrationCallKey(call) == candidateKey {
			return false, nil
		}
	}
	insertAt := len(register.Body.List)
	for index, statement := range register.Body.List {
		if isNilReturnStatement(statement) {
			insertAt = index
			break
		}
	}
	register.Body.List = append(register.Body.List, nil)
	copy(register.Body.List[insertAt+1:], register.Body.List[insertAt:])
	register.Body.List[insertAt] = candidate
	return true, nil
}

func normalizeRegistrationStatements(register *ast.FuncDecl) bool {
	if register == nil || register.Body == nil {
		return false
	}
	generated := make([]ast.Stmt, 0)
	firstGeneratedPosition := -1
	nonGenerated := make([]ast.Stmt, 0, len(register.Body.List))
	for index, statement := range register.Body.List {
		if _, ok := applicationRegistrationCall(statement); ok {
			if firstGeneratedPosition < 0 {
				firstGeneratedPosition = len(nonGenerated)
			}
			generated = append(generated, statement)
			continue
		}
		nonGenerated = append(nonGenerated, statement)
		_ = index
	}
	if len(generated) < 2 {
		return false
	}
	sort.SliceStable(generated, func(left, right int) bool {
		return applicationRegistrationCallKeyFromStatement(generated[left]) < applicationRegistrationCallKeyFromStatement(generated[right])
	})
	register.Body.List = append(nonGenerated[:firstGeneratedPosition], append(generated, nonGenerated[firstGeneratedPosition:]...)...)
	return true
}

func applicationRegistrationCall(statement ast.Stmt) (*ast.CallExpr, bool) {
	ifStatement, ok := statement.(*ast.IfStmt)
	if !ok || ifStatement.Init == nil {
		return nil, false
	}
	assignment, ok := ifStatement.Init.(*ast.AssignStmt)
	if !ok || assignment.Tok != token.DEFINE || len(assignment.Rhs) != 1 {
		return nil, false
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	if isApplicationRegistrationCall(call) {
		return call, true
	}
	return applicationEventRegistrationCall(call)
}

func isApplicationRegistrationCall(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil {
		return false
	}
	if receiver, ok := selector.X.(*ast.Ident); ok && receiver.Name == "app" {
		switch selector.Sel.Name {
		case "RegisterController", "RegisterGlobalMiddleware", "RegisterProvider":
			return true
		}
	}
	return false
}

// applicationEventRegistrationCall 识别生成器使用服务解析器创建的事件注册闭包。
func applicationEventRegistrationCall(call *ast.CallExpr) (*ast.CallExpr, bool) {
	if call == nil {
		return nil, false
	}
	literal, ok := call.Fun.(*ast.FuncLit)
	if !ok || literal.Body == nil {
		return nil, false
	}
	for _, statement := range literal.Body.List {
		ifStatement, ok := statement.(*ast.IfStmt)
		if !ok || ifStatement.Init == nil {
			continue
		}
		assignment, ok := ifStatement.Init.(*ast.AssignStmt)
		if !ok || len(assignment.Rhs) != 1 {
			continue
		}
		registrationCall, ok := assignment.Rhs[0].(*ast.CallExpr)
		if !ok || !isEventDispatcherRegistrationCall(registrationCall) {
			continue
		}
		return registrationCall, true
	}
	return nil, false
}

func isEventDispatcherRegistrationCall(call *ast.CallExpr) bool {
	if call == nil {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel == nil {
		return false
	}
	if _, ok := selector.X.(*ast.Ident); !ok {
		return false
	}
	return selector.Sel.Name == "Listen" || selector.Sel.Name == "Subscribe"
}

func applicationRegistrationCallKeyFromStatement(statement ast.Stmt) string {
	call, ok := applicationRegistrationCall(statement)
	if !ok {
		return ""
	}
	return applicationRegistrationCallKey(call)
}

func applicationRegistrationCallKey(call *ast.CallExpr) string {
	if call == nil {
		return ""
	}
	var rendered bytes.Buffer
	if err := format.Node(&rendered, token.NewFileSet(), call); err != nil {
		return ""
	}
	return rendered.String()
}

func isNilReturnStatement(statement ast.Stmt) bool {
	returnStatement, ok := statement.(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		return false
	}
	identifier, ok := returnStatement.Results[0].(*ast.Ident)
	return ok && identifier.Name == "nil"
}

func isGoPackageIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if index == 0 && !unicode.IsLetter(character) && character != '_' {
			return false
		}
		if index > 0 && !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_' {
			return false
		}
	}
	return true
}

func writeApplicationSource(basePath, relativePath string, source []byte) (returnErr error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return fmt.Errorf("打开项目根目录失败: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()
	if err := root.MkdirAll(filepath.Dir(relativePath), 0o755); err != nil {
		return fmt.Errorf("创建应用注册目录失败: %w", err)
	}
	permission := os.FileMode(0o644)
	if information, statErr := root.Stat(relativePath); statErr == nil {
		permission = information.Mode().Perm()
	}
	temporaryPath := fmt.Sprintf("%s.tmp-%d", relativePath, time.Now().UnixNano())
	file, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
	if err != nil {
		return fmt.Errorf("创建应用注册临时文件失败: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			returnErr = errors.Join(returnErr, root.Remove(temporaryPath))
		}
	}()
	written, writeErr := file.Write(source)
	if writeErr == nil && written != len(source) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(fmt.Errorf("写入应用注册临时文件失败: %w", writeErr), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("同步应用注册临时文件失败: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭应用注册临时文件失败: %w", err)
	}
	if err := root.Rename(temporaryPath, relativePath); err != nil {
		return fmt.Errorf("替换应用注册文件失败: %w", err)
	}
	removeTemporary = false
	return nil
}
