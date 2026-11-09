package resourceidentity

import "testing"

// TestKeyUsesOnlyReferenceIdentity 覆盖稳定引用、独立资源、类型边界和全部不可识别值。
func TestKeyUsesOnlyReferenceIdentity(t *testing.T) {
	first, second := 1, 1
	for _, pair := range [][2]any{
		{&first, &second},
		{make(chan int), make(chan int)},
		{map[string]int{"x": 1}, map[string]int{"x": 1}},
	} {
		key, valid := Key(pair[0])
		again, sameValid := Key(pair[0])
		other, otherValid := Key(pair[1])
		if !valid || !sameValid || !otherValid || key != again || key == other {
			t.Fatal("引用身份未区分独立实例或未保留别名")
		}
	}
	type namedPointer *int
	plain, _ := Key(&first)
	named, _ := Key(namedPointer(&first))
	if plain == named {
		t.Fatal("不同动态类型不能共享关闭身份")
	}
	var pointer *int
	var channel chan int
	var mapping map[string]int
	for _, value := range []any{nil, pointer, channel, mapping, 1, "值", struct{}{}, struct{ Value any }{[]int{1}}, []int{}, []int{1}, func() {}} {
		if _, valid := Key(value); valid {
			t.Fatalf("不可靠身份不应被用于去重: %T", value)
		}
	}
}
