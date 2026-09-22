package context

import (
	"strings"
	"testing"
)

// TestInvalidJSONDoesNotAllocatePartialStructures 防止末尾损坏的正文先分配整棵参数树或重复键索引。
func TestInvalidJSONDoesNotAllocatePartialStructures(t *testing.T) {
	const allocationRuns, smallItemCount, largeItemCount = 10, 32, 1024
	// 两次标准库语法诊断可能各自重新分配被运行时丢弃的池对象，允许此固定开销但不允许按成员增长。
	const diagnosticAllocationVariation = 2
	malformed := func(count int) []byte {
		items := strings.TrimSuffix(strings.Repeat(`{"name":"value"},`, count), ",")
		return []byte(`{"items":[` + items + `]`)
	}
	small, large := malformed(smallItemCount), malformed(largeItemCount)
	for _, operation := range []struct {
		name string
		run  func([]byte) error
	}{
		{"参数树", func(body []byte) error { _, err := decodeStrictJSONValue(body); return err }},
		{"仅校验", func(body []byte) error { return validateStrictJSONDocument(body, false) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			allocations := func(body []byte) float64 {
				return testing.AllocsPerRun(allocationRuns, func() {
					if err := operation.run(body); err == nil {
						panic("末尾损坏的 JSON 必须被拒绝")
					}
				})
			}
			smallAllocations, largeAllocations := allocations(small), allocations(large)
			if largeAllocations > smallAllocations+diagnosticAllocationVariation {
				t.Fatalf("非法正文的分配随合法前缀的成员数量增长: small=%g large=%g", smallAllocations, largeAllocations)
			}
		})
	}
}
