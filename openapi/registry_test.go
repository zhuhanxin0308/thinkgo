package openapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestRegistryValidatesFreezesAndServesDocument 验证路由转换、完整校验、防御性复制和 HTTP 缓存语义。
func TestRegistryValidatesFreezesAndServesDocument(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "Accounts API", Version: "1.0.0"})
	if err != nil {
		t.Fatalf("创建 OpenAPI 注册表失败: %v", err)
	}
	operation := validOperation("users.show")
	operation.Parameters = openapi3.Parameters{
		&openapi3.ParameterRef{Value: openapi3.NewPathParameter("id").WithSchema(openapi3.NewStringSchema())},
	}
	if err = registry.Register(http.MethodGet, "/users/:id", operation); err != nil {
		t.Fatalf("注册接口契约失败: %v", err)
	}
	operation.Summary = "调用方后续修改"
	if err = registry.Register(http.MethodGet, "/users/:id", validOperation("users.duplicate")); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("重复方法与路径必须被拒绝，实际为 %v", err)
	}

	document, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatalf("冻结 OpenAPI 文档失败: %v", err)
	}
	if !strings.Contains(string(document), `"/users/{id}"`) || strings.Contains(string(document), "调用方后续修改") {
		t.Fatalf("生成文档路径或快照隔离错误: %s", document)
	}
	if err = registry.Register(http.MethodPost, "/users", validOperation("users.create")); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("冻结后注册必须失败，实际为 %v", err)
	}

	handler, err := registry.Handler(context.Background())
	if err != nil {
		t.Fatalf("创建 OpenAPI HTTP 处理器失败: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("ETag") == "" {
		t.Fatalf("OpenAPI 响应错误: status=%d headers=%v", response.Code, response.Header())
	}
	conditional := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	conditional.Header.Set("If-None-Match", response.Header().Get("ETag"))
	notModified := httptest.NewRecorder()
	handler.ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("条件请求错误: status=%d body=%q", notModified.Code, notModified.Body.String())
	}
}

// TestRegistryRejectsInvalidContracts 验证非法信息、可选路径、重复 operationId 和缺失响应均失败关闭。
func TestRegistryRejectsInvalidContracts(t *testing.T) {
	if _, err := NewRegistry(openapi3.Info{}); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("空 Info 必须被拒绝，实际为 %v", err)
	}
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	if err := registry.Register(http.MethodGet, "/users/:id?", validOperation("users.optional")); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("可选路径段必须要求显式展开，实际为 %v", err)
	}
	if err := registry.Register(http.MethodGet, "/first", validOperation("same.id")); err != nil {
		t.Fatalf("注册首个 operationId 失败: %v", err)
	}
	if err := registry.Register(http.MethodPost, "/second", validOperation("same.id")); !errors.Is(err, ErrDuplicateOperationID) {
		t.Fatalf("重复 operationId 必须被拒绝，实际为 %v", err)
	}
	invalid, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	if err := invalid.Register(http.MethodGet, "/broken", &openapi3.Operation{OperationID: "broken"}); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("缺失响应的操作必须在注册期失败，实际为 %v", err)
	}
}

// TestRegistryValidatesRouterCoverage 验证 API 前缀内的实际路由和契约必须双向一致。
func TestRegistryValidatesRouterCoverage(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	operation := validOperation("users.show")
	operation.Parameters = openapi3.Parameters{
		&openapi3.ParameterRef{Value: openapi3.NewPathParameter("id").WithSchema(openapi3.NewStringSchema())},
	}
	_ = registry.Register(http.MethodGet, "/api/users/:id", operation)
	router := route.NewRouter()
	_, _ = router.Get("/api/users/:id", "User@Show")
	_, _ = router.Get("/health", "Health@Show")
	if err := registry.ValidateRouter(router, "/api"); err != nil {
		t.Fatalf("一致的路由契约校验失败: %v", err)
	}

	missing, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	if err := missing.ValidateRouter(router, "/api"); !errors.Is(err, ErrRouteCoverage) {
		t.Fatalf("缺失接口契约必须失败，实际为 %v", err)
	}
	extra, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	_ = extra.Register(http.MethodGet, "/api/ghost", validOperation("ghost.show"))
	if err := extra.ValidateRouter(router, "/api"); !errors.Is(err, ErrRouteCoverage) {
		t.Fatalf("不存在的文档路由必须失败，实际为 %v", err)
	}
}

