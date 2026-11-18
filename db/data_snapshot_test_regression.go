package db

import (
	"testing"
	"time"
)

func TestReusableModelRowWithMapKeysBudget(t *testing.T) {
	// 验证在 budget 计算中包含 map key，对于 getter-free/relation-free 的行，
	// 标量行 node 预算必须包含 key。
	row := make(map[string]any, 200000)
	for i := 0; i < 200000; i++ {
		row["key"] = "value"
	}
	// 按 nodesPerColumn = 3 计算应该被拒，按 4 计算(包含键)应该被拒。
	if reusableModelRow(row) {
		t.Fatal("大数据行应超出 node budget 被拒")
	}
}
