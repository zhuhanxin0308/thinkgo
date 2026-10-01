package driver

import (
	"fmt"
	"reflect"
	"testing"
)

type memorySnapshotCount int64
type memorySnapshotCounts []memorySnapshotCount

// TestMemoryScalarSliceSnapshots 验证所有可批量复制的标量切片仍返回同类型独立快照。
func TestMemoryScalarSliceSnapshots(t *testing.T) {
	values := []interface{}{
		[]bool{true, false}, []int{1, 2}, []int8{1, 2}, []int16{1, 2}, []int32{1, 2}, []int64{1, 2},
		[]uint{1, 2}, []uint8{1, 2}, []uint16{1, 2}, []uint32{1, 2}, []uint64{1, 2}, []uintptr{1, 2},
		[]float32{1.5, 2.5}, []float64{1.5, 2.5}, []complex64{1 + 2i, 3 + 4i}, []complex128{1 + 2i, 3 + 4i},
		[]string{"before", "stable"}, memorySnapshotCounts{1, 2}, []int(nil), []int{},
	}
	for _, input := range values {
		t.Run(fmt.Sprintf("%T/len_%d/nil_%t", input, reflect.ValueOf(input).Len(), reflect.ValueOf(input).IsNil()), func(t *testing.T) {
			backend := NewMemory()
			expected := reflect.ValueOf(input)
			if err := backend.Set("key", input, 0); err != nil {
				t.Fatal(err)
			}
			got, found, err := backend.Get("key")
			if err != nil || !found || !reflect.DeepEqual(got, input) {
				t.Fatalf("快照不等价: %T %v %v", got, found, err)
			}
			first := reflect.ValueOf(got)
			if first.Type() != expected.Type() || first.IsNil() != expected.IsNil() || first.Cap() != first.Len() {
				t.Fatal("类型、nil 或容量语义变化")
			}
			if first.Len() > 0 {
				expected.Index(0).Set(reflect.Zero(expected.Type().Elem()))
				still, _, _ := backend.Get("key")
				if !reflect.DeepEqual(still, got) {
					t.Fatal("输入修改污染缓存")
				}
				first.Index(0).Set(reflect.Zero(first.Type().Elem()))
				again, _, _ := backend.Get("key")
				if !reflect.DeepEqual(still, again) {
					t.Fatal("输出修改污染缓存")
				}
			}
		})
	}
}

// TestMemoryReferenceSlicesRemainDeepSnapshots 防止快路径错误覆盖含可变引用的元素。
func TestMemoryReferenceSlicesRemainDeepSnapshots(t *testing.T) {
	type item struct{ Values []int }
	input := []item{{Values: []int{42}}}
	backend := NewMemory()
	if err := backend.Set("key", input, 0); err != nil {
		t.Fatal(err)
	}
	input[0].Values[0] = 0
	got, _, _ := backend.Get("key")
	if got.([]item)[0].Values[0] != 42 {
		t.Fatal("输入的嵌套引用泄漏")
	}
	got.([]item)[0].Values[0] = 1
	again, _, _ := backend.Get("key")
	if again.([]item)[0].Values[0] != 42 {
		t.Fatal("输出的嵌套引用泄漏")
	}
}

func BenchmarkMemoryScalarSnapshots(b *testing.B) {
	for _, n := range []int{16, 1024, 8192} {
		for _, kind := range []string{"int64", "string"} {
			b.Run(fmt.Sprintf("%s_%d", kind, n), func(b *testing.B) {
				backend := NewMemory()
				var value interface{}
				if kind == "int64" {
					values := make([]int64, n)
					for i := range values {
						values[i] = int64(i)
					}
					value = values
				} else {
					values := make([]string, n)
					for i := range values {
						values[i] = "snapshot"
					}
					value = values
				}
				if err := backend.Set("key", value, 0); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, found, err := backend.Get("key"); err != nil || !found {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
