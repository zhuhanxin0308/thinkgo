// Package binding 将显式声明的请求字段绑定、验证为独立的结构体快照。
package binding

import (
	"bytes"
	"encoding/json"
)

// Optional 保存未提交、显式 null 和具体值，供 PATCH 接口区分清空与保持原值。
type Optional[T any] struct {
	value T
	set   bool
	null  bool
}

// IsSet 返回客户端是否提交该字段，显式 null 也属于已提交。
func (o Optional[T]) IsSet() bool { return o.set }

// IsNull 返回客户端是否显式提交 null。
func (o Optional[T]) IsNull() bool { return o.set && o.null }

// Value 返回字段值；调用方应先根据 IsSet、IsNull 选择更新或清空操作。
func (o Optional[T]) Value() T { return o.value }

// IsZero 支持 JSON omitzero，使未提交字段不出现在序列化结果中。
func (o Optional[T]) IsZero() bool { return !o.set }

// MarshalJSON 按字段实际值序列化，缺失值和显式空值均输出 null。
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if !o.set || o.null {
		return []byte("null"), nil
	}
	return json.Marshal(o.value)
}

// UnmarshalJSON 原子更新字段值，解码失败时保留之前的状态。
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	next := Optional[T]{set: true, null: bytes.Equal(bytes.TrimSpace(data), []byte("null"))}
	if !next.null {
		if err := json.Unmarshal(data, &next.value); err != nil {
			return err
		}
	}
	*o = next
	return nil
}

type optionalValue interface {
	valuePointer() any
	setPresence(bool)
}

func (o *Optional[T]) valuePointer() any     { return &o.value }
func (o *Optional[T]) setPresence(null bool) { o.set, o.null = true, null }
