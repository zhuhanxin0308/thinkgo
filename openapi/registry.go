// Package openapi 提供经过完整 OpenAPI 校验、可与实际路由双向核对的接口契约注册表。
package openapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

const (
	openAPIVersion        = "3.1.0"
	maximumContractPath   = 2048
	maximumOperationID    = 128
	maximumComponentName  = 128
	maximumContractRoutes = 100_000
)

var (
	ErrInvalidDocument      = errors.New("OpenAPI 文档非法")
	ErrInvalidOperation     = errors.New("OpenAPI 操作非法")
	ErrDuplicateOperation   = errors.New("OpenAPI 操作重复")
	ErrDuplicateOperationID = errors.New("OpenAPI operationId 重复")
	ErrRegistryFrozen       = errors.New("OpenAPI 注册表已冻结")
	ErrUnsupportedRoute     = errors.New("OpenAPI 不支持该路由结构")
	ErrRouteCoverage        = errors.New("OpenAPI 路由覆盖不一致")
)

var (
	pathParameterPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	operationIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	componentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// Registry 在启动期收集契约，首次导出后冻结为不可变 JSON 快照。
type Registry struct {
	mu           sync.RWMutex
	document     *openapi3.T
	operationIDs map[string]string
	operations   map[string]struct{}
	frozen       bool
	snapshot     []byte
	comments     *SourceComments
}

// NewRegistry 创建 OpenAPI 3.1 注册表并立即校验文档元信息。
func NewRegistry(info openapi3.Info, options ...RegistryOption) (*Registry, error) {
	cloned, err := cloneJSON(info)
	if err != nil {
		return nil, fmt.Errorf("%w: 复制 Info 失败: %v", ErrInvalidDocument, err)
	}
	if err = cloned.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	components := openapi3.NewComponents()
	components.Schemas = make(openapi3.Schemas)
	components.SecuritySchemes = make(openapi3.SecuritySchemes)
	registry := &Registry{
		document: &openapi3.T{
			OpenAPI:    openAPIVersion,
			Info:       &cloned,
			Paths:      openapi3.NewPaths(),
			Components: &components,
		},
		operationIDs: make(map[string]string),
		operations:   make(map[string]struct{}),
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("%w: 注册选项不能为空", ErrInvalidDocument)
		}
		if err := option(registry); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// Register 注册一个方法和路径契约；ThinkGo 的 :name 参数会转换为 {name}。
func (registry *Registry) Register(method, path string, operation *openapi3.Operation) error {
	return registry.register(method, path, operation, nil)
}

func (registry *Registry) register(method, path string, operation *openapi3.Operation, schemas openapi3.Schemas) error {
	if registry == nil {
		return ErrInvalidDocument
	}
	normalizedMethod, err := normalizeMethod(method)
	if err != nil {
		return err
	}
	normalizedPath, err := normalizePath(path)
	if err != nil {
		return err
	}
	clonedOperation, err := cloneOperation(operation)
	if err != nil {
		return err
	}
	key := operationKey(normalizedMethod, normalizedPath)
	clonedSchemas, err := cloneJSON(schemas)
	if err != nil {
		return fmt.Errorf("%w: 复制组件失败: %v", ErrInvalidDocument, err)
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return ErrRegistryFrozen
	}
	if len(registry.operations) >= maximumContractRoutes {
		return fmt.Errorf("%w: 操作数量超过 %d", ErrInvalidDocument, maximumContractRoutes)
	}
	if _, exists := registry.operations[key]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateOperation, key)
	}
	if previous, exists := registry.operationIDs[clonedOperation.OperationID]; exists {
		return fmt.Errorf("%w: %q 已用于 %s", ErrDuplicateOperationID, clonedOperation.OperationID, previous)
	}
	for name, schema := range clonedSchemas {
		if len(name) > maximumComponentName || !componentNamePattern.MatchString(name) || schema == nil {
			return ErrInvalidDocument
		}
		if previous, exists := registry.document.Components.Schemas[name]; exists {
			before, _ := json.Marshal(previous)
			after, _ := json.Marshal(schema)
			if string(before) != string(after) {
				return fmt.Errorf("%w: Schema %q 内容冲突", ErrDuplicateOperation, name)
			}
		}
	}
	item := registry.document.Paths.Value(normalizedPath)
	if item == nil {
		item = &openapi3.PathItem{}
		registry.document.Paths.Set(normalizedPath, item)
	}
	if item.GetOperation(normalizedMethod) != nil {
		return fmt.Errorf("%w: %s", ErrDuplicateOperation, key)
	}
	item.SetOperation(normalizedMethod, clonedOperation)
	for name, schema := range clonedSchemas {
		registry.document.Components.Schemas[name] = schema
	}
	registry.operations[key] = struct{}{}
	registry.operationIDs[clonedOperation.OperationID] = key
	return nil
}

