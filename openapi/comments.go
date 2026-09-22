package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"io/fs"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
)

// SourceCommentsVersion 标识生成器与运行时共享的元数据格式版本。
const SourceCommentsVersion = 1

// ErrSourceComments 表示元数据格式错误，或处理器声明与已生成内容不一致。
var ErrSourceComments = errors.New("OpenAPI 源码注释无效或已过期")

// HandlerComments 只补充文档语义，不声明路由、参数类型或运行时成功状态。
type HandlerComments struct {
	Summary     string   `json:"summary,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	OperationID string   `json:"operation_id,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
}

// SourceComments 是生成器的可移植产物，运行时不读取源码或修改全局注册状态。
type SourceComments struct {
	Version  int                        `json:"version"`
	Module   string                     `json:"module"`
	Files    map[string]string          `json:"files,omitempty"`
	Handlers map[string]HandlerComments `json:"handlers,omitempty"`
	Types    binding.Documentation      `json:"types,omitempty"`
}

// RegistryOption 在注册表创建期间执行，完成后所有注释元数据保持不可变。
type RegistryOption func(*Registry) error

// ParseSourceComments 严格读取单个生成清单，拒绝未知字段、尾随数据和不兼容版本。
func ParseSourceComments(data []byte) (SourceComments, error) {
	var source SourceComments
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return source, fmt.Errorf("%w: %v", ErrSourceComments, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return source, fmt.Errorf("%w: 清单包含尾随数据", ErrSourceComments)
	}
	return source, source.Validate()
}

// WithSourceComments 配置独立注释来源，复制所有映射与切片以隔离多个应用。
func WithSourceComments(source SourceComments) RegistryOption {
	return func(registry *Registry) error {
		if registry.comments != nil {
			return fmt.Errorf("%w: 不能重复配置注释来源", ErrSourceComments)
		}
		if err := source.Validate(); err != nil {
			return err
		}
		cloned, err := cloneJSON(source)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrSourceComments, err)
		}
		registry.comments = &cloned
		return nil
	}
}

// Validate 检查注释清单的版本、符号归属和字段名称，供生成器与运行时共享。
func (source SourceComments) Validate() error {
	if source.Version != SourceCommentsVersion || source.Module == "" || strings.ContainsAny(source.Module, " \\:\t\r\n\x00") || !fs.ValidPath(source.Module) {
		return fmt.Errorf("%w: 注释版本或模块路径非法", ErrSourceComments)
	}
	belongs := func(symbol string) bool {
		return strings.HasPrefix(symbol, source.Module+".") || strings.HasPrefix(symbol, source.Module+"/")
	}
	for name, item := range source.Handlers {
		if !belongs(name) || strings.ContainsAny(name, " \t\r\n\x00") || !strings.Contains(name, ".") {
			return fmt.Errorf("%w: 处理器符号 %q 非法", ErrSourceComments, name)
		}
		if item.OperationID != "" && (len(item.OperationID) > maximumOperationID || !operationIDPattern.MatchString(item.OperationID)) {
			return fmt.Errorf("%w: %s 的操作标识非法", ErrSourceComments, name)
		}
		for _, tag := range item.Tags {
			if strings.TrimSpace(tag) == "" {
				return fmt.Errorf("%w: %s 包含空分组", ErrSourceComments, name)
			}
		}
	}
	for name, item := range source.Types {
		if !belongs(name) || strings.ContainsAny(name, " \t\r\n\x00") {
			return fmt.Errorf("%w: 类型符号 %q 非法", ErrSourceComments, name)
		}
		if err := validateTypeComments(item, 0); err != nil {
			return err
		}
	}
	for name := range source.Files {
		if !fs.ValidPath(name) || strings.ContainsAny(name, "\\:\x00") {
			return fmt.Errorf("%w: 源码相对路径 %q 非法", ErrSourceComments, name)
		}
	}
	return nil
}

const maximumCommentDepth = 64

func validateTypeComments(item binding.TypeDocumentation, depth int) error {
	if depth > maximumCommentDepth {
		return fmt.Errorf("%w: 匿名字段说明嵌套过深", ErrSourceComments)
	}
	for field := range item.Fields {
		if !token.IsIdentifier(field) {
			return fmt.Errorf("%w: 字段符号 %q 非法", ErrSourceComments, field)
		}
	}
	for field, child := range item.Inline {
		if !token.IsIdentifier(field) {
			return fmt.Errorf("%w: 匿名字段符号 %q 非法", ErrSourceComments, field)
		}
		if err := validateTypeComments(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
