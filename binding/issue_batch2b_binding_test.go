package binding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/exception"
)

type rawMessageRequiredInput struct {
	Input
	Payload json.RawMessage `json:"payload" validate:"required"`
}

type rawMessageDefaultInput struct {
	Input
	Payload  json.RawMessage           `json:"payload" default:"{}" validate:"required"`
	Items    json.RawMessage           `json:"items" default:"[]"`
	Optional Optional[json.RawMessage] `json:"optional" default:"{\"ok\":true}"`
	Nullable *json.RawMessage          `json:"nullable" default:"null"`
}

type patchPasswordInput struct {
	Input
	Password        Optional[string] `json:"password" validate:"confirm:password_confirm"`
	PasswordConfirm Optional[string] `json:"password_confirm"`
}

type patchNestedNullCrossFieldInput struct {
	Input
	Credentials struct {
		Primary Optional[string] `json:"primary" validate:"different:backup"`
		Backup  Optional[string] `json:"backup"`
	} `json:"credentials"`
}

func TestIssue583RawMessageRequiredAndDefaultContract(t *testing.T) {
	operation, schemas, err := Operation(reflect.TypeFor[rawMessageRequiredInput](), nil, "rawRequired", http.StatusNoContent)
	if err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"openapi":    "3.1.0",
		"info":       map[string]string{"title": "Raw", "version": "1"},
		"paths":      map[string]any{"/raw": map[string]any{"post": operation}},
		"components": map[string]any{"schemas": schemas},
	}
	encodedDoc, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := openapi3.NewLoader().LoadFromData(encodedDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	schema := loaded.Paths.Value("/raw").Post.RequestBody.Value.Content["application/json"].Schema.Value

	validBodies := []struct {
		body string
		want string
	}{
		{`{"payload":{}}`, `{}`},
		{`{"payload":[]}`, `[]`},
		{`{"payload":""}`, `""`},
		{`{"payload":"   "}`, `"   "`},
		{`{"payload":0}`, `0`},
		{`{"payload":false}`, `false`},
	}
	for _, tc := range validBodies {
		var target rawMessageRequiredInput
		bindErr := Bind(bindingRequest(t, tc.body), &target)
		var decoded any
		_ = json.Unmarshal([]byte(tc.body), &decoded)
		schemaErr := schema.VisitJSON(decoded, openapi3.EnableJSONSchema2020())
		if bindErr != nil || schemaErr != nil {
			t.Fatalf("合法非 null RawMessage 被误拒 body=%s: bindErr=%v schemaErr=%v", tc.body, bindErr, schemaErr)
		}
		if string(target.Payload) != tc.want {
			t.Fatalf("RawMessage 绑定内容失真: got=%s want=%s", target.Payload, tc.want)
		}
	}

	for _, invalidBody := range []string{`{}`, `{"payload":null}`} {
		var target rawMessageRequiredInput
		bindErr := Bind(bindingRequest(t, invalidBody), &target)
		var decoded any
		_ = json.Unmarshal([]byte(invalidBody), &decoded)
		schemaErr := schema.VisitJSON(decoded, openapi3.EnableJSONSchema2020())
		var httpErr *exception.HttpException
		if !errors.As(bindErr, &httpErr) || httpErr.StatusCode != http.StatusUnprocessableEntity || schemaErr == nil {
			t.Fatalf("缺省或 null RawMessage 必须以 422 拒绝 body=%s: bindErr=%v schemaErr=%v", invalidBody, bindErr, schemaErr)
		}
	}

	// 验证 default 标签与 OpenAPI 导出一致
	var withDefaults rawMessageDefaultInput
	if err := Bind(bindingRequest(t, `{}`), &withDefaults); err != nil {
		t.Fatalf("RawMessage 默认值绑定失败: %v", err)
	}
	if string(withDefaults.Payload) != `{}` || string(withDefaults.Items) != `[]` ||
		!withDefaults.Optional.IsSet() || withDefaults.Optional.IsNull() || string(withDefaults.Optional.Value()) != `{"ok":true}` ||
		withDefaults.Nullable != nil {
		t.Fatalf("RawMessage 默认值结果异常: %#v", withDefaults)
	}
	_, defaultSchemas, err := Operation(reflect.TypeFor[rawMessageDefaultInput](), nil, "rawDefaults", http.StatusNoContent)
	if err != nil {
		t.Fatalf("导出带 default 的 RawMessage Schema 失败: %v", err)
	}
	if !bytes.Contains(defaultSchemas, []byte(`"default":{}`)) || !bytes.Contains(defaultSchemas, []byte(`"default":[]`)) {
		t.Fatalf("RawMessage 默认值未按原始 JSON 导出到 Schema: %s", defaultSchemas)
	}

	// default:"null" + validate:"required" 必须在计划编译阶段失败
	type invalidNullDefault struct {
		Input
		Payload json.RawMessage `json:"payload" default:"null" validate:"required"`
	}
	if err := ValidateType(reflect.TypeFor[invalidNullDefault]()); !errors.Is(err, ErrDefinition) {
		t.Fatalf("default:null 配合 required 必须返回 ErrDefinition，实际为 %v", err)
	}
}

