package command

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
)

type commentSourceFile struct {
	packagePath string
	file        *ast.File
}

type commentTypeReference struct {
	target   string
	position token.Pos
}

// inheritAPITypeComments 只沿实际类型定义继承字段说明，不增加字段或改变对外结构。
// 包名由 go list 给出，避免导入路径末段与 package 名不一致时解析错误。
func inheritAPITypeComments(docs binding.Documentation, files []commentSourceFile, packages map[string]string, fileSet *token.FileSet) error {
	references := make(map[string]commentTypeReference)
	for _, source := range files {
		imports := make(map[string]string)
		for _, specification := range source.file.Imports {
			path, err := strconv.Unquote(specification.Path.Value)
			if err != nil {
				return fmt.Errorf("%s: 导入路径非法: %w", fileSet.Position(specification.Pos()), err)
			}
			name := packages[path]
			if specification.Name != nil {
				name = specification.Name.Name
			}
			if name != "" {
				imports[name] = path
			}
		}
		for _, declaration := range source.file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, specification := range group.Specs {
				typ := specification.(*ast.TypeSpec)
				if typ.Name.Name == "_" {
					continue
				}
				typeParams := collectAPICommentTypeParams(typ.TypeParams)
				if target := apiCommentTypeReference(typ.Type, source.packagePath, imports, typeParams); target != "" {
					references[source.packagePath+"."+typ.Name.Name] = commentTypeReference{target: target, position: typ.Pos()}
				}
			}
		}
	}
	visiting, complete := make(map[string]bool), make(map[string]bool)
	var resolve func(string) error
	resolve = func(name string) error {
		reference, exists := references[name]
		if !exists || complete[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("%s: 类型 %s 存在循环定义", fileSet.Position(reference.position), name)
		}
		visiting[name] = true
		if err := resolve(reference.target); err != nil {
			return err
		}
		item, parent := docs[name], docs[reference.target]
		if item.Description == "" {
			item.Description = parent.Description
		}
		item.Fields, item.Inline = parent.Fields, parent.Inline
		docs[name] = item
		visiting[name], complete[name] = false, true
		return nil
	}
	for name := range references {
		if err := resolve(name); err != nil {
			return err
		}
	}
	return nil
}

func collectAPICommentTypeParams(params *ast.FieldList) map[string]bool {
	if params == nil || len(params.List) == 0 {
		return nil
	}
	names := make(map[string]bool, len(params.List))
	for _, field := range params.List {
		for _, ident := range field.Names {
			if ident != nil && ident.Name != "" && ident.Name != "_" {
				names[ident.Name] = true
			}
		}
	}
	return names
}

func apiCommentTypeReference(expression ast.Expr, packagePath string, imports map[string]string, typeParams map[string]bool) string {
	switch typ := expression.(type) {
	case *ast.Ident:
		if typeParams[typ.Name] {
			return ""
		}
		return packagePath + "." + typ.Name
	case *ast.SelectorExpr:
		if qualifier, ok := typ.X.(*ast.Ident); ok && !typeParams[qualifier.Name] && imports[qualifier.Name] != "" {
			return imports[qualifier.Name] + "." + typ.Sel.Name
		}
	case *ast.ParenExpr:
		return apiCommentTypeReference(typ.X, packagePath, imports, typeParams)
	case *ast.IndexExpr:
		return apiCommentTypeReference(typ.X, packagePath, imports, typeParams)
	case *ast.IndexListExpr:
		return apiCommentTypeReference(typ.X, packagePath, imports, typeParams)
	}
	return ""
}
