package openapi

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

type commentInput struct {
	binding.Input
	ID int `path:"id" json:"-"`
}

type commentOutput struct {
	Name string `json:"name"`
}

func commentHandler(commentInput) commentOutput { return commentOutput{} }

type commentController struct{}

func (*commentController) Show(commentInput) commentOutput { return commentOutput{} }

func commentCatalog() SourceComments {
	prefix := reflect.TypeFor[commentInput]().PkgPath()
	return SourceComments{
		Version: SourceCommentsVersion, Module: prefix,
		Handlers: map[string]HandlerComments{
			prefix + ".commentHandler":         {Summary: "查询用户", Description: "返回当前用户信息。", Tags: []string{"用户"}, Deprecated: true},
			prefix + ".commentController.Show": {Summary: "控制器查询"},
		},
		Types: binding.Documentation{prefix + ".commentOutput": {Fields: map[string]string{"Name": "用户名称"}}},
	}
}

// TestSourceCommentsFollowActualHandlerAndRoute 验证同一处理器的不同路由有独立标识，绑定方法复用源码说明。
func TestSourceCommentsFollowActualHandlerAndRoute(t *testing.T) {
	comments := commentCatalog()
	registry, err := NewRegistry(openapi3.Info{Title: "接口", Version: "1"}, WithSourceComments(comments))
	if err != nil {
		t.Fatal(err)
	}
	comments.Handlers[reflect.TypeFor[commentInput]().PkgPath()+".commentHandler"] = HandlerComments{Summary: "外部改写"}
	router := route.NewRouter()
	for _, path := range []string{"/users/:id", "/people/:id"} {
		if err := Handle(router, registry, Operation{Method: "GET", Path: path}, commentHandler); err != nil {
			t.Fatal(err)
		}
	}
	if err := Handle(router, registry, Operation{Method: "GET", Path: "/controller/:id"}, (&commentController{}).Show); err != nil {
		t.Fatal(err)
	}
	data, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"查询用户", "返回当前用户信息", "控制器查询", "用户名称", `"deprecated":true`} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("真实契约缺少 %s: %s", expected, data)
		}
	}
	if strings.Contains(string(data), "外部改写") || len(registry.operationIDs) != 3 {
		t.Fatal("元数据未隔离或操作标识冲突")
	}
}

// TestSourceCommentsPrecedenceAndMissingSource 验证明确信息优先，过期清单和匿名函数会在注册前失败。
func TestSourceCommentsPrecedenceAndMissingSource(t *testing.T) {
	registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"}, WithSourceComments(commentCatalog()))
	router := route.NewRouter()
	if err := Handle(router, registry, Operation{Method: "GET", Path: "/users/:id", OperationID: "explicit", Summary: "显式标题", Tags: []string{"管理"}}, commentHandler); err != nil {
		t.Fatal(err)
	}
	if got := registry.document.Paths.Value("/users/{id}").Get; got.Summary != "显式标题" || got.Tags[0] != "管理" || got.OperationID != "explicit" {
		t.Fatalf("显式元数据被覆盖: %#v", got)
	}
	if err := Handle(router, registry, Operation{Method: "GET", Path: "/unknown"}, func() string { return "ok" }); !errors.Is(err, ErrSourceComments) {
		t.Fatalf("匿名函数没有明确错误: %v", err)
	}
	if err := Handle(router, registry, Operation{Method: "GET", Path: "/unknown", OperationID: "manual", Summary: "手工说明"}, func() string { return "ok" }); err != nil {
		t.Fatalf("显式匿名回调不能注册: %v", err)
	}
}

// TestInvalidSourceCommentsRejected 验证非法版本、符号、指令结果和重复配置不会进入注册表。
func TestInvalidSourceCommentsRejected(t *testing.T) {
	info := openapi3.Info{Title: "接口", Version: "1"}
	for _, source := range []SourceComments{
		{}, {Version: SourceCommentsVersion, Module: "bad module"},
		{Version: SourceCommentsVersion, Module: "example.com/app", Handlers: map[string]HandlerComments{"other.com/x.Func": {Summary: "越界"}}},
		{Version: SourceCommentsVersion, Module: "example.com/app", Handlers: map[string]HandlerComments{"example.com/app.Func": {OperationID: "invalid id"}}},
	} {
		if _, err := NewRegistry(info, WithSourceComments(source)); !errors.Is(err, ErrSourceComments) {
			t.Fatalf("非法注释清单未拒绝: %#v %v", source, err)
		}
	}
	if _, err := NewRegistry(info, nil); err == nil {
		t.Fatal("空注册选项未拒绝")
	}
	if _, err := NewRegistry(info, WithSourceComments(commentCatalog()), WithSourceComments(commentCatalog())); err == nil {
		t.Fatal("重复注释来源未拒绝")
	}
}
