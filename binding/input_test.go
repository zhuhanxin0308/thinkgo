package binding

import (
	"errors"
	"reflect"
	"testing"
)

type markedInput struct {
	Input
	Name string `json:"name" validate:"required"`
}
type invalidMarkedInput struct {
	Input
	Value int `query:"value" default:"bad"`
}

// TestInputMarkerAndSignature 验证显式请求类型、服务隔离、错误定义和单请求契约约束。
func TestInputMarkerAndSignature(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[markedInput](), reflect.TypeFor[*markedInput]()} {
		if !IsInputType(typ) || ValidateType(typ) != nil {
			t.Fatalf("请求标记未识别: %s", typ)
		}
	}
	if IsInputType(nil) || IsInputType(reflect.TypeFor[struct{ Name string }]()) {
		t.Fatal("普通服务不能被识别为请求")
	}
	var input markedInput
	if err := Bind(bindingRequest(t, `{"name":"Ada"}`), &input); err != nil || input.Name != "Ada" {
		t.Fatalf("标记干扰字段绑定: %#v %v", input, err)
	}
	if err := ValidateSignature(reflect.TypeOf(func(markedInput, *int) {})); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []reflect.Type{nil, reflect.TypeFor[int](), reflect.TypeOf(func(invalidMarkedInput) {}), reflect.TypeOf(func(markedInput, *markedInput) {}), reflect.TypeOf(func(**markedInput) {}), reflect.TypeOf(func(...markedInput) {})} {
		if err := ValidateSignature(typ); !errors.Is(err, ErrDefinition) {
			t.Fatalf("错误签名未拒绝: %v %v", typ, err)
		}
	}
}
