package db

import (
	"errors"
	"reflect"
	"testing"
)

type textualPrimaryKey struct{ Text string }

func (key *textualPrimaryKey) UnmarshalText(value []byte) error {
	key.Text = string(value)
	if key.Text == "invalid" {
		return errors.New("invalid identifier")
	}
	return nil
}

type encodedPrimaryKey struct {
	Text string
	Err  error
}

func (key encodedPrimaryKey) MarshalText() ([]byte, error) { return []byte(key.Text), key.Err }

// TestPrimaryKeyTextBindingIsAtomic 验证标准文本接口支持自定义 ID，失败的解码不改变已持有的主键。
func TestPrimaryKeyTextBindingIsAtomic(t *testing.T) {
	model := struct {
		ID textualPrimaryKey `db:"id"`
	}{}
	binding, zero, err := preparePrimaryKeyBinding(reflect.ValueOf(&model).Elem(), "id", DriverCapabilities{InsertIDKinds: []InsertIDKind{InsertIDString}})
	if err != nil || !zero {
		t.Fatalf("自定义文本主键不受支持: %t %v", zero, err)
	}
	if err := binding.Assign("original"); err != nil || model.ID.Text != "original" {
		t.Fatalf("文本主键回填失败: %+v %v", model, err)
	}
	for _, invalid := range []any{"invalid", 42, nil} {
		if err := binding.Assign(invalid); !errors.Is(err, ErrInvalidModel) || model.ID.Text != "original" {
			t.Fatalf("非法标识符改变了调用方主键: %+v %v", model, err)
		}
	}
}

// TestPrimaryKeyTextEncodingPreservesFailure 验证驱动提供的标准文本 ID 可回填字符串，编码错误不会发布半成品。
func TestPrimaryKeyTextEncodingPreservesFailure(t *testing.T) {
	value := "original"
	binding := primaryKeyBinding{field: reflect.ValueOf(&value).Elem(), fieldName: "ID"}
	if err := binding.Assign(encodedPrimaryKey{Text: "identifier"}); err != nil || value != "identifier" {
		t.Fatalf("标准文本 ID 无法回填: %q %v", value, err)
	}
	if err := binding.Assign(encodedPrimaryKey{Text: "partial", Err: errors.New("encoding failed")}); !errors.Is(err, ErrInvalidModel) || value != "identifier" {
		t.Fatalf("编码失败改变了调用方主键: %q %v", value, err)
	}
}
