package binding

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	fwcontext "github.com/zhuhanxin0308/thinkgo/framework/context"
	"github.com/zhuhanxin0308/thinkgo/framework/exception"
	"github.com/zhuhanxin0308/thinkgo/framework/validate"
)

type TreeInput struct {
	Value int        `json:"value"`
	Next  *TreeInput `json:"next"`
}
type EmbeddedInput struct {
	Name string `json:"name" validate:"required"`
}

type opaqueJSONValue struct{ Value string }

func (*opaqueJSONValue) UnmarshalJSON([]byte) error { return nil }

// TestBindCompositeValuesAndDefaults 验证时间、自定义数值宽度、字典和匿名嵌入与 JSON 编解码语义一致。
func TestBindCompositeValuesAndDefaults(t *testing.T) {
	var target struct {
		*EmbeddedInput
		Created time.Time       `json:"created"`
		Values  map[string]*int `json:"values"`
		Pair    [2]uint8        `json:"pair"`
		Ratio   float32         `query:"ratio" default:"0.5"`
		Tree    *TreeInput      `json:"tree"`
	}
	req := bindingRequest(t, `{"name":"Ada","created":"2026-09-06T00:00:00Z","values":{"x":1,"empty":null},"pair":[1,255],"tree":{"value":1,"next":{"value":2}}}`)
	if err := Bind(req, &target); err != nil {
		t.Fatal(err)
	}
	if target.EmbeddedInput == nil || target.Name != "Ada" || target.Created.Year() != 2026 || *target.Values["x"] != 1 || target.Values["empty"] != nil || target.Pair != [2]uint8{1, 255} || target.Ratio != 0.5 || target.Tree.Next.Value != 2 {
		t.Fatalf("复合绑定错误: %#v", target)
	}
	for _, body := range []string{`{"name":"Ada","created":"bad"}`, `{"name":"Ada","pair":[1]}`, `{"name":"Ada","values":[]}`, `{"name":"Ada","tree":12}`} {
		if err := Bind(bindingRequest(t, body), &target); err == nil {
			t.Fatalf("非法复合字段被接受: %s", body)
		}
	}
}

// TestBindFormHeaderCookieAndPathArrays 验证编码来源不会混用，数组遵循各来源声明的序列化方式。
func TestBindFormHeaderCookieAndPathArrays(t *testing.T) {
	var target struct {
		Name     string `form:"name" validate:"required"`
		IDs      []int  `form:"id"`
		Segments []int  `path:"segments"`
		Flags    []bool `header:"X-Flags"`
		Session  string `cookie:"session"`
	}
	raw := httptest.NewRequest(http.MethodPost, "/?name=ignored", strings.NewReader("name=Ada&id=2&id=3"))
	raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	raw.Header.Set("X-Flags", "true,false")
	raw.AddCookie(&http.Cookie{Name: "session", Value: "cookie-value"})
	req := fwcontext.MustNewRequest(raw)
	req.SetRoute("segments", "4,5")
	if err := Bind(req, &target); err != nil {
		t.Fatal(err)
	}
	if target.Name != "Ada" || !reflect.DeepEqual(target.IDs, []int{2, 3}) || !reflect.DeepEqual(target.Segments, []int{4, 5}) || !reflect.DeepEqual(target.Flags, []bool{true, false}) || target.Session != "cookie-value" {
		t.Fatalf("来源绑定错误: %#v", target)
	}
	for _, body := range []string{"name=Ada&extra=true", "name=%xx"} {
		raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		raw.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := Bind(fwcontext.MustNewRequest(raw), &target); err == nil {
			t.Fatalf("非法表单被接受: %s", body)
		}
	}
}

