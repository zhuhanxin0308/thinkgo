package openapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type recursiveFieldContract struct {
	Strict    recursiveResponseMap                    `json:"strict" validate:"required|length:1,2" doc:"严格字典"`
	Lax       recursiveResponseMap                    `json:"lax,omitempty"`
	Defaulted recursiveResponseMap                    `json:"defaulted,omitempty" default:"{}" doc:"缺省字典"`
	Patch     binding.Optional[*recursiveResponseMap] `json:"patch,omitempty" doc:"可选字典"`
	Nested    *struct {
		Rows binding.Optional[*recursiveResponseSlice] `json:"rows" validate:"required"`
	} `json:"nested,omitempty"`
}

type wrappedRecursiveOptional struct {
	binding.Optional[*wrappedRecursiveOptional]
}

func (wrappedRecursiveOptional) MarshalJSON() ([]byte, error) { return []byte(`"ready"`), nil }

type wrappedRecursiveCollection struct {
	binding.Optional[map[string]*wrappedRecursiveCollection]
}

func (wrappedRecursiveCollection) MarshalJSON() ([]byte, error) { return []byte(`"ready"`), nil }

type finiteOptionalWrapper struct{ binding.Optional[string] }

type nilOptionalStorage struct{ *binding.Optional[string] }
type nestedNilOptionalStorage struct{ *finiteOptionalWrapper }

// TestOptionalWrapperNeedsZeroValueStorage 保证启动期通过的 Optional 包装在零值请求中就能接收输入。
func TestOptionalWrapperNeedsZeroValueStorage(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[struct{ Value nilOptionalStorage }](),
		reflect.TypeFor[struct{ Value nestedNilOptionalStorage }](),
	} {
		if err := binding.ValidateType(typ); !errors.Is(err, binding.ErrDefinition) {
			t.Errorf("零值没有 Optional 存储的请求定义应失败：%s %v", typ, err)
		}
	}
}

type recursiveCollectionFields struct {
	Data recursiveResponseMap `json:"data"`
}

type wrappedCollectionFields struct {
	binding.Optional[recursiveCollectionFields]
}

func (wrappedCollectionFields) MarshalJSON() ([]byte, error) { return []byte(`"ready"`), nil }

type textRecursiveMap map[string]textRecursiveMap

func (textRecursiveMap) MarshalText() ([]byte, error) { return []byte("ready"), nil }

type textCollectionFields struct {
	Inner recursiveCollectionFields `json:"inner"`
}

func (textCollectionFields) MarshalText() ([]byte, error) { return []byte("ready"), nil }

// TestRecursiveOptionalWrapperRejectsUnknownJSON 由业务编码器决定 JSON 形状的递归包装不能借提升的方法生成错误契约。
func TestRecursiveOptionalWrapperRejectsUnknownJSON(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "包装类型", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		value    any
		register func(*Registry, string, string, string, int) error
	}{
		{wrappedRecursiveOptional{}, RegisterTyped[binding.Input, wrappedRecursiveOptional]},
		{wrappedRecursiveCollection{}, RegisterTyped[binding.Input, wrappedRecursiveCollection]},
		{wrappedCollectionFields{}, RegisterTyped[binding.Input, wrappedCollectionFields]},
		{textRecursiveMap{}, RegisterTyped[binding.Input, textRecursiveMap]},
		{textCollectionFields{}, RegisterTyped[binding.Input, textCollectionFields]},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil || string(encoded) != `"ready"` {
			t.Fatalf("业务编码器结果错误：%s %v", encoded, err)
		}
		typ := reflect.TypeOf(test.value)
		op, schemas, err := binding.Operation(reflect.TypeFor[binding.Input](), typ, "wrapper", http.StatusOK)
		if !errors.Is(err, binding.ErrDefinition) || op != nil || schemas != nil {
			t.Errorf("不明确的递归编码注册出了契约：%s %v", typ, err)
		}
		if _, _, err := binding.OperationWithDocumentation(reflect.TypeFor[binding.Input](), typ, "wrapper", http.StatusOK, binding.Documentation{}); !errors.Is(err, binding.ErrDefinition) {
			t.Errorf("带文档入口未拒绝 %s：%v", typ, err)
		}
		if err := test.register(registry, http.MethodGet, "/wrapper", "wrapper", http.StatusOK); !errors.Is(err, binding.ErrDefinition) {
			t.Errorf("注册表未拒绝 %s：%v", typ, err)
		}
	}
	type input struct {
		Value wrappedRecursiveCollection `json:"value"`
	}
	if err := RegisterTyped[input, string](registry, http.MethodPost, "/collection", "collection", http.StatusOK); !errors.Is(err, binding.ErrDefinition) {
		t.Errorf("未拒绝输入集合环上的自定义编码：%v", err)
	}
	if err := RegisterTyped[binding.Input, finiteOptionalWrapper](registry, http.MethodGet, "/wrapper", "wrapper", http.StatusOK); err != nil {
		t.Fatalf("非递归包装应保留既有契约，失败注册不得占用路由：%v", err)
	}
	type compatible struct {
		Finite finiteOptionalWrapper `json:"finite"`
		Other  recursiveResponseMap  `json:"other"`
	}
	if err := RegisterTyped[struct{ Data textRecursiveMap }, compatible](registry, http.MethodPost, "/compatible", "compatible", http.StatusOK); err != nil {
		t.Errorf("响应的兄弟字段或请求的纯文本编码能力不应改变有效契约：%v", err)
	}
}