// AddSchema 注册防御性复制的组件 Schema。
func (registry *Registry) AddSchema(name string, schema *openapi3.SchemaRef) error {
	if registry == nil {
		return ErrInvalidDocument
	}
	name = strings.TrimSpace(name)
	if len(name) > maximumComponentName || !componentNamePattern.MatchString(name) || schema == nil {
		return fmt.Errorf("%w: Schema 名称或内容非法", ErrInvalidDocument)
	}
	cloned, err := cloneJSON(*schema)
	if err != nil {
		return fmt.Errorf("%w: 复制 Schema %q 失败: %v", ErrInvalidDocument, name, err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return ErrRegistryFrozen
	}
	if _, exists := registry.document.Components.Schemas[name]; exists {
		return fmt.Errorf("%w: Schema %q", ErrDuplicateOperation, name)
	}
	registry.document.Components.Schemas[name] = &cloned
	return nil
}

// AddSecurityScheme 注册防御性复制的安全方案。
func (registry *Registry) AddSecurityScheme(name string, scheme *openapi3.SecuritySchemeRef) error {
	if registry == nil {
		return ErrInvalidDocument
	}
	name = strings.TrimSpace(name)
	if len(name) > maximumComponentName || !componentNamePattern.MatchString(name) || scheme == nil {
		return fmt.Errorf("%w: SecurityScheme 名称或内容非法", ErrInvalidDocument)
	}
	cloned, err := cloneJSON(*scheme)
	if err != nil {
		return fmt.Errorf("%w: 复制 SecurityScheme %q 失败: %v", ErrInvalidDocument, name, err)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.frozen {
		return ErrRegistryFrozen
	}
	if _, exists := registry.document.Components.SecuritySchemes[name]; exists {
		return fmt.Errorf("%w: SecurityScheme %q", ErrDuplicateOperation, name)
	}
	registry.document.Components.SecuritySchemes[name] = &cloned
	return nil
}

// JSON 完整校验文档并返回独立的不可变快照。
func (registry *Registry) JSON(ctx context.Context) ([]byte, error) {
	if registry == nil {
		return nil, ErrInvalidDocument
	}
	if ctx == nil {
		ctx = context.Background()
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if !registry.frozen {
		encoded, err := json.Marshal(registry.document)
		if err != nil {
			return nil, fmt.Errorf("%w: 序列化失败: %v", ErrInvalidDocument, err)
		}
		// 在独立文档上解析内部组件引用；默认禁止外部引用，不读取网络或仓库文件。
		loader := openapi3.NewLoader()
		loader.Context = ctx
		resolved, err := loader.LoadFromData(encoded)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
		}
		if err := resolved.Validate(ctx, openapi3.EnableMultiError()); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
		}
		registry.snapshot = encoded
		registry.frozen = true
	}
	return append([]byte(nil), registry.snapshot...), nil
}

// Handler 返回只读 JSON 处理器，支持 HEAD、ETag 和条件请求。
func (registry *Registry) Handler(ctx context.Context) (http.Handler, error) {
	content, err := registry.JSON(ctx)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.Header().Set("ETag", etag)
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		if request == nil || request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			writer.Header().Del("Content-Length")
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("If-None-Match") == etag {
			writer.Header().Del("Content-Length")
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.WriteHeader(http.StatusOK)
		if request.Method == http.MethodGet {
			_, _ = writer.Write(content)
		}
	}), nil
}

