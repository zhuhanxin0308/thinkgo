package command

import (
	"encoding/csv"
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"unicode"

	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
)

// collectOpenAPIComments 保留函数、方法、类型及字段声明的真实符号，错误定位到原始源码行。
func collectOpenAPIComments(source *openapi.SourceComments, fileSet *token.FileSet, file *ast.File, packagePath string) error {
	for _, declaration := range file.Decls {
		switch declared := declaration.(type) {
		case *ast.FuncDecl:
			if declared.Name.Name == "init" || declared.Name.Name == "_" {
				continue
			}
			name := declared.Name.Name
			if declared.Recv != nil {
				receiver := commentTypeName(declared.Recv.List[0].Type)
				if receiver == "" {
					return fmt.Errorf("%s: 无法识别方法接收者", fileSet.Position(declared.Pos()))
				}
				name = receiver + "." + name
			}
			key := packagePath + "." + name
			if _, exists := source.Handlers[key]; exists {
				return fmt.Errorf("%s: 重复处理器声明 %s", fileSet.Position(declared.Pos()), key)
			}
			comments, err := parseAPIHandlerComments(declared.Doc.Text(), declared.Name.Name)
			if err != nil {
				return fmt.Errorf("%s: %w", fileSet.Position(declared.Pos()), err)
			}
			check := openapi.SourceComments{Version: openapi.SourceCommentsVersion, Module: packagePath, Handlers: map[string]openapi.HandlerComments{key: comments}}
			if err := check.Validate(); err != nil {
				return fmt.Errorf("%s: %w", fileSet.Position(declared.Pos()), err)
			}
			source.Handlers[key] = comments
		case *ast.GenDecl:
			if declared.Tok != token.TYPE {
				continue
			}
			for _, specification := range declared.Specs {
				typ := specification.(*ast.TypeSpec)
				if typ.Name.Name == "_" {
					continue
				}
				key := packagePath + "." + typ.Name.Name
				if _, exists := source.Types[key]; exists {
					return fmt.Errorf("%s: 重复类型声明 %s", fileSet.Position(typ.Pos()), key)
				}
				text := typ.Doc.Text()
				if text == "" && len(declared.Specs) == 1 {
					text = declared.Doc.Text()
				}
				item := binding.TypeDocumentation{Description: stripCommentName(text, typ.Name.Name)}
				if structure, ok := typ.Type.(*ast.StructType); ok {
					item = collectAPIStructComments(structure, item.Description, fileSet, file.Comments)
				}
				source.Types[key] = item
			}
		}
	}
	return nil
}

func collectAPIStructComments(structure *ast.StructType, description string, fileSet *token.FileSet, comments []*ast.CommentGroup) binding.TypeDocumentation {
	item := binding.TypeDocumentation{Description: description, Fields: make(map[string]string), Inline: make(map[string]binding.TypeDocumentation)}
	for index, field := range structure.Fields.List {
		text := field.Doc.Text()
		if text == "" {
			text = field.Comment.Text()
		}
		if text == "" {
			limit := structure.Fields.Closing
			if index+1 < len(structure.Fields.List) {
				limit = structure.Fields.List[index+1].Pos()
			}
			position := sort.Search(len(comments), func(index int) bool { return comments[index].Pos() >= field.End() })
			if position < len(comments) {
				comment := comments[position]
				if comment.End() <= limit && fileSet.Position(comment.Pos()).Line == fileSet.Position(field.End()).Line {
					text = comment.Text()
				}
			}
		}
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{{Name: commentTypeName(field.Type)}}
		}
		for _, name := range names {
			if !ast.IsExported(name.Name) {
				continue
			}
			item.Fields[name.Name] = stripCommentName(text, name.Name)
			if child := anonymousAPICommentStruct(field.Type); child != nil {
				item.Inline[name.Name] = collectAPIStructComments(child, "", fileSet, comments)
			}
		}
	}
	return item
}

func anonymousAPICommentStruct(expression ast.Expr) *ast.StructType {
	switch typed := expression.(type) {
	case *ast.StructType:
		return typed
	case *ast.StarExpr:
		return anonymousAPICommentStruct(typed.X)
	case *ast.ParenExpr:
		return anonymousAPICommentStruct(typed.X)
	case *ast.ArrayType:
		return anonymousAPICommentStruct(typed.Elt)
	case *ast.MapType:
		return anonymousAPICommentStruct(typed.Value)
	case *ast.IndexExpr:
		// 泛型容器内的匿名声明保留位置；运行时由真实类型判断是否为 Optional。
		return anonymousAPICommentStruct(typed.Index)
	}
	return nil
}

func commentTypeName(expression ast.Expr) string {
	switch typed := expression.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.StarExpr:
		return commentTypeName(typed.X)
	case *ast.ParenExpr:
		return commentTypeName(typed.X)
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.IndexExpr:
		return commentTypeName(typed.X)
	case *ast.IndexListExpr:
		return commentTypeName(typed.X)
	}
	return ""
}

func stripCommentName(text, name string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, name) {
		remainder := strings.TrimPrefix(text, name)
		if remainder != "" && unicode.IsSpace([]rune(remainder)[0]) {
			return strings.TrimSpace(remainder)
		}
	}
	return text
}

// parseAPIHandlerComments 使用普通首行作为标题，保留后续 Markdown 正文；注解仅补充文档信息。
func parseAPIHandlerComments(text, name string) (openapi.HandlerComments, error) {
	var result openapi.HandlerComments
	plain := make([]string, 0)
	seen := make(map[string]bool)
	var descriptions []string
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@") {
			plain = append(plain, line)
			continue
		}
		fields := strings.Fields(trimmed)
		directive := fields[0]
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, directive))
		if seen[directive] && directive != "@Description" {
			return result, fmt.Errorf("重复注解 %s", directive)
		}
		seen[directive] = true
		switch directive {
		case "@Summary", "@ID", "@Description", "@Tags":
			if value == "" {
				return result, fmt.Errorf("注解 %s 缺少内容", directive)
			}
		case "@Deprecated":
		default:
			return result, fmt.Errorf("不支持注解 %s；路由、参数和响应类型请在真实代码中声明", directive)
		}
		switch directive {
		case "@Summary":
			result.Summary = value
		case "@ID":
			result.OperationID = value
		case "@Description":
			descriptions = append(descriptions, value)
		case "@Deprecated":
			result.Deprecated = true
			if value != "" {
				descriptions = append(descriptions, "已弃用："+value)
			}
		case "@Tags":
			reader := csv.NewReader(strings.NewReader(value))
			reader.TrimLeadingSpace = true
			tags, err := reader.Read()
			if err != nil {
				return result, fmt.Errorf("分组格式错误: %w", err)
			}
			for _, tag := range tags {
				if tag = strings.TrimSpace(tag); tag == "" {
					return result, fmt.Errorf("分组不能为空")
				}
				result.Tags = append(result.Tags, tag)
			}
		}
	}
	paragraphs := strings.Split(stripCommentName(strings.Join(plain, "\n"), name), "\n")
	if result.Summary == "" {
		result.Summary = paragraphs[0]
	}
	description := strings.TrimSpace(strings.Join(paragraphs[1:], "\n"))
	if description != "" {
		descriptions = append([]string{description}, descriptions...)
	}
	result.Description = strings.Join(descriptions, "\n\n")
	return result, nil
}
