package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type changingActionController struct{}

func (*changingActionController) Index() string { return "plain" }
func (*changingActionController) IndexAction(request *fwcontext.Request) string {
	return "suffix:" + request.Method()
}

// TestDispatchCacheTracksConfiguredAction 验证同一类型的动作与签名随配置切换，且未知动作不执行旧计划。
func TestDispatchCacheTracksConfiguredAction(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("Changing", func() interface{} { return &changingActionController{} })
	handler := newTestHTTPHandler(t, application)
	matched := routeForDispatchTest(t, "Changing/Index")
	for _, testCase := range []struct {
		suffix string
		body   string
		status int
	}{{"", "plain", http.StatusOK}, {"Action", "suffix:GET", http.StatusOK}, {"", "plain", http.StatusOK}, {"Missing", "Internal Server Error", http.StatusInternalServerError}, {"Action", "suffix:GET", http.StatusOK}} {
		if err := application.Config().Set("route.action_suffix", testCase.suffix); err != nil {
			t.Fatal(err)
		}
		request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "http://example.com/changing", nil))
		response := handler.dispatch(matched, request)
		if response.GetStatus() != testCase.status || string(response.GetBody()) != testCase.body {
			t.Fatalf("后缀 %q 调用了旧动作或旧参数计划: 状态=%d, 正文=%q", testCase.suffix, response.GetStatus(), response.GetBody())
		}
	}
	if len(handler.dispatchPlans) != 1 {
		t.Fatal("同一处理器的配置切换不应无限增加缓存条目")
	}
}

// TestDispatchPlanConcurrentActionChanges 验证并发编译和读取各自返回所请求的不可变动作计划。
func TestDispatchPlanConcurrentActionChanges(t *testing.T) {
	application := newTestHTTPApp(t, t.TempDir(), map[string]interface{}{"enable": false})
	application.BindFactory("Changing", func() interface{} { return &changingActionController{} })
	handler := newTestHTTPHandler(t, application)
	controllerType := reflect.TypeFor[*changingActionController]()
	matched := routeForDispatchTest(t, "Changing/Index")
	const workers = 8
	const iterations = 32
	var done sync.WaitGroup
	for worker := range workers {
		done.Go(func() {
			action := "Index"
			if worker%2 != 0 {
				action = "IndexAction"
			}
			for range iterations {
				plan, err := handler.resolveDispatchPlan("Changing/Index", action, controllerType, true)
				if err != nil || plan.actionMethod.Name != action {
					t.Errorf("并发解析动作 %q 返回错误计划: %v", action, err)
					return
				}
				value := plan.actionMethod.Func.Call(func() []reflect.Value {
					arguments := []reflect.Value{reflect.ValueOf(&changingActionController{})}
					if action == "IndexAction" {
						request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
						arguments = append(arguments, reflect.ValueOf(request))
					}
					return arguments
				}())[0].String()
				if action == "Index" && value != "plain" || action == "IndexAction" && value != "suffix:GET" {
					t.Errorf("已返回计划被其他请求改变: %q", value)
				}
				suffix := ""
				if action == "IndexAction" {
					suffix = "Action"
				}
				if err := application.Config().Set("route.action_suffix", suffix); err != nil {
					t.Error(err)
					return
				}
				request := fwcontext.MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil))
				response := handler.dispatch(matched, request)
				body := string(response.GetBody())
				if response.GetStatus() != http.StatusOK || body != "plain" && body != "suffix:GET" {
					t.Errorf("并发配置更新导致无效分发: 状态=%d, 正文=%q", response.GetStatus(), body)
				}
			}
		})
	}
	done.Wait()
}
