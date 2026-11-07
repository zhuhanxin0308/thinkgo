package adapters_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	beegoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/beego"
	echoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/echo"
	fiberadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/fiber"
	ginadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/gin"
	irisadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/iris"
	thinkgoadapter "github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/adapters/thinkgo"
	"github.com/zhuhanxin0308/thinkgo/benchmarks/business/internal/business"
)

// probeService 只隔离数据库依赖；每个请求仍经过被测框架真实路由和绑定流程。
type probeService struct {
	authCalls      int
	calls          int
	authError      error
	err            error
	replay         bool
	cache          string
	lastID         string
	lastKey        string
	query          url.Values
	order          business.OrderInput
	comment        business.CommentInput
	requestContext context.Context
	identity       business.Principal
}

func (s *probeService) Limits() business.Dataset {
	return business.Dataset{MaximumBodyBytes: 256, RequestTimeoutSeconds: 2}
}
func (s *probeService) Authenticate(header string) (business.Principal, error) {
	s.authCalls++
	if header != "Bearer valid" {
		return business.Principal{}, &business.PublicError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "凭证错误"}
	}
	return business.Principal{TenantID: 2, UserID: 1001}, s.authError
}
func (s *probeService) seen(ctx context.Context, principal business.Principal) {
	s.calls++
	s.requestContext = ctx
	s.identity = principal
}
func (s *probeService) Catalog(ctx context.Context, principal business.Principal, query url.Values) (business.CatalogResponse, error) {
	s.seen(ctx, principal)
	s.query = query
	return business.CatalogResponse{}, s.err
}
func (s *probeService) Detail(ctx context.Context, principal business.Principal, id string) (business.DetailResponse, string, error) {
	s.seen(ctx, principal)
	s.lastID = id
	return business.DetailResponse{}, s.cache, s.err
}
func (s *probeService) CreateOrder(ctx context.Context, principal business.Principal, key string, input business.OrderInput) (business.OrderResponse, bool, error) {
	s.seen(ctx, principal)
	s.lastKey = key
	s.order = input
	return business.OrderResponse{}, s.replay, s.err
}
func (s *probeService) AddComment(ctx context.Context, principal business.Principal, id string, input business.CommentInput) (business.CommentResponse, error) {
	s.seen(ctx, principal)
	s.lastID = id
	s.comment = input
	return business.CommentResponse{}, s.err
}

type application interface {
	Handler() http.Handler
	Close() error
}
type exchange func(*http.Request) (*http.Response, error)
type factory func(*probeService) (exchange, func() error, error)

const testAddress = "127.0.0.1:18080"

func ordinary(app application, err error) (exchange, func() error, error) {
	if err != nil {
		return nil, nil, err
	}
	return func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return recorder.Result(), nil
	}, app.Close, nil
}

