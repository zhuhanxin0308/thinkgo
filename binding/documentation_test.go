package binding

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type DocumentedIdentity struct {
	ID int64 `path:"id" json:"-"`
}

type documentedInput struct {
	Input
	DocumentedIdentity
	Name string `json:"name" validate:"required" doc:"显式字段说明"`
}

type documentedOutput[T any] struct {
	Data []T `json:"data"`
}

type documentedUser struct {
	Name string `json:"name"`
}

// TestOperationSourceDocumentation 验证注释覆盖路径参数、匿名嵌入、泛型与嵌套响应，显式标签具有优先级。
func TestOperationSourceDocumentation(t *testing.T) {
	prefix := reflect.TypeFor[documentedInput]().PkgPath() + "."
	docs := Documentation{
		prefix + "documentedInput":    {Description: "创建请求", Fields: map[string]string{"Name": "源码名称"}},
		prefix + "DocumentedIdentity": {Fields: map[string]string{"ID": "用户编号"}},
		prefix + "documentedOutput":   {Description: "分页响应", Fields: map[string]string{"Data": "用户列表"}},
		prefix + "documentedUser":     {Fields: map[string]string{"Name": "用户姓名"}},
	}
	operation, schemas, err := OperationWithDocumentation(reflect.TypeFor[documentedInput](), reflect.TypeFor[documentedOutput[documentedUser]](), "create", 201, docs)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"用户编号", "显式字段说明", "创建请求", "分页响应", "用户列表", "用户姓名"} {
		if !strings.Contains(string(operation)+string(schemas), expected) {
			t.Fatalf("文档缺少 %q: %s %s", expected, operation, schemas)
		}
	}
	if strings.Contains(string(schemas), "源码名称") || !json.Valid(schemas) {
		t.Fatalf("标签优先级或 JSON 无效: %s", schemas)
	}
}

// TestSourceDocumentationDoesNotMutateBindingPlans 验证不同注册表不会通过共享绑定缓存串用说明。
func TestSourceDocumentationDoesNotMutateBindingPlans(t *testing.T) {
	typ := reflect.TypeFor[documentedUser]()
	key := typ.PkgPath() + "." + typ.Name()
	var wait sync.WaitGroup
	for _, description := range []string{"甲说明", "乙说明"} {
		wait.Go(func() {
			docs := Documentation{key: {Description: description}}
			_, schemas, err := OperationWithDocumentation(reflect.TypeFor[Input](), typ, "get", 200, docs)
			if err != nil || !strings.Contains(string(schemas), description) {
				t.Errorf("独立文档生成失败: %s %v", schemas, err)
			}
		})
	}
	wait.Wait()
	_, schemas, err := Operation(reflect.TypeFor[Input](), typ, "get", 200)
	if err != nil || strings.Contains(string(schemas), "说明") {
		t.Fatalf("注释污染共享计划: %s %v", schemas, err)
	}
}

type documentedInlineInput struct {
	Input
	Shipping struct {
		City string `json:"city"`
	} `json:"shipping"`
	Billing struct {
		City string `json:"city"`
	} `json:"billing"`
}

// TestInlineSourceCommentsKeepTheirFieldContext 验证形状相同的匿名结构体仍保留各自的字段含义。
func TestInlineSourceCommentsKeepTheirFieldContext(t *testing.T) {
	typ := reflect.TypeFor[documentedInlineInput]()
	docs := Documentation{typ.PkgPath() + "." + typ.Name(): {
		Inline: map[string]TypeDocumentation{
			"Shipping": {Fields: map[string]string{"City": "收货城市"}},
			"Billing":  {Fields: map[string]string{"City": "账单城市"}},
		},
	}}
	_, schemas, err := OperationWithDocumentation(typ, nil, "create", 204, docs)
	if err != nil || !strings.Contains(string(schemas), "收货城市") || !strings.Contains(string(schemas), "账单城市") {
		t.Fatalf("匿名字段说明被错误合并: %s %v", schemas, err)
	}
}
