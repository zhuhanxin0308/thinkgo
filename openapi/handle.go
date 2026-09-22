package openapi

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/middleware"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

// Operation 声明接口元信息；请求字段与响应结构始终从真实处理器推导。
// SuccessStatus 为零时，数据返回默认 200，空返回默认 204。
type Operation struct {
	Method        string
	Path          string
	OperationID   string
	SuccessStatus int
	Summary       string
	Description   string
	Tags          []string
	Deprecated    bool
	Security      *openapi3.SecurityRequirements
}

// RouteRegistrar 由 Router 和 Group 实现，关联契约与路由在同一次注册中提交。
type RouteRegistrar interface {
	AddWithCommit(string, string, route.HandlerFunc, func(route.RouteInfo) error, ...middleware.Handler) (*route.Route, error)
}

// Handle 同时注册实际处理器和 OpenAPI 契约，保留请求、服务和模型自动注入。
// 处理器应返回具体 DTO、(DTO, error)、error 或无返回值；普通原始响应继续使用 Router.Add。
func Handle(target RouteRegistrar, registry *Registry, metadata Operation, callback any, handlers ...middleware.Handler) error {
	if target == nil {
		return ErrInvalidOperation
	}
	if registry == nil || registry.document == nil {
		return ErrInvalidDocument
	}
	method, err := normalizeMethod(metadata.Method)
	if err != nil {
		return err
	}
	// 框架路由只把 :name 视为变量，禁止把文档花括号误注册成静态地址。
	if strings.ContainsAny(metadata.Path, "{}") {
		return fmt.Errorf("%w: 实际路由参数请使用 :name", ErrUnsupportedRoute)
	}
	handler, err := route.NewJSONHandler(callback, metadata.SuccessStatus)
	if err != nil {
		return err
	}
	if method == http.MethodHead && handler.OutputType() != nil {
		return fmt.Errorf("%w: HEAD 接口不能声明响应体", ErrInvalidOperation)
	}
	input, err := callbackInput(reflect.TypeOf(callback))
	if err != nil {
		return err
	}
	metadata, symbol, err := registry.commentedOperation(metadata, callback)
	if err != nil {
		return err
	}
	_, err = target.AddWithCommit(method, metadata.Path, handler, func(info route.RouteInfo) error {
		if info.Domain != "" || strings.ContainsAny(info.Path, "{}") {
			return fmt.Errorf("%w: 域名分组或花括号路径未声明契约", ErrUnsupportedRoute)
		}
		path, err := normalizePath(info.Path)
		if err != nil {
			return err
		}
		if metadata.OperationID == "" && symbol != "" {
			metadata.OperationID = automaticOperationID(symbol, info.Method, path)
		}
		var docs binding.Documentation
		if registry.comments != nil {
			docs = registry.comments.Types
		}
		operation, schemas, err := documentedOperation(input, handler.OutputType(), metadata.OperationID, handler.SuccessStatus(), docs)
		if err != nil {
			return err
		}
		operation.Summary, operation.Description = metadata.Summary, metadata.Description
		operation.Tags = append([]string(nil), metadata.Tags...)
		operation.Deprecated, operation.Security = metadata.Deprecated, metadata.Security
		// 字符串结果同样输出 JSON，保持实际响应与文档一致。
		response := operation.Responses.Value(strconv.Itoa(handler.SuccessStatus())).Value
		if text, exists := response.Content["text/plain"]; exists {
			delete(response.Content, "text/plain")
			response.Content["application/json"] = text
		}
		if err := validateOperationPath(path, operation); err != nil {
			return err
		}
		return registry.register(info.Method, path, operation, schemas)
	}, handlers...)
	return err
}

// callbackInput 只从显式 Input 推导请求，未声明请求参数时使用空输入计划。
// 独立标量参数缺少稳定的字段名契约，必须归入带来源标签的 Input。
func callbackInput(signature reflect.Type) (reflect.Type, error) {
	plan, err := binding.CompileCall(signature, false)
	if err != nil {
		return nil, err
	}
	for _, argument := range plan.Arguments {
		if argument.Kind == binding.ArgumentValue {
			return nil, fmt.Errorf("%w: 参数 %s 必须通过 binding.Input 声明来源", ErrInvalidOperation, argument.Type)
		}
	}
	return plan.Input, nil
}

// validateOperationPath 在任何状态发布前核对路径变量，避免启动导出才发现契约错配。
func validateOperationPath(path string, operation *openapi3.Operation) error {
	variables := make(map[string]bool)
	for _, segment := range strings.Split(path, "/") {
		if strings.HasPrefix(segment, "{") {
			variables[strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")] = true
		}
	}
	for _, parameter := range operation.Parameters {
		if parameter.Value.In != "path" {
			continue
		}
		if !variables[parameter.Value.Name] {
			return fmt.Errorf("%w: 请求路径字段 %q 不在路由 %s 中", ErrInvalidOperation, parameter.Value.Name, path)
		}
		delete(variables, parameter.Value.Name)
	}
	if len(variables) != 0 {
		return fmt.Errorf("%w: 路由 %s 存在未声明的路径字段", ErrInvalidOperation, path)
	}
	return nil
}