func TestNativeFrameworkContracts(t *testing.T) {
	factories := map[string]factory{
		"thinkgo": func(s *probeService) (exchange, func() error, error) {
			return ordinary(thinkgoadapter.New(s, testAddress))
		},
		"gin": func(s *probeService) (exchange, func() error, error) { return ordinary(ginadapter.New(s, testAddress)) },
		"echo": func(s *probeService) (exchange, func() error, error) {
			return ordinary(echoadapter.New(s, testAddress))
		},
		"beego": func(s *probeService) (exchange, func() error, error) {
			return ordinary(beegoadapter.New(s, testAddress))
		},
		"iris": func(s *probeService) (exchange, func() error, error) {
			return ordinary(irisadapter.New(s, testAddress))
		},
		"fiber": func(s *probeService) (exchange, func() error, error) {
			app, err := fiberadapter.New(s, testAddress)
			if err != nil {
				return nil, nil, err
			}
			return func(r *http.Request) (*http.Response, error) { return app.Router.Test(r) }, app.Close, nil
		},
	}
	for name, create := range factories {
		t.Run(name, func(t *testing.T) {
			service := &probeService{cache: "hit"}
			send, closeApp, err := create(service)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := closeApp(); err != nil {
					t.Error(err)
				}
			})
			request := func(method, path, body, token string) *http.Response {
				t.Helper()
				r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
				r.Header.Set("Authorization", token)
				if method == http.MethodPost {
					r.Header.Set("Content-Type", "application/json")
				}
				r.Header.Set("Idempotency-Key", "test-order-key")
				response, err := send(r)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { response.Body.Close() })
				return response
			}
			health := request(http.MethodGet, "/healthz", "", "")
			assertStatus(t, health, http.StatusOK)
			if service.authCalls != 0 {
				t.Fatal("健康接口错误触发业务认证")
			}
			assertJSON(t, health, map[string]string{"status": "ok"})
			invalid := request(http.MethodGet, "/api/catalog", "", "")
			status, body := business.ErrorResult(&business.PublicError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "凭证错误"})
			assertStatus(t, invalid, status)
			assertJSON(t, invalid, body)
			if service.calls != 0 {
				t.Fatal("认证失败后执行了业务")
			}
			catalog := request(http.MethodGet, "/api/catalog?category_id=2&limit=3", "", "Bearer valid")
			assertStatus(t, catalog, http.StatusOK)
			assertJSON(t, catalog, business.CatalogResponse{})
			if service.query.Get("category_id") != "2" || service.query.Get("limit") != "3" {
				t.Fatal("查询参数未完整传入业务")
			}
			if service.identity != (business.Principal{TenantID: 2, UserID: 1001}) {
				t.Fatal("框架上下文中的已验证身份未传入业务")
			}
			if _, ok := service.requestContext.Deadline(); !ok {
				t.Fatal("业务上下文缺少请求期限")
			}
			if service.requestContext.Err() != context.Canceled {
				t.Fatal("请求结束后未释放上下文")
			}
			for _, cache := range []string{"hit", "miss"} {
				service.cache = cache
				response := request(http.MethodGet, "/api/products/17", "", "Bearer valid")
				assertStatus(t, response, http.StatusOK)
				if response.Header.Get("X-Benchmark-Cache") != cache || service.lastID != "17" {
					t.Fatal("详情缓存或路径参数错误")
				}
			}
			orderBody := `{"items":[{"product_id":17,"quantity":2}]}`
			for _, replay := range []bool{false, true} {
				service.replay = replay
				response := request(http.MethodPost, "/api/orders", orderBody, "Bearer valid")
				expected := http.StatusCreated
				if replay {
					expected = http.StatusOK
				}
				assertStatus(t, response, expected)
				if service.lastKey != "test-order-key" || len(service.order.Items) != 1 || service.order.Items[0].ProductID != 17 || service.order.Items[0].Quantity != 2 {
					t.Fatal("订单绑定或幂等键丢失")
				}
			}
			comment := request(http.MethodPost, "/api/tickets/23/comments", `{"body":"这是一条有效的测试评论内容。"}`, "Bearer valid")
			assertStatus(t, comment, http.StatusCreated)
			if service.lastID != "23" || service.comment.Body != "这是一条有效的测试评论内容。" {
				t.Fatal("工单评论绑定错误")
			}
			for _, endpoint := range []string{"/api/orders", "/api/tickets/23/comments"} {
				for _, malformed := range []string{"", "{", "null", "{}{}", `{"items":[{"quantity":"wrong"}],"body":17}`, `{"body":"a","body":"b"}`} {
					before := service.calls
					response := request(http.MethodPost, endpoint, malformed, "Bearer valid")
					assertStatus(t, response, http.StatusUnprocessableEntity)
					if service.calls != before {
						t.Fatal("绑定失败后仍然执行了业务")
					}
				}
			}
			before := service.calls
			largeBody := `{"padding":"` + strings.Repeat("x", 300) + `"}`
			if name == "fiber" {
				// Fiber.Test在HTTP正文读取超限时直接返回传输错误；真实413另由监听测试验证。
				raw := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/orders", strings.NewReader(largeBody))
				raw.Header.Set("Content-Type", "application/json")
				raw.Header.Set("Authorization", "Bearer valid")
				if _, err := send(raw); err == nil || !strings.Contains(err.Error(), "body size exceeds") {
					t.Fatalf("Fiber原生正文上限未生效: %v", err)
				}
			} else {
				large := request(http.MethodPost, "/api/orders", largeBody, "Bearer valid")
				if large.StatusCode != http.StatusUnprocessableEntity && large.StatusCode != http.StatusRequestEntityTooLarge {
					t.Fatalf("请求体上限未生效: %d", large.StatusCode)
				}
			}
			if service.calls != before {
				t.Fatal("超限请求执行了业务")
			}
			service.err = errors.New("内部数据库细节不得泄漏")
			for _, input := range []struct{ method, path, body string }{
				{http.MethodGet, "/api/catalog", ""}, {http.MethodGet, "/api/products/17", ""},
				{http.MethodPost, "/api/orders", orderBody}, {http.MethodPost, "/api/tickets/23/comments", `{"body":"有效评论"}`},
			} {
				response := request(input.method, input.path, input.body, "Bearer valid")
				status, body := business.ErrorResult(service.err)
				assertStatus(t, response, status)
				assertJSON(t, response, body)
			}
		})
	}
}

func assertStatus(t *testing.T, response *http.Response, status int) {
	t.Helper()
	if response.StatusCode != status {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP状态=%d，期望=%d，响应=%s", response.StatusCode, status, data)
	}
}
func assertJSON(t *testing.T, response *http.Response, expected any) {
	t.Helper()
	if !strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		t.Fatal("响应不是JSON")
	}
	var actual, wanted any
	if err := json.NewDecoder(response.Body).Decode(&actual); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &wanted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, wanted) {
		t.Fatalf("响应字段不符: actual=%#v expected=%#v", actual, wanted)
	}
}
