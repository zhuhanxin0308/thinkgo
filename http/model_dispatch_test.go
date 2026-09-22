package http

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/framework"
	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/db"
)

type injectedActionModel struct {
	*db.Model
}

type injectedModelController struct{}

func (*injectedModelController) Read(model *injectedActionModel) (map[string]interface{}, error) {
	return model.FindMap()
}

type injectedModelConnection struct {
	identity db.ConnectionID
}

func (connection *injectedModelConnection) ConnectionID() db.ConnectionID {
	return connection.identity
}

func (*injectedModelConnection) Select(ctx stdcontext.Context, request db.SelectRequest) ([]map[string]interface{}, error) {
	return []map[string]interface{}{{"context": ctx.Value(httpTestContextKey{}), "table": request.Table()}}, nil
}

func (*injectedModelConnection) Insert(stdcontext.Context, db.InsertRequest) (db.InsertResult, error) {
	return db.InsertResult{}, nil
}

func (*injectedModelConnection) Update(stdcontext.Context, db.UpdateRequest) (db.UpdateResult, error) {
	return db.UpdateResult{}, nil
}

func (*injectedModelConnection) Delete(stdcontext.Context, db.DeleteRequest) (db.DeleteResult, error) {
	return db.DeleteResult{}, nil
}

func (*injectedModelConnection) Count(stdcontext.Context, db.CountRequest) (int64, error) {
	return 0, nil
}

func (*injectedModelConnection) Close() error { return nil }

// TestHTTPAutomaticallyInjectsModelsWithRequestContext 验证完整 HTTP 链中的控制器和回调自动模型注入。
func TestHTTPAutomaticallyInjectsModelsWithRequestContext(t *testing.T) {
	basePath := t.TempDir()
	ensureHTTPTestConfigFiles(t, basePath)
	app := framework.NewConsoleAppUninitialized(basePath)
	t.Cleanup(func() { _ = app.Close() })
	if err := app.RegisterModel("User", &injectedActionModel{}); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterController("User", &injectedModelController{}); err != nil {
		t.Fatal(err)
	}
	database := db.NewDB(&injectedModelConnection{identity: db.NewConnectionID("model-http")})
	t.Cleanup(func() { _ = database.Close() })
	if err := app.Instance(string(framework.ServiceDB), database); err != nil {
		t.Fatal(err)
	}
	if err := app.BindFactory(string(framework.ServiceRequest), func(container *framework.Container, raw *http.Request, options ...fwcontext.RequestOption) (*fwcontext.Request, error) {
		model, err := framework.ResolveModel[*injectedActionModel](container)
		if err != nil {
			return nil, err
		}
		row, err := model.FindMap()
		if err != nil || row["context"] != "before-middleware" {
			return nil, fmt.Errorf("请求工厂没有接收原始上下文: row=%v err=%v", row, err)
		}
		return fwcontext.NewRequest(raw, options...)
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterGlobalMiddleware(func(request *fwcontext.Request, next func(*fwcontext.Request) *fwcontext.Response) *fwcontext.Response {
		updated := stdcontext.WithValue(request.Context(), httpTestContextKey{}, request.Path())
		*request.Raw() = *request.Raw().WithContext(updated)
		return next(request)
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterRouteLoader(func(current *framework.App) error {
		current.Route().Get("/model-action", "user/read")
		current.Route().Get("/model-callback", func(model *injectedActionModel) (map[string]interface{}, error) {
			return model.FindMap()
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Initialize(); err != nil {
		t.Fatalf("初始化自动模型应用失败: %v", err)
	}
	handler := newTestHTTPHandler(t, app)
	for _, path := range []string{"/model-action", "/model-callback"} {
		recorder := httptest.NewRecorder()
		raw := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		raw = raw.WithContext(stdcontext.WithValue(raw.Context(), httpTestContextKey{}, "before-middleware"))
		handler.ServeHTTP(recorder, raw)
		if recorder.Code != http.StatusOK {
			t.Fatalf("模型注入请求失败: path=%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		var row map[string]interface{}
		if err := json.Unmarshal(recorder.Body.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if row["context"] != path || row["table"] != "injected_action_model" {
			t.Fatalf("模型类型或请求上下文错误: path=%s row=%v", path, row)
		}
	}
	// 自定义内核直接构造 Request 时仍可解析模型，但必须沿用该请求自己的上下文。
	raw := httptest.NewRequest(http.MethodGet, "http://localhost/model-action", nil)
	raw = raw.WithContext(stdcontext.WithValue(raw.Context(), httpTestContextKey{}, "standalone"))
	request := fwcontext.MustNewRequest(raw)
	matched, _, err := mustHTTPRoute(t, app).Match(request)
	if err != nil || matched == nil {
		t.Fatalf("独立请求路由匹配失败: %v", err)
	}
	response := handler.dispatch(matched, request)
	var row map[string]interface{}
	if err := json.Unmarshal(response.GetBody(), &row); err != nil || row["context"] != "standalone" {
		t.Fatalf("无作用域模型回退丢失请求上下文: row=%v err=%v", row, err)
	}
}
