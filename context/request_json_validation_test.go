package context

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestRequestJSONBindingAndParameterTree 验证独立原文绑定不构造值树，默认 Parse 仍一次构树。
func TestRequestJSONBindingAndParameterTree(t *testing.T) {
	const body = `{"profile":{"name":"alice"},"items":[1,2],"amount":18446744073709551616}`
	request := newJSONValidationRequest(body)
	var custom strictJSONCustomValue
	if err := request.Json(&custom); err != nil || custom.Raw != body {
		t.Fatalf("原文绑定必须保留自定义解码: raw=%q err=%v", custom.Raw, err)
	}
	if request.jsonBody != nil {
		t.Fatal("独立原文绑定不应缓存参数树")
	}
	if err := request.Parse(); err != nil || request.jsonBody == nil {
		t.Fatalf("默认 Parse 必须一次构造参数树: err=%v", err)
	}
	value := request.Param("amount")
	if value != "18446744073709551616" || request.jsonBody["amount"] != json.Number("18446744073709551616") {
		t.Fatalf("参数读取未保留精确数字: value=%#v", value)
	}
	sources, err := request.Sources()
	if err != nil {
		t.Fatal(err)
	}
	sources.Body["profile"].(map[string]any)["name"] = "changed"
	sources.Body["items"].([]any)[0] = json.Number("99")
	next, err := request.Sources()
	if err != nil || next.Body["profile"].(map[string]any)["name"] != "alice" || next.Body["items"].([]any)[0] != json.Number("1") {
		t.Fatalf("参数树被外部快照修改: next=%#v err=%v", next.Body, err)
	}
}

// TestRequestJSONParseRejectsBeforeBinding 验证默认参数解析仍在业务执行前拒绝全部严格边界。
func TestRequestJSONParseRejectsBeforeBinding(t *testing.T) {
	for _, body := range []string{`{"a":1,"\u0061":2}`, `{"nested":[{"x":1,"x":2}]}`, `{}`, `[]`, `null`, `{} {}`, `{"a":`, strings.Repeat(`{"x":`, maxJSONNestingDepth+1) + "0" + strings.Repeat("}", maxJSONNestingDepth+1)} {
		request := newJSONValidationRequest(body)
		parseErr := request.Parse()
		if (parseErr == nil) != (body == `{}`) {
			t.Fatalf("请求前严格校验结果改变: body=%q err=%v", body, parseErr)
		}
		if parseErr != nil && request.jsonBody != nil {
			t.Fatalf("失败解析暴露了部分参数树: body=%q", body)
		}
		if parseErr != nil && !errors.Is(parseErr, ErrInvalidJSONBody) {
			t.Fatalf("严格错误分类改变: %v", parseErr)
		}
	}
	// 独立 Json 绑定支持非对象根；HTTP 参数校验的根约束不能污染该接口。
	request := newJSONValidationRequest(`[1,2]`)
	_ = request.Parse()
	var values []int
	if err := request.Json(&values); err != nil || len(values) != 2 {
		t.Fatalf("独立目标绑定的数组根语义改变: values=%v err=%v", values, err)
	}
}