// TestRecursiveFieldConstraintsSurviveRegistry 验证注册表克隆和导出后，字段修饰仍只约束各自的使用位置。
func TestRecursiveFieldConstraintsSurviveRegistry(t *testing.T) {
	registry, err := NewRegistry(openapi3.Info{Title: "递归字段", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[recursiveFieldContract, recursiveFieldContract](registry, http.MethodPatch, "/dict", "patch", http.StatusOK); err != nil {
		t.Fatal(err)
	}
	document, err := registry.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(document)
	if err != nil {
		t.Fatal(err)
	}
	patch := doc.Paths.Value("/dict").Patch
	request := patch.RequestBody.Value.Content["application/json"].Schema.Value
	response := patch.Responses.Status(http.StatusOK).Value.Content["application/json"].Schema.Value
	for _, schema := range []*openapi3.Schema{request, response} {
		properties := schema.Properties
		defaulted := properties["defaulted"].Value
		defaultValue, ok := defaulted.Default.(map[string]any)
		if !ok || len(defaultValue) != 0 || defaulted.Description != "缺省字典" || properties["strict"].Value.Description != "严格字典" || properties["patch"].Value.Description != "可选字典" {
			t.Errorf("字段的默认值或说明在发布后丢失：%v %q %q %q", defaulted.Default, defaulted.Description, properties["strict"].Value.Description, properties["patch"].Value.Description)
		}
	}
	for _, test := range []struct {
		body string
		ok   bool
	}{
		{`{"strict":{"a":null},"lax":null}`, true},
		{`{"strict":{"a":null},"lax":{},"patch":null}`, true},
		{`{"strict":{"a":null},"nested":{"rows":[null]},"patch":{"a":{}}}`, true},
		{`{"strict":null}`, false},
		{`{"strict":{}}`, false},
		{`{"strict":{"a":null,"b":null,"c":null}}`, false},
		{`{"strict":{"a":null},"nested":{"rows":null}}`, false},
		{`{"strict":{"a":null},"nested":{"rows":[]}}`, false},
		{`{"lax":{}}`, false},
	} {
		var value any
		if err := json.Unmarshal([]byte(test.body), &value); err != nil {
			t.Fatal(err)
		}
		in := httptest.NewRequest(http.MethodPatch, "/dict", strings.NewReader(test.body))
		in.Header.Set("Content-Type", "application/json")
		var bound recursiveFieldContract
		bindErr := binding.Bind(fwcontext.MustNewRequest(in), &bound)
		schemaErr := request.VisitJSON(value, openapi3.EnableJSONSchema2020())
		outputErr := response.VisitJSON(value, openapi3.EnableJSONSchema2020())
		if (bindErr == nil) != test.ok || (schemaErr == nil) != test.ok || (outputErr == nil) != test.ok {
			t.Errorf("%s 应通过=%t，Bind=%v，请求 Schema=%v，响应 Schema=%v", test.body, test.ok, bindErr, schemaErr, outputErr)
		}
		if test.ok && bound.Defaulted == nil {
			t.Errorf("%s 未应用默认字典", test.body)
		}
	}
}
