package command

import (
	"bytes"
	"go/format"
	"strconv"
	"strings"
	"text/template"
)

func renderCRUDSource(plan *crudPlan) ([]byte, error) {
	return renderCRUDTemplate(crudSourceTemplate, plan)
}

func renderCRUDTestSource(plan *crudPlan) ([]byte, error) {
	return renderCRUDTemplate(crudTestTemplate, plan)
}

// renderCRUDTemplate 将所有用户字符串作为 Go 字符串或标签编码，避免拼接成可执行代码。
func renderCRUDTemplate(source string, plan *crudPlan) ([]byte, error) {
	functions := template.FuncMap{
		"q": strconv.Quote,
		"tag": func(value string) string {
			if strings.ContainsRune(value, '`') {
				return strconv.Quote(value)
			}
			return "`" + value + "`"
		},
		"columnTag": func(field crudField) string { return "thinkgo:" + strconv.Quote(field.Column) },
		"outputTag": func(field crudField) string {
			tag := "json:" + strconv.Quote(field.JSON) + " thinkgo:" + strconv.Quote(field.Column+",readonly")
			if field.description != "" {
				tag += " doc:" + strconv.Quote(field.description)
			}
			return tag
		},
		"columns": func(fields []crudField) string {
			columns := make([]string, len(fields))
			for index, field := range fields {
				columns[index] = field.Column
			}
			return strings.Join(columns, ",")
		},
	}
	compiled, err := template.New("crud").Funcs(functions).Parse(source)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := compiled.Execute(&output, plan); err != nil {
		return nil, err
	}
	return format.Source(output.Bytes())
}

