package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/framework/binding"
	"github.com/zhuhanxin0308/thinkgo/framework/route"
)

// TestSourceCommentsJSONBoundary 验证清单解析拒绝未知字段、尾随值、坏路径与递归字段图。
func TestSourceCommentsJSONBoundary(t *testing.T) {
	for _, data := range []string{`null`, `{`, `{"version":1,"module":"example.com/app","typo":true}`, `{"version":1,"module":"example.com/app"} {}`, `{"version":1,"module":"example.com/app"} garbage`} {
		if _, err := ParseSourceComments([]byte(data)); !errors.Is(err, ErrSourceComments) {
			t.Fatalf("非法清单未拒绝: %q %v", data, err)
		}
	}
	for name, modify := range map[string]func(*SourceComments){
		"路径穿越": func(source *SourceComments) { source.Files = map[string]string{"../api.go": "digest"} },
		"越界类型": func(source *SourceComments) { source.Types = binding.Documentation{"other.com/model.User": {}} },
		"非法字段": func(source *SourceComments) {
			source.Types = binding.Documentation{source.Module + ".User": {Fields: map[string]string{"bad-field": "字段"}}}
		},
		"非法匿名字段": func(source *SourceComments) {
			source.Types = binding.Documentation{source.Module + ".User": {Inline: map[string]binding.TypeDocumentation{"": {}}}}
		},
		"空分组": func(source *SourceComments) {
			source.Handlers[source.Module+".Empty"] = HandlerComments{Tags: []string{" "}}
		},
		"循环字段": func(source *SourceComments) {
			item := binding.TypeDocumentation{Inline: make(map[string]binding.TypeDocumentation)}
			item.Inline["Next"] = item
			source.Types = binding.Documentation{source.Module + ".User": item}
		},
	} {
		t.Run(name, func(t *testing.T) {
			source := commentCatalog()
			modify(&source)
			if err := source.Validate(); !errors.Is(err, ErrSourceComments) {
				t.Fatalf("非法元数据未拒绝: %v", err)
			}
		})
	}
	encoded, _ := json.Marshal(commentCatalog())
	if _, err := ParseSourceComments(encoded); err != nil {
		t.Fatal(err)
	}
}

type genericCommentController[T any] struct{}

func (genericCommentController[T]) Show(commentInput) commentOutput { return commentOutput{} }
func genericCommentHandler[T any](commentInput) commentOutput       { return commentOutput{} }

// TestGenericCommentSymbols 验证泛型函数、值接收者绑定方法与普通方法保持源码声明身份。
func TestGenericCommentSymbols(t *testing.T) {
	prefix := reflect.TypeFor[commentInput]().PkgPath()
	for _, item := range []struct {
		callback any
		name     string
	}{
		{genericCommentHandler[int], ".genericCommentHandler"},
		{genericCommentController[map[string][]int]{}.Show, ".genericCommentController.Show"},
		{(&commentController{}).Show, ".commentController.Show"},
	} {
		if name := sourceHandlerName(item.callback); name != prefix+item.name {
			t.Fatalf("编译符号与源码不一致: %s，预期 %s", name, prefix+item.name)
		}
	}
	for _, callback := range []any{nil, 1, (func())(nil)} {
		if sourceHandlerName(callback) != "" {
			t.Fatal("非法处理器产生了符号")
		}
	}
}

// TestSourceCommentsRegistryIsolation 验证两份应用文档互不污染，旧类型化入口同样获得字段说明。
func TestSourceCommentsRegistryIsolation(t *testing.T) {
	first, second := commentCatalog(), commentCatalog()
	key := reflect.TypeFor[commentOutput]().PkgPath() + ".commentOutput"
	second.Types[key] = binding.TypeDocumentation{Fields: map[string]string{"Name": "另一个应用的姓名"}}
	for _, item := range []struct {
		source SourceComments
		want   string
		absent string
	}{{first, "用户名称", "另一个应用的姓名"}, {second, "另一个应用的姓名", "用户名称"}} {
		registry, err := NewRegistry(openapi3.Info{Title: "接口", Version: "1"}, WithSourceComments(item.source))
		if err != nil {
			t.Fatal(err)
		}
		if err := RegisterTyped[commentInput, commentOutput](registry, "GET", "/users/:id", "users.show", 200); err != nil {
			t.Fatal(err)
		}
		data, err := registry.JSON(context.Background())
		if err != nil || !strings.Contains(string(data), item.want) || strings.Contains(string(data), item.absent) {
			t.Fatalf("类型说明未隔离: %v %s", err, data)
		}
	}
}

// TestCommentOperationIDUsesFinalGroup 验证真实分组路径决定自动标识，重复启动得到完全相同的文档。
func TestCommentOperationIDUsesFinalGroup(t *testing.T) {
	var previous string
	for range 2 {
		registry, _ := NewRegistry(openapi3.Info{Title: "接口", Version: "1"}, WithSourceComments(commentCatalog()))
		router := route.NewRouter()
		for _, prefix := range []string{"/admin", "/public"} {
			if err := router.Group(prefix, func(group *route.Group) error {
				return registry.Routes(group).Get("/users/:id", commentHandler)
			}); err != nil {
				t.Fatal(err)
			}
		}
		if len(registry.operationIDs) != 2 {
			t.Fatal("分组路由的自动标识发生冲突")
		}
		data, err := registry.JSON(context.Background())
		if err != nil || previous != "" && previous != string(data) {
			t.Fatalf("自动标识不稳定: %v", err)
		}
		previous = string(data)
	}
}
