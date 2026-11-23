package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
)

type recursiveResponseMap map[string]recursiveResponseMap
type recursiveResponseSlice []recursiveResponseSlice
type recursiveResponseIndex map[string]recursiveResponseRows
type recursiveResponseRows []recursiveResponseIndex
type recursiveResponsePointer *recursiveResponsePointer
type recursiveOptionalPointer *binding.Optional[recursiveOptionalPointer]

// TestRegisterTypedRecursiveResponses 在独立子进程验证注册、引用解析及实例校验。
func TestRegisterTypedRecursiveResponses(t *testing.T) {
	const child = "THINKGO_TEST_RECURSIVE_RESPONSE_CHILD"
	if os.Getenv(child) == "" {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRegisterTypedRecursiveResponses$")
		cmd.Env = append(os.Environ(), child+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("子进程未能完成递归响应契约校验：%v\n%s", err, output)
		}
		return
	}
	registry, err := NewRegistry(openapi3.Info{Title: "递归响应", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	registrations := []struct {
		name     string
		register func(*Registry, string, string, string, int) error
		valid    string
		invalid  string
	}{
		{"map", RegisterTyped[binding.Input, recursiveResponseMap], `{"a":{"b":null}}`, `{"a":[]}`},
		{"slice", RegisterTyped[binding.Input, recursiveResponseSlice], `[[null],[]]`, `[{}]`},
		{"index", RegisterTyped[binding.Input, recursiveResponseIndex], `{"a":[{"b":[]}]}`, `{"a":{}}`},
		{"rows", RegisterTyped[binding.Input, recursiveResponseRows], `[{"a":[null]}]`, `[{"a":{}}]`},
		{"pointer", RegisterTyped[binding.Input, recursiveResponsePointer], `null`, `{}`},
		{"optional", RegisterTyped[binding.Input, recursiveOptionalPointer], `null`, `0`},
	}
	for _, route := range registrations {
		if err := route.register(registry, http.MethodGet, "/"+route.name, route.name, http.StatusOK); err != nil {
			t.Fatalf("注册 %s 失败：%v", route.name, err)
		}
	}
	data, err := registry.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	response := func(name string) *openapi3.SchemaRef {
		return doc.Paths.Value("/" + name).Get.Responses.Status(http.StatusOK).Value.Content["application/json"].Schema
	}
	selfMap, selfSlice := response("map"), response("slice")
	if selfMap.Ref == "" || selfMap.Value.AdditionalProperties.Schema.Ref != selfMap.Ref || selfSlice.Ref == "" || selfSlice.Value.Items.Ref != selfSlice.Ref {
		t.Fatal("直接递归类型未引用自身")
	}
	index, rows := response("index"), response("rows")
	if index.Ref == rows.Ref || index.Value.AdditionalProperties.Schema.Ref != rows.Ref || rows.Value.Items.Ref != index.Ref {
		t.Fatal("从不同类型开始注册后，互递归组件没有复用同一对名称")
	}
	var pointer recursiveResponsePointer
	var optional binding.Optional[recursiveOptionalPointer]
	for _, value := range []any{pointer, recursiveResponsePointer(&pointer), recursiveOptionalPointer(&optional)} {
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != "null" {
			t.Fatalf("有限指针值的 JSON 编码为 %s，%v", encoded, err)
		}
	}
	for _, route := range registrations {
		for _, input := range []string{route.valid, route.invalid} {
			var value any
			if err := json.Unmarshal([]byte(input), &value); err != nil {
				t.Fatal(err)
			}
			err := response(route.name).Value.VisitJSON(value, openapi3.EnableJSONSchema2020())
			if (err == nil) != (input == route.valid) {
				t.Errorf("%s 对 %s 的校验结果不符：%v", route.name, input, err)
			}
		}
	}
}