const crudSourceTemplate = `package api

import (
	"net/http"
	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
	model {{q .ModelImport}}
	{{range .Imports}}{{.Alias}} {{q .Path}}
	{{end}}
)

const {{.Name}}ResourcePath = {{q .Path}}

// {{.Name}}CreateInput 创建{{.Name}}的请求参数。
type {{.Name}}CreateInput struct {
	binding.Input
	{{range .Write}}{{.InputName}} {{.Type}} {{tag .CreateTag}}
	{{end}}
}

// {{.Name}}KeyInput {{.Name}}的资源标识。
type {{.Name}}KeyInput struct {
	binding.Input
	{{.KeyInput}} {{.Key.Type}} {{tag (printf "path:%q json:%q validate:%q" "id" "-" "required")}}
}

// {{.Name}}UpdateInput 更新{{.Name}}的请求参数，仅修改本次提交的字段。
type {{.Name}}UpdateInput struct {
	binding.Input
	{{.KeyInput}} {{.Key.Type}} {{tag (printf "path:%q json:%q validate:%q" "id" "-" "required")}}
	{{range .Update}}{{.InputName}} binding.Optional[{{.Type}}] {{tag .UpdateTag}}
	{{end}}
}

// {{.Name}}ListInput 查询{{.Name}}列表的分页参数。
type {{.Name}}ListInput struct {
	binding.Input
	Page int {{tag (printf "query:%q default:%q validate:%q" "page" "1" (printf "between:1,%d" .MaxPage))}}
	PageSize int {{tag (printf "query:%q default:%q validate:%q" "page_size" (printf "%d" .PageSize) (printf "between:1,%d" .MaxPageSize))}}
}

// {{.Name}}Output {{.Name}}的公开资料。
type {{.Name}}Output struct {
	{{range .Read}}{{.Name}} {{.Type}} {{tag (outputTag .)}}
	{{end}}
}

// {{.Name}}CreateRecord 仅提交白名单列，让其他列保留数据库默认值及模型事件行为。
type {{.Name}}CreateRecord struct {
	{{range .Create}}{{.Name}} {{.Type}} {{tag (columnTag .)}}
	{{end}}
}

// Register{{.Name}}Routes 注册完整 CRUD 与文档，可传入应用路由门面或分组以复用鉴权中间件。
func Register{{.Name}}Routes(router openapi.RouteRegistrar, registry *openapi.Registry) error {
	operations := []struct {
		method, path, id string
		status int
		handler any
	}{
		{http.MethodGet, {{.Name}}ResourcePath, {{q (printf "%s.list" .Name)}}, http.StatusOK, List{{.Name}}},
		{http.MethodPost, {{.Name}}ResourcePath, {{q (printf "%s.create" .Name)}}, http.StatusCreated, Create{{.Name}}},
		{http.MethodGet, {{.Name}}ResourcePath + "/:id", {{q (printf "%s.read" .Name)}}, http.StatusOK, Read{{.Name}}},
		{http.MethodPatch, {{.Name}}ResourcePath + "/:id", {{q (printf "%s.update" .Name)}}, http.StatusOK, Update{{.Name}}},
		{http.MethodDelete, {{.Name}}ResourcePath + "/:id", {{q (printf "%s.delete" .Name)}}, http.StatusNoContent, Delete{{.Name}}},
	}
	for _, operation := range operations {
		if err := openapi.Handle(router, registry, openapi.Operation{
			Method: operation.method, Path: operation.path, OperationID: operation.id,
			SuccessStatus: operation.status, Tags: []string{ {{q .Name}} },
		}, operation.handler); err != nil {
			return err
		}
	}
	return nil
}

// List{{.Name}} 分页查询{{.Name}}。
func List{{.Name}}(input {{.Name}}ListInput, recordModel *model.{{.Name}}) (*db.Paginator[{{.Name}}Output], error) {
	return db.Paginate[{{.Name}}Output](recordModel.Field({{q (columns .Read)}}).Order({{q .Key.Column}}), input.Page, input.PageSize)
}

// Read{{.Name}} 查询{{.Name}}详情。
//
// 资源不存在时返回 404。
func Read{{.Name}}(input {{.Name}}KeyInput, recordModel *model.{{.Name}}) ({{.Name}}Output, error) {
	record, err := find{{.Name}}(recordModel, input.{{.KeyInput}})
	if err != nil { return {{.Name}}Output{}, err }
	return output{{.Name}}(record), nil
}

// Create{{.Name}} 创建{{.Name}}。
func Create{{.Name}}(input {{.Name}}CreateInput, request *framework.Request, app *framework.App) ({{.Name}}Output, error) {
	var output {{.Name}}Output
	err := transaction{{.Name}}(app, request, func(recordModel *model.{{.Name}}) error {
		data := {{.Name}}CreateRecord{
			{{range .Write}}{{.Name}}: input.{{.InputName}},
			{{end}}
		}
		if err := recordModel.Create(&data); err != nil { return err }
		record, err := find{{.Name}}(recordModel, data.{{.Key.Name}})
		if err != nil { return err }
		output = output{{.Name}}(record)
		return nil
	})
	if err != nil { return {{.Name}}Output{}, err }
	return output, nil
}

// Update{{.Name}} 更新{{.Name}}。
//
// 仅修改已提交字段，资源不存在时返回 404。
func Update{{.Name}}(input {{.Name}}UpdateInput, request *framework.Request, app *framework.App) ({{.Name}}Output, error) {
	if {{range $index, $field := .Update}}{{if $index}} && {{end}}!input.{{.InputName}}.IsSet(){{end}} {
		return {{.Name}}Output{}, exception.NewHttpException(http.StatusUnprocessableEntity, "至少提交一个更新字段")
	}
	{{range .Update}}{{if not .Nullable}}if input.{{.InputName}}.IsNull() {
		return {{$.Name}}Output{}, exception.NewHttpException(http.StatusUnprocessableEntity, {{q (printf "字段 %s 不能清空" .Name)}})
	}
	{{end}}{{end}}
	var output {{.Name}}Output
	err := transaction{{.Name}}(app, request, func(recordModel *model.{{.Name}}) error {
		record, err := find{{.Name}}(recordModel, input.{{.KeyInput}})
		if err != nil { return err }
		{{range .Update}}if input.{{.InputName}}.IsSet() {
			{{if .Nullable}}if input.{{.InputName}}.IsNull() { record.{{.Name}} = nil } else { record.{{.Name}} = input.{{.InputName}}.Value() }{{else}}record.{{.Name}} = input.{{.InputName}}.Value(){{end}}
		}
		{{end}}
		if err := record.Save(); err != nil { return err }
		record, err = find{{.Name}}(recordModel, input.{{.KeyInput}})
		if err != nil { return err }
		output = output{{.Name}}(record)
		return nil
	})
	if err != nil { return {{.Name}}Output{}, err }
	return output, nil
}

// Delete{{.Name}} 删除{{.Name}}。
//
// 成功返回 204，资源不存在时返回 404。
func Delete{{.Name}}(input {{.Name}}KeyInput, request *framework.Request, app *framework.App) error {
	return transaction{{.Name}}(app, request, func(recordModel *model.{{.Name}}) error {
		record, err := find{{.Name}}(recordModel, input.{{.KeyInput}})
		if err != nil { return err }
		return record.Delete()
	})
}

func find{{.Name}}(recordModel *model.{{.Name}}, key {{.Key.Type}}) (*model.{{.Name}}, error) {
	var record model.{{.Name}}
	found, err := recordModel.Where({{q .Key.Column}}, key).Find(&record)
	if err != nil { return nil, err }
	if !found { return nil, exception.NewHttpException(http.StatusNotFound, "资源不存在") }
	return &record, nil
}

func output{{.Name}}(record *model.{{.Name}}) {{.Name}}Output {
	return {{.Name}}Output{
		{{range .Read}}{{.Name}}: record.{{.Name}},
		{{end}}
	}
}

// transaction{{.Name}} 使用应用默认数据库与请求上下文，任何步骤失败都会回滚。
func transaction{{.Name}}(app *framework.App, request *framework.Request, work func(*model.{{.Name}}) error) error {
	database := app.DB()
	if database == nil { return db.ErrDatabaseUnavailable }
	return database.TransactionContext(request.Context(), func(tx *db.Tx) error {
		recordModel, err := framework.ResolveModel[*model.{{.Name}}](request, db.WithModelTransaction(tx))
		if err != nil { return err }
		return work(recordModel)
	})
}
`

const crudTestTemplate = `package api

import (
	"context"
	"testing"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// Test{{.Name}}Routes 验证模型字段演进后，请求绑定、路由与响应文档仍可以完整注册。
func Test{{.Name}}Routes(t *testing.T) {
	registry, err := openapi.NewRegistry(openapi3.Info{Title: {{q .Name}}, Version: "1"})
	if err != nil { t.Fatal(err) }
	router := route.NewRouter()
	if err := Register{{.Name}}Routes(router, registry); err != nil { t.Fatal(err) }
	if err := registry.ValidateRouter(router, {{.Name}}ResourcePath); err != nil { t.Fatal(err) }
	if _, err := registry.JSON(context.Background()); err != nil { t.Fatal(err) }
}
`