// ValidateRouter 检查 prefix 范围内实际路由和文档操作双向一一对应。
// ignored 使用 "METHOD /path" 格式显式排除健康检查等不进入公开契约的路由。
func (registry *Registry) ValidateRouter(router *route.Router, prefix string, ignored ...string) error {
	if registry == nil || router == nil {
		return ErrRouteCoverage
	}
	normalizedPrefix, err := normalizeCoveragePrefix(prefix)
	if err != nil {
		return err
	}
	ignoredSet := make(map[string]struct{}, len(ignored))
	for _, key := range ignored {
		ignoredSet[strings.TrimSpace(key)] = struct{}{}
	}
	routes, err := router.Routes()
	if err != nil {
		return fmt.Errorf("%w: 读取路由失败: %v", ErrRouteCoverage, err)
	}
	actual := make(map[string]struct{})
	problems := make([]string, 0)
	for _, info := range routes {
		if !pathHasPrefix(info.Path, normalizedPrefix) {
			continue
		}
		normalizedPath, pathErr := normalizePath(info.Path)
		if pathErr != nil {
			problems = append(problems, fmt.Sprintf("实际路由 %s %s 无法表达为 OpenAPI: %v", info.Method, info.Path, pathErr))
			continue
		}
		method, methodErr := normalizeMethod(info.Method)
		if methodErr != nil {
			problems = append(problems, fmt.Sprintf("实际路由 %s %s 无法表达为 OpenAPI", info.Method, info.Path))
			continue
		}
		key := operationKey(method, normalizedPath)
		if _, skipped := ignoredSet[key]; skipped {
			continue
		}
		if info.Domain != "" || info.ExtensionRequired {
			problems = append(problems, fmt.Sprintf(
				"实际路由 %s %s 的 domain=%q extension=%q 尚未绑定到 OpenAPI operation，无法仅凭 METHOD/path 证明覆盖",
				info.Method,
				info.Path,
				info.Domain,
				info.Extension,
			))
			continue
		}
		actual[key] = struct{}{}
	}

	registry.mu.RLock()
	documented := make(map[string]struct{}, len(registry.operations))
	for key := range registry.operations {
		parts := strings.SplitN(key, " ", 2)
		if len(parts) == 2 && pathHasPrefix(parts[1], normalizedPrefix) {
			documented[key] = struct{}{}
		}
	}
	registry.mu.RUnlock()
	for key := range actual {
		if _, exists := documented[key]; !exists {
			problems = append(problems, "缺少文档: "+key)
		}
	}
	for key := range documented {
		if _, exists := actual[key]; !exists {
			if _, skipped := ignoredSet[key]; !skipped {
				problems = append(problems, "缺少实际路由: "+key)
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("%w: %s", ErrRouteCoverage, strings.Join(problems, "; "))
	}
	return nil
}

func cloneOperation(operation *openapi3.Operation) (*openapi3.Operation, error) {
	if operation == nil {
		return nil, ErrInvalidOperation
	}
	operationID := strings.TrimSpace(operation.OperationID)
	if operationID == "" || len(operationID) > maximumOperationID || !operationIDPattern.MatchString(operationID) {
		return nil, fmt.Errorf("%w: operationId 非法", ErrInvalidOperation)
	}
	cloned, err := cloneJSON(*operation)
	if err != nil {
		return nil, fmt.Errorf("%w: 复制失败: %v", ErrInvalidOperation, err)
	}
	cloned.OperationID = operationID
	if cloned.Responses == nil || cloned.Responses.Len() == 0 {
		return nil, fmt.Errorf("%w: responses 不能为空", ErrInvalidOperation)
	}
	return &cloned, nil
}

func cloneJSON[T any](value T) (T, error) {
	var cloned T
	encoded, err := json.Marshal(value)
	if err != nil {
		return cloned, err
	}
	if err = json.Unmarshal(encoded, &cloned); err != nil {
		return cloned, err
	}
	return cloned, nil
}

func normalizeMethod(method string) (string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return method, nil
	default:
		return "", fmt.Errorf("%w: HTTP 方法 %q", ErrUnsupportedRoute, method)
	}
}

func normalizePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > maximumContractPath || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "#\\\r\n\x00") || strings.Contains(path, "//") {
		return "", fmt.Errorf("%w: 路径 %q 非法", ErrUnsupportedRoute, path)
	}
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if path == "/" {
		return path, nil
	}
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			name := strings.TrimPrefix(segment, ":")
			if strings.HasSuffix(name, "?") {
				return "", fmt.Errorf("%w: 可选参数 %q 必须展开为两条显式契约", ErrUnsupportedRoute, segment)
			}
			if !pathParameterPattern.MatchString(name) {
				return "", fmt.Errorf("%w: 参数 %q 非法", ErrUnsupportedRoute, name)
			}
			segments[index] = "{" + name + "}"
			continue
		}
		if strings.ContainsAny(segment, "{}") {
			if len(segment) < 3 || segment[0] != '{' || segment[len(segment)-1] != '}' || !pathParameterPattern.MatchString(segment[1:len(segment)-1]) {
				return "", fmt.Errorf("%w: 参数段 %q 非法", ErrUnsupportedRoute, segment)
			}
		}
	}
	return "/" + strings.Join(segments, "/"), nil
}

func normalizeCoveragePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return "/", nil
	}
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "{}:?\r\n\x00") {
		return "", fmt.Errorf("%w: 前缀 %q 非法", ErrRouteCoverage, prefix)
	}
	if prefix != "/" {
		prefix = strings.TrimSuffix(prefix, "/")
	}
	return prefix, nil
}

func pathHasPrefix(path, prefix string) bool {
	return prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

func operationKey(method, path string) string {
	return method + " " + path
}
