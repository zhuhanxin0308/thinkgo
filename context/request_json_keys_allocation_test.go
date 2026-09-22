//go:build !race

package context

import "testing"

// TestStrictJSONValidationSmallObjectAllocation 保证只校验常见小对象时不复制业务字段名。
// race 插桩会改变标准库池的分配行为，资源预算在普通测试中独立执行。
func TestStrictJSONValidationSmallObjectAllocation(t *testing.T) {
	const body = `{"sequence":123,"items":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`
	payload := []byte(body)
	allocations := testing.AllocsPerRun(100, func() {
		if err := validateStrictJSONDocument(payload, true); err != nil {
			panic(err)
		}
	})
	if allocations != 0 {
		t.Fatalf("小对象边界校验不应分配键副本: %.0f", allocations)
	}
}
