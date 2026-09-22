package openapi

import (
	"encoding/json"
	"reflect"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
)

// RegisterTyped 使用请求与返回类型注册完整接口契约，字段来源和约束与绑定器一致。
// 同名组件仅在内容相同时复用；任何冲突均在发布注册变更前返回。
func RegisterTyped[Input, Output any](registry *Registry, method, path, operationID string, successStatus int) error {
	var docs binding.Documentation
	if registry != nil && registry.comments != nil {
		docs = registry.comments.Types
	}
	operation, schemas, err := documentedOperation(reflect.TypeFor[Input](), reflect.TypeFor[Output](), operationID, successStatus, docs)
	if err != nil {
		return err
	}
	return registry.register(method, path, operation, schemas)
}

// typedOperation 将契约快照转换为文档库类型，避免运行时请求绑定依赖文档加载器。
func typedOperation(input, output reflect.Type, operationID string, successStatus int) (*openapi3.Operation, openapi3.Schemas, error) {
	return documentedOperation(input, output, operationID, successStatus, nil)
}

func documentedOperation(input, output reflect.Type, operationID string, successStatus int, docs binding.Documentation) (*openapi3.Operation, openapi3.Schemas, error) {
	operationJSON, schemasJSON, err := binding.OperationWithDocumentation(input, output, operationID, successStatus, docs)
	if err != nil {
		return nil, nil, err
	}
	var operation openapi3.Operation
	var schemas openapi3.Schemas
	if err := json.Unmarshal(operationJSON, &operation); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(schemasJSON, &schemas); err != nil {
		return nil, nil, err
	}
	return &operation, schemas, nil
}
