package driver

import (
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"
)

type privateMutableSnapshot struct{ data []byte }
type privateScalarSnapshot struct{ label string }

// TestMemoryRejectsPrivateMutableSnapshots 验证所有普通写入入口在失败时保留已有缓存。
func TestMemoryRejectsPrivateMutableSnapshots(t *testing.T) {
	for _, value := range []interface{}{
		big.NewInt(1234567), privateMutableSnapshot{data: []byte("secret")},
		map[string]interface{}{"nested": big.NewInt(1234567)},
		map[interface{}]int{big.NewInt(1): 1},
		[]privateMutableSnapshot{{data: []byte("nested")}},
		make(chan int), func() {},
	} {
		t.Run(reflect.TypeOf(value).String(), func(t *testing.T) {
			backend := NewMemory()
			if err := backend.Set("existing", "original", time.Minute); err != nil {
				t.Fatal(err)
			}
			for _, write := range []func() error{
				func() error { return backend.Set("existing", value, 0) },
				func() error {
					return backend.Update("existing", 0, func(interface{}, bool) (interface{}, bool, error) { return value, false, nil })
				},
			} {
				if err := write(); !errors.Is(err, ErrUnsupportedMemoryValue) {
					t.Fatalf("不安全的快照必须明确拒绝: %v", err)
				}
				got, found, err := backend.Get("existing")
				if err != nil || !found || got != "original" {
					t.Fatalf("失败写入损坏原缓存: got=%v found=%v err=%v", got, found, err)
				}
			}
		})
	}
}

// TestMemoryPreservesImmutablePrivateFields 验证不可变私有标量和时间值继续兼容。
func TestMemoryPreservesImmutablePrivateFields(t *testing.T) {
	backend := NewMemory()
	value := struct {
		Label privateScalarSnapshot
		Time  time.Time
	}{privateScalarSnapshot{label: "immutable"}, time.Now()}
	if err := backend.Set("value", value, 0); err != nil {
		t.Fatal(err)
	}
	got, found, err := backend.Get("value")
	if err != nil || !found || !reflect.DeepEqual(got, value) {
		t.Fatalf("不可变字段不应被拒绝或改写: got=%v found=%v err=%v", got, found, err)
	}
}

// TestMemoryPreservesImmutableInterfaceMapKeys 验证接口封装的不可变键保持原有兼容性。
func TestMemoryPreservesImmutableInterfaceMapKeys(t *testing.T) {
	backend := NewMemory()
	input := map[interface{}][]int{
		"name": {1}, 42: {2}, nil: {3}, struct{ Key interface{} }{Key: "nested"}: {4},
	}
	if err := backend.Set("keys", input, 0); err != nil {
		t.Fatal(err)
	}
	input["name"][0] = 99
	got, found, err := backend.Get("keys")
	if err != nil || !found || got.(map[interface{}][]int)["name"][0] != 1 || len(got.(map[interface{}][]int)) != len(input) {
		t.Fatalf("接口键丢失或值共享了原切片: got=%v found=%v err=%v", got, found, err)
	}
}
