package command

import (
	"fmt"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/console"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

const (
	crudDefaultKey  = "ID"
	crudMaxPage     = 10000
	crudMaxPageSize = 100
)

type crudField struct {
	Name        string
	InputName   string
	Column      string
	Type        string
	JSON        string
	CreateTag   string
	UpdateTag   string
	Nullable    bool
	ReadOnly    bool
	goType      types.Type
	validation  string
	defaultTag  string
	description string
	owner       string
}

type crudImport struct{ Alias, Path string }

type crudPlan struct {
	Name        string
	FileName    string
	Path        string
	ModelImport string
	KeyInput    string
	Key         crudField
	Write       []crudField
	Update      []crudField
	Read        []crudField
	Create      []crudField
	Imports     []crudImport
	PageSize    int
	MaxPage     int
	MaxPageSize int
	semantic    *applicationSemanticContext
}

// buildCRUDPlan 通过现有 go/types 上下文解析模型，保留类型身份、构建约束和字段标签。
func buildCRUDPlan(basePath string, target generatorTarget, input *console.Input) (*crudPlan, error) {
	path := strings.TrimSpace(input.GetOption("path"))
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, ":{}*?%#\\\r\n\x00") || strings.Contains(path, "//") {
		return nil, fmt.Errorf("CRUD --path 必须是没有变量的明确资源路径")
	}
	path = strings.TrimSuffix(path, "/")
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return nil, fmt.Errorf("CRUD 路径不能包含点段")
		}
	}
	if path == "" || input.GetOption("write") == "" || input.GetOption("read") == "" {
		return nil, fmt.Errorf("CRUD 必须显式声明 --path、--write 和 --read")
	}
	validationRouter := route.NewRouter()
	for _, candidate := range []string{path, path + "/:id"} {
		if _, err := validationRouter.Get(candidate, func() {}); err != nil {
			return nil, err
		}
	}
	semantic, err := newApplicationSemanticContextFromModule(basePath)
	if err != nil {
		return nil, err
	}
	modelDirectory := filepath.Join("app", target.application, "model")
	modelImport := semantic.packagePath(modelDirectory)
	modelPackage, err := semantic.Import(modelImport)
	if err != nil {
		return nil, err
	}
	object, ok := modelPackage.Scope().Lookup(target.name).(*types.TypeName)
	if !ok {
		return nil, fmt.Errorf("模型类型 %s 不存在", target.name)
	}
	basePackage, err := semantic.Import(frameworkImportPath + "/db")
	if err != nil {
		return nil, err
	}
	fields, err := collectCRUDFields(object.Type(), basePackage.Scope().Lookup("Model").Type())
	if err != nil {
		return nil, err
	}
	comments, err := scanOpenAPIComments(input.Context(), basePath)
	if err != nil {
		return nil, err
	}
	for name, field := range fields {
		if field.description == "" {
			field.description = comments.Types[field.owner].Fields[name]
			fields[name] = field
		}
	}
	keyName := strings.TrimSpace(input.GetOption("key"))
	if keyName == "" {
		keyName = crudDefaultKey
	}
	key, exists := fields[keyName]
	if !exists || key.ReadOnly {
		return nil, fmt.Errorf("模型主键字段 %q 不存在或不可写", keyName)
	}
	basic, ok := key.goType.Underlying().(*types.Basic)
	if !ok || basic.Info()&(types.IsInteger|types.IsString) == 0 {
		return nil, fmt.Errorf("CRUD 主键必须是整数或字符串")
	}
	write, err := selectCRUDFields(fields, input.GetOption("write"), true)
	if err != nil {
		return nil, err
	}
	read, err := selectCRUDFields(fields, input.GetOption("read"), false)
	if err != nil {
		return nil, err
	}
	plan := &crudPlan{Name: target.name, FileName: db.ToSnakeCase(target.name), Path: path, ModelImport: modelImport,
		Key: key, Write: write, Read: read, PageSize: db.DefaultPageSize, MaxPage: crudMaxPage, MaxPageSize: crudMaxPageSize, semantic: semantic}
	used := map[string]bool{"Input": true}
	for _, field := range write {
		used[field.Name] = true
	}
	plan.KeyInput = uniqueCRUDName("ResourceKey", used)
	for index := range plan.Write {
		field := &plan.Write[index]
		field.InputName = field.Name
		if field.Name == "Input" {
			field.InputName = uniqueCRUDName("InputValue", used)
		}
		if field.Name != keyName {
			plan.Update = append(plan.Update, *field)
		}
	}
	if len(plan.Update) == 0 {
		return nil, fmt.Errorf("CRUD --write 至少包含一个非主键字段")
	}
	plan.Create = append([]crudField(nil), plan.Write...)
	hasKey := false
	for _, field := range plan.Create {
		hasKey = hasKey || field.Name == keyName
	}
	if !hasKey {
		plan.Create = append([]crudField{key}, plan.Create...)
	}
	if err := prepareCRUDTypes(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// collectCRUDFields 复用 ORM 的显式列名和匿名嵌入规则，拒绝重复或不可识别的模型声明。
func collectCRUDFields(modelType, baseType types.Type) (map[string]crudField, error) {
	fields := make(map[string]crudField)
	columns := make(map[string]bool)
	visiting := make(map[types.Type]bool)
	models := 0
	var walk func(types.Type) error
	walk = func(current types.Type) error {
		if visiting[current] {
			return fmt.Errorf("模型包含循环匿名嵌入")
		}
		visiting[current] = true
		defer delete(visiting, current)
		structure, ok := current.Underlying().(*types.Struct)
		if !ok {
			return fmt.Errorf("模型必须是结构体")
		}
		owner := ""
		if named, ok := types.Unalias(current).(*types.Named); ok && named.Obj().Pkg() != nil {
			owner = named.Obj().Pkg().Path() + "." + named.Obj().Name()
		}
		for index := 0; index < structure.NumFields(); index++ {
			field := structure.Field(index)
			if !field.Exported() {
				continue
			}
			underlying := types.Unalias(field.Type())
			pointer, isPointer := underlying.(*types.Pointer)
			if isPointer {
				underlying = types.Unalias(pointer.Elem())
			}
			if types.Identical(underlying, baseType) {
				if !isPointer || !field.Embedded() {
					return fmt.Errorf("模型必须匿名嵌入 *db.Model")
				}
				models++
				continue
			}
			tag := reflect.StructTag(structure.Tag(index))
			parts := strings.Split(tag.Get("thinkgo"), ",")
			column := parts[0]
			if column == "-" {
				continue
			}
			readOnly := false
			for _, option := range parts[1:] {
				if option != "omitempty" && option != "readonly" {
					return fmt.Errorf("字段 %s 使用未知 thinkgo 选项 %q", field.Name(), option)
				}
				readOnly = readOnly || option == "readonly"
			}
			if _, ok := underlying.Underlying().(*types.Struct); ok && field.Embedded() && column == "" {
				if named, ok := underlying.(*types.Named); !ok || named.Obj().Pkg().Path() != "time" {
					if len(parts) != 1 {
						return fmt.Errorf("匿名模型字段不能声明列选项")
					}
					if err := walk(underlying); err != nil {
						return err
					}
					continue
				}
			}
			if column == "" {
				column = db.ToSnakeCase(field.Name())
			}
			if !crudColumnIdentifier(column) || columns[column] {
				return fmt.Errorf("模型列 %q 非法或重复", column)
			}
			if _, exists := fields[field.Name()]; exists {
				return fmt.Errorf("模型字段 %q 存在提升歧义", field.Name())
			}
			jsonTag := tag.Get("json")
			if jsonTag == "" || strings.HasPrefix(jsonTag, ",") {
				jsonTag = column + jsonTag
			}
			nullable := isPointer
			switch underlying.Underlying().(type) {
			case *types.Map, *types.Slice:
				nullable = true
			}
			fields[field.Name()] = crudField{Name: field.Name(), Column: column, JSON: jsonTag, Nullable: nullable,
				ReadOnly: readOnly, goType: field.Type(), validation: tag.Get("validate"), defaultTag: tag.Get("default"), description: tag.Get("doc"), owner: owner}
			columns[column] = true
		}
		return nil
	}
	if err := walk(modelType); err != nil {
		return nil, err
	}
	if models != 1 {
		return nil, fmt.Errorf("模型必须且只能嵌入一个 *db.Model")
	}
	return fields, nil
}

func crudColumnIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (index == 0 || character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func selectCRUDFields(available map[string]crudField, selection string, writable bool) ([]crudField, error) {
	seen, names := make(map[string]bool), make(map[string]bool)
	selected := make([]crudField, 0)
	for _, name := range strings.Split(selection, ",") {
		name = strings.TrimSpace(name)
		field, exists := available[name]
		jsonName := strings.Split(field.JSON, ",")[0]
		if !exists || seen[name] || names[jsonName] || jsonName == "-" || writable && field.ReadOnly {
			return nil, fmt.Errorf("字段 %q 不存在、重复、隐藏或不可写", name)
		}
		seen[name], names[jsonName] = true, true
		selected = append(selected, field)
	}
	return selected, nil
}

func uniqueCRUDName(base string, used map[string]bool) string {
	name := base
	for index := 1; used[name]; index++ {
		name = base + strconv.Itoa(index)
	}
	used[name] = true
	return name
}

// prepareCRUDTypes 使用导入路径分配稳定别名，保留自定义类型、别名和泛型实例。
func prepareCRUDTypes(plan *crudPlan) error {
	imports := map[string]string{plan.ModelImport: "model", frameworkImportPath + "/db": "db", frameworkImportPath + "/binding": "binding"}
	qualifier := func(pkg *types.Package) string {
		if name, exists := imports[pkg.Path()]; exists {
			return name
		}
		name := "fieldtype" + strconv.Itoa(len(imports))
		imports[pkg.Path()] = name
		return name
	}
	prepare := func(field *crudField) error {
		if err := validateCRUDFieldType(field.goType, make(map[types.Type]bool)); err != nil {
			return fmt.Errorf("字段 %s: %w", field.Name, err)
		}
		field.Type = types.TypeString(field.goType, qualifier)
		jsonTag := strings.Split(field.JSON, ",")[0]
		if strings.Contains(","+field.JSON+",", ",string,") {
			jsonTag += ",string"
		}
		field.CreateTag = "json:" + strconv.Quote(jsonTag)
		field.UpdateTag = field.CreateTag
		if field.validation != "" {
			field.CreateTag += " validate:" + strconv.Quote(field.validation)
			rules := make([]string, 0)
			for _, rule := range strings.Split(field.validation, "|") {
				if strings.TrimSpace(rule) != "required" {
					rules = append(rules, rule)
				}
			}
			if len(rules) > 0 {
				field.UpdateTag += " validate:" + strconv.Quote(strings.Join(rules, "|"))
			}
		}
		if field.defaultTag != "" {
			field.CreateTag += " default:" + strconv.Quote(field.defaultTag)
		}
		if field.description != "" {
			field.CreateTag += " doc:" + strconv.Quote(field.description)
			field.UpdateTag += " doc:" + strconv.Quote(field.description)
		}
		return nil
	}
	if err := prepare(&plan.Key); err != nil {
		return err
	}
	for _, fields := range [][]crudField{plan.Write, plan.Update, plan.Read, plan.Create} {
		for index := range fields {
			if err := prepare(&fields[index]); err != nil {
				return err
			}
		}
	}
	for path, alias := range imports {
		if path != plan.ModelImport && path != frameworkImportPath+"/db" && path != frameworkImportPath+"/binding" {
			plan.Imports = append(plan.Imports, crudImport{Alias: alias, Path: path})
		}
	}
	sort.Slice(plan.Imports, func(left, right int) bool { return plan.Imports[left].Path < plan.Imports[right].Path })
	return nil
}

func validateCRUDFieldType(typ types.Type, visiting map[types.Type]bool) error {
	if visiting[typ] {
		return nil
	}
	visiting[typ] = true
	switch value := typ.(type) {
	case *types.Alias:
		if value.Obj().Pkg() != nil && !value.Obj().Exported() {
			return fmt.Errorf("字段引用了不可导出的类型 %s", value.Obj().Name())
		}
		return validateCRUDFieldType(types.Unalias(value), visiting)
	case *types.Named:
		if !value.Obj().Exported() {
			return fmt.Errorf("字段引用了不可导出的类型 %s", value.Obj().Name())
		}
		for index := 0; index < value.TypeArgs().Len(); index++ {
			if err := validateCRUDFieldType(value.TypeArgs().At(index), visiting); err != nil {
				return err
			}
		}
		if _, ok := value.Underlying().(*types.Struct); ok {
			return nil
		}
		return validateCRUDFieldType(value.Underlying(), visiting)
	case *types.Pointer:
		return validateCRUDFieldType(value.Elem(), visiting)
	case *types.Slice:
		return validateCRUDFieldType(value.Elem(), visiting)
	case *types.Array:
		return validateCRUDFieldType(value.Elem(), visiting)
	case *types.Map:
		key, ok := value.Key().Underlying().(*types.Basic)
		if !ok || key.Info()&types.IsString == 0 {
			return fmt.Errorf("JSON 映射键必须是字符串")
		}
		return validateCRUDFieldType(value.Elem(), visiting)
	case *types.Basic:
		if value.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) != 0 {
			return nil
		}
	}
	return fmt.Errorf("字段类型 %s 不能直接生成请求契约", typ)
}
