package snapshot

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCopyPreservesTypesAndSharedReferences(t *testing.T) {
	type key struct {
		at   time.Time
		path [2]any
	}
	type payload struct {
		hidden string
		Items  []int
	}
	at := time.Unix(1700000000, 0).In(time.FixedZone("local", 3600))
	label := key{at: at, path: [2]any{"feature", nil}}
	original := &payload{hidden: "value", Items: []int{41, 42}}
	input := map[key][5]any{label: {original, original, at, nil, []int(nil)}}
	value, err := Copy(input)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[key][5]any)[label]
	first, second := result[0].(*payload), result[1].(*payload)
	if first == original || first != second || first.hidden != original.hidden || result[2] != at || result[3] != nil || result[4].([]int) != nil {
		t.Fatalf("副本破坏了类型、空值或共享关系: %#v", result)
	}
	first.Items[0] = 99
	if original.Items[0] != 41 || second.Items[0] != 99 {
		t.Fatal("副本没有正确隔离外部存储并保留内部共享")
	}
}

func TestCopyRejectsUnsafeReferences(t *testing.T) {
	type node struct{ Next *node }
	cyclic := &node{}
	cyclic.Next = cyclic
	deep := &node{}
	for range MaxDepth {
		deep = &node{Next: deep}
	}
	cyclicMap := map[string]any{}
	cyclicMap["self"] = cyclicMap
	values := []struct {
		name  string
		value any
	}{
		{"指针环", cyclic},
		{"映射环", cyclicMap},
		{"超过深度限制", deep},
		{"锁", &sync.Mutex{}},
		{"私有可变字段", struct{ hidden []int }{[]int{1}}},
		{"可变映射键", map[[1]*int]string{{new(int)}: "key"}},
		{"资源状态映射键", map[struct{ lock sync.Mutex }]bool{{}: true}},
		{"切片中的通道", []any{make(chan int)}},
		{"映射中的函数", map[string]func(){"run": func() {}}},
	}
	for _, test := range values {
		t.Run(test.name, func(t *testing.T) {
			if value, err := Copy(test.value); value != nil || !errors.Is(err, ErrUnsupportedValue) {
				t.Fatalf("应拒绝不可隔离的值: %v, %v", value, err)
			}
		})
	}
}

func TestCopyValidatesOverlappingViewsAcrossBranches(t *testing.T) {
	backing := []int{1, 2, 3}
	if _, err := Copy([2][]int{backing[:2], backing[1:]}); !errors.Is(err, ErrUnsupportedValue) {
		t.Fatalf("重叠视图不能复制成独立切片: %v", err)
	}
	var spans Spans
	if _, err := CopyAt(backing[:1:1], 2, &spans); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyAt(&backing[0], 2, &spans); err != nil {
		t.Fatal(err)
	}
	if err := spans.Validate(); !errors.Is(err, ErrUnsupportedValue) {
		t.Fatalf("共享存储校验应跨快照分支生效: %v", err)
	}
	if value, err := Copy(nil); value != nil || err != nil {
		t.Fatalf("空输入应保持空值: %v, %v", value, err)
	}
}