// TestDefinitionErrorsFailBeforeUserData 验证类型、字段歧义和默认值错误不会被误归类为用户输入错误。
func TestDefinitionErrorsFailBeforeUserData(t *testing.T) {
	definitions := []any{
		struct {
			Value opaqueJSONValue `json:"value"`
		}{},
		time.Time{},
		Optional[string]{},
		struct {
			X string `json:"x"`
			Y string `form:"y"`
		}{},
		reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "X", Type: reflect.TypeFor[string](), Tag: `json:"x"`},
			{Name: "Y", Type: reflect.TypeFor[int](), Tag: `json:"x"`},
		})).Elem().Interface(),
		struct {
			X string `path:"x" query:"x"`
		}{},
		struct {
			X string `header:"bad name"`
		}{},
		struct {
			X string `header:"x-a"`
			Y string `header:"X-A"`
		}{},
		struct {
			X func() `json:"x"`
		}{},
		struct {
			X map[int]string `json:"x"`
		}{},
		struct {
			X map[string]string `query:"x"`
		}{},
		reflect.New(reflect.StructOf([]reflect.StructField{
			{Name: "x", PkgPath: "binding", Type: reflect.TypeFor[string](), Tag: `json:"x"`},
		})).Elem().Interface(),
		struct {
			x string `query:"x"`
		}{},
		struct {
			X int `json:"x" default:"1 2"`
		}{},
		struct {
			X int `json:"x" default:"-1" validate:"gt:0"`
		}{},
		struct {
			X int `json:"x" default:"null"`
		}{},
		struct {
			X string `json:"x" validate:"unknown"`
		}{},
		struct {
			X string `query:""`
		}{},
	}
	for _, definition := range definitions {
		typ := reflect.TypeOf(definition)
		if err := ValidateType(typ); !errors.Is(err, ErrDefinition) {
			t.Fatalf("非法定义应明确失败 %s: %v", typ, err)
		}
	}
	if err := ValidateType(nil); !errors.Is(err, ErrDefinition) {
		t.Fatal(err)
	}
	if err := ValidateType(reflect.TypeFor[string]()); !errors.Is(err, ErrDefinition) {
		t.Fatal(err)
	}
	var target struct {
		Value int `json:"value" validate:"gt:0"`
	}
	if err := Bind(bindingRequest(t, `{}`), &target, validate.WithLocation(nil)); !errors.Is(err, ErrDefinition) {
		t.Fatalf("无效选项分类错误: %v", err)
	}
	for _, bad := range []any{nil, target, (*int)(nil), new(int)} {
		if err := Bind(bindingRequest(t, `{}`), bad); !errors.Is(err, ErrDefinition) {
			t.Fatalf("绑定目标错误: %v", err)
		}
	}
	if err := Bind(nil, &target); !errors.Is(err, ErrDefinition) {
		t.Fatal(err)
	}
}

