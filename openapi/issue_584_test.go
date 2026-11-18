package openapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	"github.com/zhuhanxin0308/thinkgo/v3/route"
)

type issue584GenericEnvelope[T any] struct {
	Data T `json:"data"`
}

func registerIssue584LocalA(api *API) error {
	type localReq struct {
		binding.Input
		UserName string `json:"user_name" validate:"required"`
	}
	type localItem struct {
		UserID string `json:"user_id"`
	}
	return api.Handle(
		Operation{Method: http.MethodPost, Path: "/a", OperationID: "opA"},
		func(in localReq) (issue584GenericEnvelope[localItem], error) {
			return issue584GenericEnvelope[localItem]{Data: localItem{UserID: in.UserName}}, nil
		},
	)
}

func registerIssue584LocalB(api *API) error {
	type localReq struct {
		binding.Input
		OrderID int64 `json:"order_id" validate:"required|gt:0"`
	}
	type localItem struct {
		Amount int64 `json:"amount"`
	}
	return api.Handle(
		Operation{Method: http.MethodPost, Path: "/b", OperationID: "opB"},
		func(in localReq) (issue584GenericEnvelope[localItem], error) {
			return issue584GenericEnvelope[localItem]{Data: localItem{Amount: in.OrderID}}, nil
		},
	)
}

// TestIssue584LocalStructsAndGenericInstantiationsDoNotCollide 验证同包不同函数内的同名局部结构体
// 以及包裹同名局部类型的泛型实例能够生成互不冲突的确定性组件名。
func TestIssue584LocalStructsAndGenericInstantiationsDoNotCollide(t *testing.T) {
	router := route.NewRouter()
	registry, err := NewRegistry(openapi3.Info{Title: "Issue584", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	api := registry.Routes(router)
	if err := registerIssue584LocalA(api); err != nil {
		t.Fatalf("注册 opA 失败: %v", err)
	}
	if err := registerIssue584LocalB(api); err != nil {
		t.Fatalf("注册 opB 不应发生 ThinkGo_<hash> 冲突: %v", err)
	}

	raw, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatalf("导出 OpenAPI JSON 失败: %v", err)
	}
	text := string(raw)
	for _, expected := range []string{`"user_name"`, `"order_id"`, `"user_id"`, `"amount"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("OpenAPI 文档缺失字段 %s: %s", expected, text)
		}
	}
}

type issue584CustomJSONString string

// TestIssue584JSONNumberAndDerivedStringUseApplicationJSON 验证 RegisterTyped 对 json.Number
// 及非原生 string 的具名字符串衍生类型声明为 application/json，而原生 string 保持 text/plain。
func TestIssue584JSONNumberAndDerivedStringUseApplicationJSON(t *testing.T) {
	type emptyIn struct {
		binding.Input
	}
	registry, err := NewRegistry(openapi3.Info{Title: "MediaTypes", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterTyped[emptyIn, json.Number](registry, http.MethodGet, "/num", "getNum", http.StatusOK); err != nil {
		t.Fatalf("注册 json.Number 输出失败: %v", err)
	}
	if err := RegisterTyped[emptyIn, issue584CustomJSONString](registry, http.MethodGet, "/code", "getCode", http.StatusOK); err != nil {
		t.Fatalf("注册具名字符串衍生类型输出失败: %v", err)
	}
	if err := RegisterTyped[emptyIn, string](registry, http.MethodGet, "/plain", "getPlain", http.StatusOK); err != nil {
		t.Fatalf("注册原生 string 输出失败: %v", err)
	}

	raw, err := registry.JSON(context.Background())
	if err != nil {
		t.Fatalf("校验并导出 OpenAPI 文档失败: %v", err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(raw)
	if err != nil {
		t.Fatal(err)
	}

	numContent := doc.Paths.Value("/num").Get.Responses.Status(http.StatusOK).Value.Content
	if numContent["text/plain"] != nil || numContent["application/json"] == nil || !numContent["application/json"].Schema.Value.Type.Is("number") {
		t.Fatalf("/num 响应媒体类型或 Schema 错误: %#v", numContent)
	}

	codeContent := doc.Paths.Value("/code").Get.Responses.Status(http.StatusOK).Value.Content
	if codeContent["text/plain"] != nil || codeContent["application/json"] == nil || !codeContent["application/json"].Schema.Value.Type.Is("string") {
		t.Fatalf("/code 响应媒体类型或 Schema 错误: %#v", codeContent)
	}

	plainContent := doc.Paths.Value("/plain").Get.Responses.Status(http.StatusOK).Value.Content
	if plainContent["text/plain"] == nil || plainContent["application/json"] != nil {
		t.Fatalf("/plain 原生 string 应保持 text/plain 契约: %#v", plainContent)
	}
}
