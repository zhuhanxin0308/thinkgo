package openapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"runtime"
	"strings"
)

const commentOperationDigestBytes = 8

// sourceHandlerName 统一函数、绑定方法与泛型实例的编译符号，保持完整包路径以避免同名冲突。
func sourceHandlerName(callback any) string {
	value := reflect.ValueOf(callback)
	if !value.IsValid() || value.Kind() != reflect.Func || value.IsNil() {
		return ""
	}
	function := runtime.FuncForPC(value.Pointer())
	if function == nil {
		return ""
	}
	name := strings.TrimSuffix(function.Name(), "-fm")
	var canonical strings.Builder
	depth := 0
	for _, character := range name {
		switch character {
		case '[':
			depth++
		case ']':
			depth--
		default:
			if depth == 0 && character != '(' && character != ')' && character != '*' {
				canonical.WriteRune(character)
			}
		}
	}
	return canonical.String()
}

func (registry *Registry) commentedOperation(metadata Operation, callback any) (Operation, string, error) {
	if registry.comments == nil {
		return metadata, "", nil
	}
	name := sourceHandlerName(callback)
	comments, exists := registry.comments.Handlers[name]
	if !exists {
		// 匿名回调与外部处理器仍可显式描述；自动模式必须具有可稳定定位的源码声明。
		if metadata.OperationID != "" && metadata.Summary != "" {
			return metadata, "", nil
		}
		return metadata, "", fmt.Errorf("%w: 找不到处理器 %q；请在当前模块的业务包中定义具名处理器并运行 openapi:generate，或显式声明 OperationID 和 Summary", ErrSourceComments, name)
	}
	if metadata.OperationID == "" {
		metadata.OperationID = comments.OperationID
	}
	if metadata.Summary == "" {
		metadata.Summary = comments.Summary
	}
	if metadata.Description == "" {
		metadata.Description = comments.Description
	}
	if metadata.Tags == nil {
		metadata.Tags = comments.Tags
	}
	metadata.Deprecated = metadata.Deprecated || comments.Deprecated
	return metadata, name, nil
}

func automaticOperationID(symbol, method, path string) string {
	// 路径使用最终分组结果，同一函数用于多个路由时仍产生稳定且不同的标识。
	digest := sha256.Sum256([]byte(symbol + "\n" + method + "\n" + path))
	return strings.ToLower(method) + "_" + hex.EncodeToString(digest[:commentOperationDigestBytes])
}
