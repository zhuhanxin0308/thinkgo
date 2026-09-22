package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/openapi"
)

type testService struct{ Name string }
type testInput struct {
	binding.Input
	Name string `json:"name" validate:"required"`
}
type testOutput struct {
	Name string `json:"name"`
}

// TestHostRunsRealLifecycleAndDependencyReplacement 验证隔离应用完整启动、类型注入和配置覆盖。
func TestHostRunsRealLifecycleAndDependencyReplacement(t *testing.T) {
	host := New(t, Options{
		Config: map[string]any{"app.default_timezone": "UTC"},
		Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(app *framework.App) error {
				registry, _ := openapi.NewRegistry(openapi3.Info{Title: "测试", Version: "1"})
				return openapi.Handle(app.Route(), registry, openapi.Operation{Method: http.MethodPost, Path: "/users", OperationID: "create", SuccessStatus: http.StatusCreated}, func(input testInput, service *testService) testOutput {
					return testOutput{Name: service.Name + input.Name}
				})
			})
		},
		Configure: func(app *framework.App) error {
			return app.Instance("testService", &testService{Name: "test-"})
		},
	})
	if host.App().Location().String() != "UTC" || !host.App().Initialized() {
		t.Fatal("测试宿主未应用配置或完成初始化")
	}
	response, err := host.JSON(http.MethodPost, "/users", testOutput{Name: "Ada"})
	if err != nil || response.Code != http.StatusCreated {
		t.Fatalf("请求失败: %#v %v", response, err)
	}
	output, err := DecodeJSON[testOutput](response)
	if err != nil || output.Name != "test-Ada" {
		t.Fatalf("依赖或 JSON 错误: %#v %v", output, err)
	}
	const requests = 12
	var wait sync.WaitGroup
	for range requests {
		wait.Go(func() {
			response, err := host.JSON(http.MethodPost, "/users", testOutput{Name: "并发"})
			if err != nil || response.Code != http.StatusCreated {
				t.Errorf("并发请求失败: %#v %v", response, err)
			}
		})
	}
	wait.Wait()
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if response, err := host.JSON(http.MethodPost, "/users", nil); err != nil || response.Code < http.StatusInternalServerError {
		t.Fatalf("关闭后仍然执行业务: %#v %v", response, err)
	}
}

// TestHostReportsStartupFailures 验证初始化各阶段的错误可定位且不会交付半初始化宿主。
func TestHostReportsStartupFailures(t *testing.T) {
	failure := errors.New("测试启动失败")
	for _, options := range []Options{
		{Config: map[string]any{"app.server.port": "invalid"}},
		{Config: map[string]any{"../outside": true}},
		{Register: func(*framework.App) error { return failure }},
		{Configure: func(*framework.App) error { return failure }},
		{Register: func(app *framework.App) error {
			return app.RegisterRouteLoader(func(*framework.App) error { return failure })
		}},
	} {
		host, err := newHost(t.TempDir(), options)
		if err == nil || host != nil {
			t.Fatalf("错误配置交付了宿主: %#v %v", host, err)
		}
	}
}

// TestHostRequestPreservesContextAndRejectsInvalidInput 验证原始请求与 JSON 便捷入口的边界。
func TestHostRequestPreservesContextAndRejectsInvalidInput(t *testing.T) {
	host := New(t, Options{})
	if _, err := host.Do(nil); err == nil {
		t.Fatal("空请求未拒绝")
	}
	if _, err := host.JSON(http.MethodPost, "/", make(chan int)); err == nil {
		t.Fatal("不可编码请求未拒绝")
	}
	if _, err := host.JSON("invalid method", "/", nil); err == nil {
		t.Fatal("非法方法未拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if _, err := host.Do(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("请求取消未保留: %v", err)
	}
	var absent *Host
	if _, err := absent.Do(request); err == nil {
		t.Fatal("空宿主未拒绝")
	}
	if err := absent.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeJSONValidatesContentAndPreservesNumbers 验证响应类型错误与多值 JSON 不会静默通过。
func TestDecodeJSONValidatesContentAndPreservesNumbers(t *testing.T) {
	for _, test := range []struct{ content, media string }{{`{}`, "text/html"}, {`{`, "application/json"}, {`{} {}`, "application/json"}} {
		response := httptest.NewRecorder()
		response.Header().Set("Content-Type", test.media)
		response.Body.WriteString(test.content)
		if _, err := DecodeJSON[testOutput](response); err == nil {
			t.Fatalf("非法 JSON 响应未拒绝: %#v", test)
		}
	}
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Body.WriteString(`{"id":9007199254740993}`)
	value, err := DecodeJSON[map[string]any](response)
	if err != nil || value["id"] != json.Number("9007199254740993") || !strings.Contains(response.Body.String(), "9007199254740993") {
		t.Fatalf("JSON 数字精度或响应体被改变: %#v %v", value, err)
	}
	if _, err := DecodeJSON[testOutput](nil); err == nil {
		t.Fatal("空响应未拒绝")
	}
}