// TestRequestJSONOverrideBindingCache 验证独立绑定、默认解析和替换输入不会复用旧状态。
func TestRequestJSONOverrideBindingCache(t *testing.T) {
	request := newJSONValidationRequest(`{"original":true}`)
	request.WithInput(`{"current":{"value":1}}`)
	var custom strictJSONCustomValue
	if err := request.Json(&custom); err != nil || custom.Raw != `{"current":{"value":1}}` || request.thinkPHPState().inputValues != nil {
		t.Fatalf("覆盖输入应从同一验证快照绑定: raw=%q err=%v", custom.Raw, err)
	}
	if err := request.Parse(); err != nil || request.thinkPHPState().inputValues == nil || !request.Has("current") {
		t.Fatalf("默认 Parse 必须缓存当前覆盖输入的参数树: err=%v", err)
	}
	request.WithInput(`{"next":2}`)
	if request.thinkPHPState().inputValues != nil {
		t.Fatal("覆盖输入未丢弃旧参数树")
	}
	if err := request.Parse(); err != nil || request.thinkPHPState().inputValues == nil {
		t.Fatalf("新输入错误复用了旧树: err=%v", err)
	}
	if request.Has("current") || request.ParamInt("next", 0) != 2 {
		t.Fatal("替换输入后仍读取到旧字段")
	}
	request.WithInput(`{"bad":1,"bad":2}`)
	if err := request.ParseError(); !errors.Is(err, ErrInvalidJSONBody) {
		t.Fatalf("错误查询必须验证当前覆盖输入: %v", err)
	}
	request.WithInput("field=value")
	request.WithHeader(map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if err := request.Parse(); err != nil || request.Post("field") != "value" {
		t.Fatalf("JSON 切换表单后状态未刷新: err=%v", err)
	}
	request.WithHeader(map[string]string{"Content-Type": "application/json"})
	if err := request.JSONError(); !errors.Is(err, ErrInvalidJSONBody) {
		t.Fatalf("媒体切换必须重新校验覆盖输入: %v", err)
	}
}

// TestRequestJSONCachesConcurrent 验证读取原文、默认解析和参数访问不会共享可变输出或竞争缓存。
func TestRequestJSONCachesConcurrent(t *testing.T) {
	for _, override := range []bool{false, true} {
		request := newJSONValidationRequest(`{"name":"alice","items":[1,2]}`)
		if override {
			request.WithInput(`{"name":"alice","items":[1,2]}`)
		}
		var workers sync.WaitGroup
		for range 24 {
			workers.Go(func() {
				for range 30 {
					if err := request.ValidateBody(); err != nil {
						t.Error(err)
						return
					}
					if err := request.Parse(); err != nil {
						t.Error(err)
						return
					}
					var body map[string]any
					if err := request.Json(&body); err != nil || body["name"] != "alice" {
						t.Errorf("并发原文绑定失败: body=%#v err=%v", body, err)
						return
					}
					sources, err := request.Sources()
					if err != nil || sources.Body["name"] != "alice" {
						t.Errorf("并发快照读取失败: body=%#v err=%v", sources.Body, err)
						return
					}
					sources.Body["name"] = "external"
					if request.Post("name") != "alice" {
						t.Error("外部快照修改了请求状态")
					}
				}
			})
		}
		workers.Wait()
	}
}

// TestRequestJSONOverrideValidationSnapshot 验证并发替换正文不会让重复键绕过自定义解码前的校验。
func TestRequestJSONOverrideValidationSnapshot(t *testing.T) {
	const valid = `{"role":"user"}`
	const invalid = `{"role":"user","\u0072ole":"admin"}`
	request := newJSONValidationRequest(valid)
	request.WithInput(valid)
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 500 {
			request.WithInput(invalid)
			request.WithInput(valid)
		}
	})
	for range 8 {
		workers.Go(func() {
			for range 200 {
				var target strictJSONCustomValue
				err := request.Json(&target)
				if err == nil && target.Raw != valid {
					t.Errorf("绑定复用了另一版正文的校验结果: raw=%q", target.Raw)
					return
				}
				if err != nil && (!errors.Is(err, ErrInvalidJSONBody) || target.Raw != "") {
					t.Errorf("非法正文必须在自定义解码前被拒绝: raw=%q err=%v", target.Raw, err)
					return
				}
			}
		})
	}
	workers.Wait()
}

// FuzzStrictJSONValidationMatchesTokenReference 检查无值树校验与既有 Token 语义接受相同的文档。
func FuzzStrictJSONValidationMatchesTokenReference(f *testing.F) {
	for _, body := range []string{
		strictJSONCheckoutBody, `{"a":1,"\u0061":2}`, `{"𝄞":1,"\uD834\uDD1E":2}`,
		"{\"\xff\":1,\"\\ufffd\":2}", `"\uD800"`, "\"\xff\"", `[{},null,1e1000]`,
		`{}`, `[]`, `null`, `{} {}`, `{"x":"a\\\"b"}`, `{"x":"\x41"}`,
		strings.Repeat("[", maxJSONNestingDepth) + "{}" + strings.Repeat("]", maxJSONNestingDepth),
		strings.Repeat("[", maxJSONNestingDepth+1) + "{}" + strings.Repeat("]", maxJSONNestingDepth+1),
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		want, wantErr := referenceStrictJSONValue(body)
		gotErr := validateStrictJSONDocument(body, false)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("只校验路径接受范围改变: body=%q got=%v want=%v", body, gotErr, wantErr)
		}
		_, object := want.(map[string]any)
		objectErr := validateStrictJSONDocument(body, true)
		if (objectErr == nil) != (wantErr == nil && object) {
			t.Fatalf("对象根约束改变: body=%q got=%v want=%v object=%v", body, objectErr, wantErr, object)
		}
	})
}

func newJSONValidationRequest(body string) *Request {
	raw := httptest.NewRequest(http.MethodPost, "/json", strings.NewReader(body))
	raw.Header.Set("Content-Type", "application/json")
	return MustNewRequest(raw)
}
