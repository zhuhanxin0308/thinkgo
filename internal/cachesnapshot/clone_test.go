package cachesnapshot_test

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/internal/cachesnapshot"
)

func TestGraphClonePreservesCyclesAndSeparatesMutableStorage(t *testing.T) {
	type node struct {
		Label string
		Next  *node
	}
	type namedPointer *node
	type packet struct {
		Graph [2]namedPointer
		Edges map[string]any
		Items []any
		Stamp time.Time
		title string
	}
	root := &node{Label: "original"}
	root.Next = root
	edges := map[string]any{}
	edges["self"] = edges
	items := make([]any, 3)
	items[0], items[1], items[2] = items, []byte("old"), nil
	source := packet{Graph: [2]namedPointer{root, root}, Edges: edges, Items: items, Stamp: time.Now(), title: "private scalar"}
	value, err := cachesnapshot.Clone(source)
	if err != nil {
		t.Fatal(err)
	}
	copy := value.(packet)
	if copy.Graph[0] == root || copy.Graph[1] != copy.Graph[0] || copy.Graph[0].Next != copy.Graph[0] {
		t.Fatal("快照丢失指针环、共享引用或仍然引用原图")
	}
	copy.Graph[0].Label = "changed"
	copy.Edges["self"].(map[string]any)["added"] = true
	copy.Items[0].([]any)[1].([]byte)[0] = 'n'
	if root.Label != "original" || edges["added"] != nil || string(items[1].([]byte)) != "old" {
		t.Fatal("修改快照影响了源对象")
	}
	if copy.Edges["added"] != true || string(copy.Items[1].([]byte)) != "nld" || copy.Items[2] != nil || copy.Stamp != source.Stamp || copy.title != source.title {
		t.Fatal("快照没有保留环内的修改或不可变字段")
	}
}

func TestCloneKeepsTypedNilAndImmutableKeys(t *testing.T) {
	type key struct{ ID [2]any }
	for _, value := range []any{
		nil, int64(17), "text", time.Now(), (*int)(nil), []string(nil), map[int]int(nil),
		map[key]string{{ID: [2]any{"a", nil}}: "text"}, map[[2]uint64]string{{1, 2}: "array"},
	} {
		got, err := cachesnapshot.Clone(value)
		if err != nil || !reflect.DeepEqual(value, got) {
			t.Fatalf("快照改变类型或值：input=%#v got=%#v err=%v", value, got, err)
		}
	}
}

func TestCloneRejectsUnisolatableReferences(t *testing.T) {
	type key struct{ ID any }
	value := 42
	for _, unsupported := range []any{
		bytes.NewBufferString("private storage"),
		make(chan int),
		func() {},
		map[*int]int{&value: 1},
		map[key]int{{ID: &value}: 1},
		map[[1]any]int{{&value}: 1},
		[]any{map[string]any{"channel": make(chan int)}},
	} {
		if got, err := cachesnapshot.Clone(unsupported); got != nil || !errors.Is(err, cachesnapshot.ErrUnsupportedValue) {
			t.Fatalf("不能安全复制的 %T 未被拒绝：got=%v err=%v", unsupported, got, err)
		}
	}
}