func TestIssue583ExplicitNullParticipatesInCrossFieldValidation(t *testing.T) {
	// 1. 完全未传字段时跳过 confirm
	var omitted patchPasswordInput
	if err := Bind(bindingRequest(t, `{}`), &omitted); err != nil || omitted.Password.IsSet() {
		t.Fatalf("缺省 PATCH 字段不应报错: target=%#v err=%v", omitted, err)
	}

	// 2. 显式传入 null 但确认字段不一致或缺失时必须触发 422 confirm 错误
	for _, badBody := range []string{
		`{"password":null,"password_confirm":"mismatch"}`,
		`{"password":null}`,
	} {
		var target patchPasswordInput
		err := Bind(bindingRequest(t, badBody), &target)
		var httpErr *exception.HttpException
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("显式 null 跨字段不一致应返回 422 body=%s: err=%v", badBody, err)
		}
		if target.Password.IsSet() {
			t.Fatalf("校验失败不得污染目标结构体: %#v", target)
		}
	}

	// 3. 两侧同时显式传入 null 时通过 confirm 并保留 IsSet && IsNull
	var matchedNull patchPasswordInput
	if err := Bind(bindingRequest(t, `{"password":null,"password_confirm":null}`), &matchedNull); err != nil {
		t.Fatalf("两侧一致显式 null 应通过 confirm: %v", err)
	}
	if !matchedNull.Password.IsSet() || !matchedNull.Password.IsNull() {
		t.Fatalf("显式 null 三态标记错误: %#v", matchedNull)
	}

	// 4. 嵌套对象内的显式 null 同样参与 different 跨字段校验
	var nested patchNestedNullCrossFieldInput
	err := Bind(bindingRequest(t, `{"credentials":{"primary":null,"backup":null}}`), &nested)
	var httpErr *exception.HttpException
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("嵌套显式 null 违反 different 应返回 422，实际为 %v", err)
	}
	if err := Bind(bindingRequest(t, `{"credentials":{"primary":null,"backup":"fallback"}}`), &nested); err != nil {
		t.Fatalf("嵌套显式 null 满足 different 不应报错: %v", err)
	}
}

func TestIssue319SliceBindingElementLimitPreventsAmplification(t *testing.T) {
	type heavyRecord struct {
		Padding [1024]byte `json:"-"`
		V       int        `json:"v"`
	}
	type batchInput struct {
		Input
		Numbers []int         `json:"numbers"`
		Records []heavyRecord `json:"records"`
	}
	type querySliceInput struct {
		Input
		IDs []int `header:"X-Ids"`
	}

	// 1. 超过 maxSliceBindingElements 的 JSON 数组必须在预分配前拒绝为 400
	var buf bytes.Buffer
	buf.WriteString(`{"records":[`)
	for index := 0; index <= maxSliceBindingElements; index++ {
		if index > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{}`)
	}
	buf.WriteString(`]}`)

	rawReq := httptest.NewRequest(http.MethodPost, "/batch", &buf)
	rawReq.Header.Set("Content-Type", "application/json")
	req := fwcontext.MustNewRequest(rawReq)
	var target batchInput
	err := Bind(req, &target)
	var httpErr *exception.HttpException
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("超大 JSON 数组应返回 400 BadRequest，实际为 %v", err)
	}

	// 2. 非 body 来源（Header 逗号展开）同样受单字段元素上限约束
	headerReq := httptest.NewRequest(http.MethodGet, "/batch", nil)
	headerReq.Header.Set("X-Ids", strings.Repeat("1,", maxSliceBindingElements)+"1")
	var queryTarget querySliceInput
	err = Bind(fwcontext.MustNewRequest(headerReq), &queryTarget)
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("超大 Header 数组应返回 400 BadRequest，实际为 %v", err)
	}

	// 3. 正常规模批量请求正常通过
	var okTarget batchInput
	if err := Bind(bindingRequest(t, `{"numbers":[1,2,3]}`), &okTarget); err != nil || !reflect.DeepEqual(okTarget.Numbers, []int{1, 2, 3}) {
		t.Fatalf("正常数组绑定失败: target=%#v err=%v", okTarget, err)
	}

	// 4. OpenAPI 输入 Schema 同步导出 maxItems 上限，输出响应切片不附加输入上限
	type listResponse struct {
		Items []int `json:"items"`
	}
	_, components, err := Operation(reflect.TypeFor[batchInput](), reflect.TypeFor[listResponse](), "batchOp", http.StatusOK)
	if err != nil {
		t.Fatalf("导出包含切片的 OpenAPI Schema 失败: %v", err)
	}
	var schemaMap map[string]map[string]any
	if err := json.Unmarshal(components, &schemaMap); err != nil {
		t.Fatal(err)
	}
	foundInputMax, foundOutput := false, false
	for _, comp := range schemaMap {
		props, _ := comp["properties"].(map[string]any)
		if numbers, ok := props["numbers"].(map[string]any); ok {
			foundInputMax = true
			if numbers["maxItems"] != float64(maxSliceBindingElements) {
				t.Fatalf("输入切片 Schema 缺少默认 maxItems=%d: %#v", maxSliceBindingElements, numbers)
			}
		}
		if items, ok := props["items"].(map[string]any); ok {
			foundOutput = true
			if _, hasMax := items["maxItems"]; hasMax {
				t.Fatalf("输出切片 Schema 不应附加输入绑定上限: %#v", items)
			}
		}
	}
	if !foundInputMax || !foundOutput {
		t.Fatalf("未找到预期的输入或输出组件: foundInput=%t foundOutput=%t", foundInputMax, foundOutput)
	}
}
