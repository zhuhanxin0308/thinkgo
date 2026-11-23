package binding

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type recursiveSchemaMap map[string]recursiveSchemaList
type recursiveSchemaList []Optional[*recursiveSchemaMap]
type recursiveSchemaArray [1]*recursiveSchemaArray
type optionalSchemaPointer *Optional[optionalSchemaPointer]

// TestRecursiveElementClassification 验证只标记元素链自身的环；经结构体字段递归时沿用对象组件。
func TestRecursiveElementClassification(t *testing.T) {
	for _, test := range []struct {
		typ   reflect.Type
		cycle elementCycle
	}{
		{reflect.TypeFor[recursiveSchemaMap](), collectionCycle},
		{reflect.TypeFor[recursiveSchemaList](), collectionCycle},
		{reflect.TypeFor[recursiveSchemaArray](), collectionCycle},
		{reflect.TypeFor[recursiveCallPointer](), nullCycle},
		{reflect.TypeFor[optionalSchemaPointer](), nullCycle},
		{reflect.TypeFor[[]recursiveSchemaMap](), noElementCycle},
		{reflect.TypeFor[*TreeInput](), noElementCycle},
		{reflect.TypeFor[[]*int](), noElementCycle},
	} {
		plan, err := compileNode(test.typ, false, true, make(map[planKey]*node), 0)
		if err != nil {
			t.Fatal(err)
		}
		if plan.elementCycle != test.cycle {
			t.Errorf("%s 的元素递归分类为 %d，预期 %d", test.typ, plan.elementCycle, test.cycle)
		}
	}
}

type recursiveSchemaFields struct {
	Mixed  Optional[*recursiveSchemaMap] `json:"mixed"`
	Fixed  recursiveSchemaArray          `json:"fixed,omitempty"`
	Never  recursiveCallPointer          `json:"never" default:"null"`
	Finite []map[string]int              `json:"finite,omitempty"`
}

// TestRecursiveSchemaContracts 验证组件名称跨入口稳定，输入和输出保持各自的长度、注释和可空契约。
func TestRecursiveSchemaContracts(t *testing.T) {
	typ := reflect.TypeFor[recursiveSchemaFields]()
	prefix := typ.PkgPath() + "."
	docs := Documentation{
		prefix + typ.Name(): {Inline: map[string]TypeDocumentation{
			"Mixed": {Description: "匿名对象说明"}, "Never": {Description: "匿名对象说明"},
		}},
		prefix + "recursiveSchemaMap":   {Description: "递归字典"},
		prefix + "recursiveCallPointer": {Description: "可空指针"},
	}
	operation, schemas, err := OperationWithDocumentation(typ, typ, "recursive", 200, docs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"openapi": "3.1.0", "info": map[string]string{"title": "API", "version": "1"},
		"paths": map[string]any{"/recursive": map[string]any{"post": operation}}, "components": map[string]any{"schemas": schemas},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	post := doc.Paths.Value("/recursive").Post
	request := post.RequestBody.Value.Content["application/json"].Schema.Value
	response := post.Responses.Status(200).Value.Content["application/json"].Schema.Value
	for _, object := range []*openapi3.Schema{request, response} {
		mixed := object.Properties["mixed"].Value.AnyOf[0].Value.AnyOf[0]
		list := mixed.Value.AdditionalProperties.Schema
		back := list.Value.Items.Value.AnyOf[0].Value.AnyOf[0]
		if mixed.Ref == "" || back.Ref != mixed.Ref || mixed.Value.Description != "递归字典" || list.Ref == "" {
			t.Fatalf("双向组件引用或类型说明不符：map=%q list=%q back=%q", mixed.Ref, list.Ref, back.Ref)
		}
		if object == request && (list.Value.MaxItems == nil || *list.Value.MaxItems != maxSliceBindingElements) || object == response && list.Value.MaxItems != nil {
			t.Fatalf("输入和输出的列表长度约束不符：%v", list.Value.MaxItems)
		}
		fixed := object.Properties["fixed"]
		if fixed.Value.MinItems != 1 || fixed.Value.MaxItems == nil || *fixed.Value.MaxItems != 1 || fixed.Value.Items.Value.AnyOf[0].Ref != fixed.Ref {
			t.Fatal("递归数组未保留固定长度或元素引用")
		}
		finite := object.Properties["finite"]
		if finite.Ref != "" || finite.Value.Items.Ref != "" || !finite.Value.Items.Value.AdditionalProperties.Schema.Value.Type.Is("integer") {
			t.Fatal("有限容器应保留原有内联结构")
		}
		if nullable := object.Properties["never"].Value; !nullable.Type.Is("null") || nullable.Description != "可空指针" {
			t.Fatal("纯指针环未保留 null 与类型说明")
		}
	}
	for _, payload := range []string{`{"mixed":{"next":[{},null]}}`, `{"never":null,"fixed":[null]}`, `{"never":3}`, `{"mixed":{"next":"不合法"}}`} {
		var value any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatal(err)
		}
		var input recursiveSchemaFields
		bindErr := Bind(bindingRequest(t, payload), &input)
		schemaErr := request.VisitJSON(value, openapi3.EnableJSONSchema2020())
		if (bindErr == nil) != (schemaErr == nil) {
			t.Fatalf("%s 的请求绑定与 Schema 结论不同：%v，%v", payload, bindErr, schemaErr)
		}
	}
}