// TestRegistryComponentsAndHTTPMethodBoundaries 验证组件冻结、组件去重以及 HEAD/405 响应边界。
func TestRegistryComponentsAndHTTPMethodBoundaries(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	schema := &openapi3.SchemaRef{Value: openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema())}
	if err := registry.AddSchema("User", schema); err != nil {
		t.Fatalf("注册 Schema 失败: %v", err)
	}
	if err := registry.AddSchema("User", schema); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("重复 Schema 必须被拒绝，实际为 %v", err)
	}
	if err := registry.AddSchema("bad name", schema); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("非法 Schema 名称必须被拒绝，实际为 %v", err)
	}
	scheme := &openapi3.SecuritySchemeRef{Value: openapi3.NewSecurityScheme().WithType("http").WithScheme("bearer")}
	if err := registry.AddSecurityScheme("BearerAuth", scheme); err != nil {
		t.Fatalf("注册安全方案失败: %v", err)
	}
	if err := registry.AddSecurityScheme("BearerAuth", scheme); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("重复安全方案必须被拒绝，实际为 %v", err)
	}
	if err := registry.Register(http.MethodGet, "/users", validOperation("users.list")); err != nil {
		t.Fatalf("注册接口失败: %v", err)
	}
	handler, err := registry.Handler(context.Background())
	if err != nil {
		t.Fatalf("创建文档处理器失败: %v", err)
	}
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, "/openapi.json", nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD 响应错误: status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}
	methodNotAllowed := httptest.NewRecorder()
	handler.ServeHTTP(methodNotAllowed, httptest.NewRequest(http.MethodPost, "/openapi.json", nil))
	if methodNotAllowed.Code != http.StatusMethodNotAllowed || methodNotAllowed.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("非只读方法响应错误: status=%d headers=%v", methodNotAllowed.Code, methodNotAllowed.Header())
	}
	if methodNotAllowed.Header().Get("Content-Length") != "" || methodNotAllowed.Body.Len() != 0 {
		t.Fatalf("405 空响应不得保留 OpenAPI 文档长度: headers=%v body=%q", methodNotAllowed.Header(), methodNotAllowed.Body.String())
	}
	if err := registry.AddSchema("AfterFreeze", schema); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("冻结后 Schema 注册必须失败，实际为 %v", err)
	}
	if err := registry.AddSecurityScheme("AfterFreeze", scheme); !errors.Is(err, ErrRegistryFrozen) {
		t.Fatalf("冻结后安全方案注册必须失败，实际为 %v", err)
	}
}

// TestRegistryRejectsUnsupportedMethodsAndCoverageRoutes 验证 ANY、非法前缀和可选实际路由不会被静默遗漏。
func TestRegistryRejectsUnsupportedMethodsAndCoverageRoutes(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
	if err := registry.Register("*", "/api/all", validOperation("all")); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("ANY 契约必须被拒绝，实际为 %v", err)
	}
	router := route.NewRouter()
	_, _ = router.Any("/api/all", "All@Handle")
	_, _ = router.Get("/api/users/:id?", "User@Optional")
	if err := registry.ValidateRouter(router, "api"); !errors.Is(err, ErrRouteCoverage) {
		t.Fatalf("非法覆盖前缀必须被拒绝，实际为 %v", err)
	}
	if err := registry.ValidateRouter(router, "/api"); !errors.Is(err, ErrRouteCoverage) || !strings.Contains(err.Error(), "无法表达") {
		t.Fatalf("不可表达的实际路由必须报告覆盖错误，实际为 %v", err)
	}
}

// TestRegistryRejectsUnboundDomainAndExtensionRoutes 验证仅凭 METHOD/path 不能把
// Host 或扩展名不同的真实路由误判为已被同一 OpenAPI operation 覆盖。
func TestRegistryRejectsUnboundDomainAndExtensionRoutes(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*route.Route) error
		fragment  string
	}{
		{name: "域名", configure: func(registered *route.Route) error { return registered.WithDomain("api.example.com") }, fragment: "domain"},
		{name: "扩展名", configure: func(registered *route.Route) error { return registered.WithExtension("json") }, fragment: "extension"},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, _ := NewRegistry(openapi3.Info{Title: "API", Version: "1"})
			if err := registry.Register(http.MethodGet, "/api/users", validOperation("users.list")); err != nil {
				t.Fatalf("注册 OpenAPI operation 失败: %v", err)
			}
			router := route.NewRouter()
			registered, err := router.Get("/api/users", "User@Index")
			if err != nil {
				t.Fatalf("注册路由失败: %v", err)
			}
			if err = test.configure(registered); err != nil {
				t.Fatalf("配置路由身份失败: %v", err)
			}
			err = registry.ValidateRouter(router, "/api")
			if !errors.Is(err, ErrRouteCoverage) || !strings.Contains(err.Error(), test.fragment) {
				t.Fatalf("未绑定的%s路由必须 fail-closed: %v", test.name, err)
			}
		})
	}
}

func validOperation(operationID string) *openapi3.Operation {
	return &openapi3.Operation{
		OperationID: operationID,
		Summary:     "读取资源",
		Responses: openapi3.NewResponses(
			openapi3.WithStatus(http.StatusOK, &openapi3.ResponseRef{Value: openapi3.NewResponse().WithDescription("成功")}),
		),
	}
}
