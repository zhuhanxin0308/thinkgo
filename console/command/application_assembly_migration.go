package command

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type discoverySourceSnapshot struct {
	information os.FileInfo
	content     []byte
}

// projectAssemblyMigrationSources 只计划入口引用变更；发布与清单共用同一回滚事务。
func projectAssemblyMigrationSources(basePath, modulePath string, migrate bool) ([]generatedApplicationSource, error) {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var sources []generatedApplicationSource
	for _, directory := range []string{".", filepath.Join("cmd", "think")} {
		opened, err := root.Open(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries, readErr := opened.ReadDir(-1)
		if err := errors.Join(readErr, opened.Close()); err != nil {
			return nil, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || name == applicationDiscoveryFilename || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			path := filepath.Join(directory, name)
			snapshot, err := readAssemblySource(root, path)
			if err != nil {
				return nil, err
			}
			content, err := migrateApplicationEntry(snapshot.content, modulePath+"/app")
			if err != nil {
				return nil, fmt.Errorf("入口 %q 无法安全迁移，请人工调整: %w", path, err)
			}
			if bytes.Equal(content, snapshot.content) {
				continue
			}
			if !migrate {
				return nil, fmt.Errorf("入口 %q 仍使用 app.Register，请执行 service:discover --migrate 或人工改为 registerApplications", path)
			}
			sources = append(sources, generatedApplicationSource{relativePath: path, source: content, expected: snapshot})
		}
	}
	if len(sources) > 0 {
		semantic := newApplicationSemanticContext(basePath, modulePath)
		inspected, err := semantic.inspectApplicationPackage("app", "app")
		if err != nil {
			return nil, err
		}
		if inspected.hasSource && inspected.packageGo.Scope().Lookup("Register") != nil {
			return nil, fmt.Errorf("根 app.Register 包含手写实现，请人工迁移其业务逻辑后重试")
		}
	}
	return sources, nil
}

// readAssemblySource 同时保存文件身份和内容，防止读取后发生的外部修改被覆盖。
func readAssemblySource(root *os.Root, path string) (*discoverySourceSnapshot, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("应用装配入口 %q 必须是普通文件", path)
	}
	content, err := root.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := verifyControllerDiscoverySourceSnapshot(root, path, info, content); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrControllerDiscoveryPublishConflict, path, err)
	}
	return &discoverySourceSnapshot{information: info, content: content}, nil
}

// protectAssemblyTargets 避免新增的入口清单覆盖同名手写文件。
func protectAssemblyTargets(basePath string, sources []generatedApplicationSource) error {
	root, err := os.OpenRoot(basePath)
	if err != nil {
		return err
	}
	defer root.Close()
	for index := range sources {
		current := &sources[index]
		if current.relativePath != applicationDiscoveryFilename && current.relativePath != filepath.Join("cmd", "think", applicationDiscoveryFilename) {
			continue
		}
		snapshot, err := readAssemblySource(root, current.relativePath)
		if errors.Is(err, os.ErrNotExist) {
			current.expected = &discoverySourceSnapshot{}
			continue
		}
		if err != nil {
			return err
		}
		content := bytes.TrimPrefix(snapshot.content, []byte("//go:build thinkgo_runtime\n\n"))
		if !bytes.HasPrefix(content, []byte(applicationAssemblyHeader)) {
			return fmt.Errorf("应用清单 %q 已被手写文件占用，请人工迁移后重试", current.relativePath)
		}
		current.expected = snapshot
	}
	return nil
}

type applicationEntryEdit struct {
	start, end int
	text       string
}

func migrateApplicationEntry(content []byte, appImport string) ([]byte, error) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, "entry.go", content, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	if file.Scope.Lookup("registerApplications") != nil {
		return nil, fmt.Errorf("registerApplications 名称已被手写声明占用")
	}
	var edits []applicationEntryEdit
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil || path != appImport {
			continue
		}
		alias := "app"
		if imported.Name != nil {
			alias = imported.Name.Name
		}
		if alias == "." {
			return nil, fmt.Errorf("根 app 的点导入无法安全迁移")
		}
		used, changed := false, false
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok || identifier.Name != alias || identifier.Obj != nil {
				return true
			}
			if selector.Sel.Name != "Register" {
				used = true
				return true
			}
			changed = true
			edits = append(edits, applicationEntryEdit{positions.Position(selector.Pos()).Offset, positions.Position(selector.End()).Offset, "registerApplications"})
			return true
		})
		if changed && !used {
			start, end := imported.Pos(), imported.End()
			for _, declaration := range file.Decls {
				group, ok := declaration.(*ast.GenDecl)
				if ok && group.Tok == token.IMPORT && len(group.Specs) == 1 && group.Specs[0] == imported {
					start, end = group.Pos(), group.End()
				}
			}
			edits = append(edits, applicationEntryEdit{positions.Position(start).Offset, positions.Position(end).Offset, ""})
		}
	}
	if len(edits) == 0 {
		return content, nil
	}
	if file.Name.Name != "main" {
		return nil, fmt.Errorf("只能自动迁移 main 包中的注册入口")
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := append([]byte(nil), content...)
	for _, edit := range edits {
		result = append(append(result[:edit.start:edit.start], edit.text...), result[edit.end:]...)
	}
	return format.Source(result)
}