// TestBodyLimitAndPointerDefaults 验证请求体限制映射为 413，指针时间默认值保持可解析且不共享状态。
func TestBodyLimitAndPointerDefaults(t *testing.T) {
	var target struct {
		Created *time.Time `json:"created" default:"2026-09-06T00:00:00Z"`
		Name    string     `json:"name"`
	}
	if err := Bind(bindingRequest(t, `{}`), &target); err != nil || target.Created == nil || target.Created.Year() != 2026 {
		t.Fatalf("时间默认值错误: %#v %v", target, err)
	}
	raw := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"too long"}`))
	raw.Header.Set("Content-Type", "application/json")
	const bodyLimit = 8
	req := fwcontext.MustNewRequest(raw, fwcontext.WithMaxBodyBytes(bodyLimit))
	var failure *exception.HttpException
	if err := Bind(req, &target); !errors.As(err, &failure) || failure.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("请求体限制分类错误: %v", err)
	}
	if target.Created == nil || target.Created.Year() != 2026 {
		t.Fatal("失败请求改变已有值")
	}
}

// TestBindBytesAndNumericParameters 验证字节字段遵循 Go JSON 的 base64 语义，整数参数允许合法十进制写法。
func TestBindBytesAndNumericParameters(t *testing.T) {
	var target struct {
		Data  []byte `json:"data"`
		Count int    `query:"count"`
	}
	req := bindingRequest(t, `{"data":"aGVsbG8="}`)
	req.Raw().URL.RawQuery = "count=%2B007"
	if err := Bind(req, &target); err != nil || string(target.Data) != "hello" || target.Count != 7 {
		t.Fatalf("字节或整数绑定不一致: %#v %v", target, err)
	}
	if err := Bind(bindingRequest(t, `{"data":"invalid base64!"}`), &target); err == nil {
		t.Fatal("非法字节编码被接受")
	}
}

// TestNonBodyFieldsCanBeHiddenFromJSON 验证路径和查询来源不受 JSON 输出忽略标签影响。
func TestNonBodyFieldsCanBeHiddenFromJSON(t *testing.T) {
	var target struct {
		ID     int    `path:"id" json:"-"`
		Page   int    `query:"page" json:"-"`
		Hidden string `json:"-"`
	}
	if err := Bind(bindingRequest(t, `{}`), &target); err != nil || target.ID != 42 || target.Page != 2 {
		t.Fatalf("来源标签被序列化标签覆盖: %#v %v", target, err)
	}
	encoded, err := json.Marshal(target)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("输出忽略规则失效: %s %v", encoded, err)
	}
}

// TestJSONSpecialTypesPreserveWireValues 验证精确数值与原始 JSON 不会被误当普通字符串或 base64 字节。
func TestJSONSpecialTypesPreserveWireValues(t *testing.T) {
	var target struct {
		Amount json.Number     `json:"amount"`
		Extra  json.RawMessage `json:"extra"`
	}
	const amount = "9007199254740993.125"
	if err := Bind(bindingRequest(t, `{"amount":`+amount+`,"extra":{"enabled":false}}`), &target); err != nil || target.Amount.String() != amount || string(target.Extra) != `{"enabled":false}` {
		t.Fatalf("JSON 特殊类型失真: %#v %v", target, err)
	}
	if err := Bind(bindingRequest(t, `{"amount":"123"}`), &target); err == nil {
		t.Fatal("数值字段不应接受字符串")
	}
}

// TestOptionalJSONRoundTripAndFailure 验证可选字段编解码与失败后的既有状态保持。
func TestOptionalJSONRoundTripAndFailure(t *testing.T) {
	var value Optional[int]
	if !value.IsZero() {
		t.Fatal("新字段必须未设置")
	}
	for _, text := range []string{"0", "42", "null"} {
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil || string(encoded) != text || !value.IsSet() {
			t.Fatalf("三态序列化错误: %s %v", encoded, err)
		}
	}
	if err := json.Unmarshal([]byte("42"), &value); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`"bad"`), &value); err == nil || value.Value() != 42 {
		t.Fatal("失败解码覆盖已有值")
	}
	encoded, err := json.Marshal(Optional[int]{})
	if err != nil || string(encoded) != "null" {
		t.Fatalf("未设置字段序列化错误: %s %v", encoded, err)
	}
}

// TestBindLimitsAndMalformedSources 验证异常输入受边界控制，并保留准确的客户端错误分类。
func TestBindLimitsAndMalformedSources(t *testing.T) {
	var target struct {
		Tree  *TreeInput `json:"tree"`
		Count uint8      `query:"count"`
	}
	for _, query := range []string{"count=-1", "count=256", "count=%xx"} {
		req := bindingRequest(t, `{}`)
		req.Raw().URL.RawQuery = query
		if err := Bind(req, &target); err == nil {
			t.Fatalf("非法查询参数被接受: %s", query)
		}
	}
	deep := `{"tree":` + strings.Repeat(`{"next":`, maximumDepth) + `{}` + strings.Repeat(`}`, maximumDepth) + `}`
	if err := Bind(bindingRequest(t, deep), &target); err == nil {
		t.Fatal("过深输入未被拒绝")
	}
	req := bindingRequest(t, `{}`)
	req.Raw().Header.Set("Content-Type", "text/plain")
	var failure *exception.HttpException
	if err := Bind(req, &target); !errors.As(err, &failure) || failure.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("内容类型错误分类: %v", err)
	}
}

// TestConcurrentBindingsAreIsolated 验证编译计划可共享，输入对象与验证结果不会跨请求污染。
func TestConcurrentBindingsAreIsolated(t *testing.T) {
	const requests = 32
	var wait sync.WaitGroup
	for index := 0; index < requests; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var target struct {
				Name string         `json:"name" validate:"required"`
				Flag Optional[bool] `json:"flag"`
			}
			if err := Bind(bindingRequest(t, `{"name":"isolated","flag":false}`), &target); err != nil || target.Name != "isolated" || !target.Flag.IsSet() {
				t.Errorf("并发请求污染: %#v %v", target, err)
			}
		}()
	}
	wait.Wait()
}
