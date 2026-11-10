package context

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	frameworksession "github.com/zhuhanxin0308/thinkgo/v3/session"
)

type requestIDs []int
type requestLabels map[string]string

// TestTypedRequestSnapshotsIsolateIngressAndEgress 覆盖各公开来源的强类型集合边界。
func TestTypedRequestSnapshotsIsolateIngressAndEgress(t *testing.T) {
	for _, source := range []string{"route", "get", "post", "middleware"} {
		t.Run(source, func(t *testing.T) {
			request := MustNewRequest(httptest.NewRequest(http.MethodPost, "/", nil))
			values := map[string]interface{}{
				"ids": requestIDs{1, 2}, "labels": requestLabels{"name": "before"},
				"nested": map[int][]int{1: {3, 4}}, "array": [1][]int{{5}},
			}
			switch source {
			case "route":
				request.WithRoute(values)
			case "get":
				request.WithGet(values)
			case "post":
				request.WithPost(values)
			case "middleware":
				request.WithMiddleware(values)
			}
			read := request.All
			if source == "middleware" {
				read = func() map[string]interface{} { return request.Middleware().(map[string]interface{}) }
			}
			mutate := func(snapshot map[string]interface{}) {
				snapshot["ids"].(requestIDs)[0] = 99
				snapshot["labels"].(requestLabels)["name"] = "after"
				snapshot["nested"].(map[int][]int)[1][0] = 99
				snapshot["array"].([1][]int)[0][0] = 99
			}
			mutate(values)
			want := map[string]interface{}{
				"ids": requestIDs{1, 2}, "labels": requestLabels{"name": "before"},
				"nested": map[int][]int{1: {3, 4}}, "array": [1][]int{{5}},
			}
			if !reflect.DeepEqual(read(), want) {
				t.Fatal("输入集合与请求内部值共享存储")
			}
			if source == "route" {
				value, _ := request.RouteValue("ids")
				value.(requestIDs)[0] = 77
			}
			var workers sync.WaitGroup
			for range 8 {
				workers.Go(func() {
					for range 20 {
						mutate(read())
					}
				})
			}
			workers.Wait()
			if !reflect.DeepEqual(read(), want) {
				t.Fatal("输出快照与请求内部值共享存储")
			}
		})
	}
}

func TestRequestClonePreservesNilEmptyAndObjectIdentity(t *testing.T) {
	current := &frameworksession.Session{}
	channel := make(chan struct{})
	values := map[string]interface{}{
		"nilSlice": requestIDs(nil), "emptySlice": requestIDs{},
		"nilMap": requestLabels(nil), "emptyMap": requestLabels{},
		"nil": nil, "session": current, "channel": channel,
	}
	request := MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)).WithMiddleware(values)
	got := request.Middleware().(map[string]interface{})
	if !reflect.DeepEqual(got, values) || got["session"] != current || got["channel"] != channel {
		t.Fatalf("空值、类型或对象身份发生变化: %#v", got)
	}
	if got["nilSlice"].(requestIDs) != nil || got["emptySlice"].(requestIDs) == nil ||
		got["nilMap"].(requestLabels) != nil || got["emptyMap"].(requestLabels) == nil {
		t.Fatal("nil 与空集合被混淆")
	}
}

// TestRequestCloneHandlesCycles 验证快照与过滤共用有环遍历，不会无限递归或借出内部值。
func TestRequestCloneHandlesCycles(t *testing.T) {
	cyclicMap := map[string]interface{}{"name": " before "}
	cyclicMap["self"] = cyclicMap
	cyclicSlice := make([]interface{}, 2)
	cyclicSlice[0], cyclicSlice[1] = cyclicSlice, cyclicMap
	request := MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)).WithRoute(map[string]interface{}{
		"mapping": cyclicMap, "slice": cyclicSlice,
	})
	request.Filter(strings.TrimSpace)
	cyclicMap["name"] = "outside"
	first := request.All()
	mapping := first["mapping"].(map[string]interface{})
	if mapping["name"] != "before" || reflect.ValueOf(mapping).Pointer() != reflect.ValueOf(mapping["self"]).Pointer() {
		t.Fatal("有环映射过滤或结构错误")
	}
	slice := first["slice"].([]interface{})
	if reflect.ValueOf(slice).Pointer() != reflect.ValueOf(slice[0]).Pointer() {
		t.Fatal("有环切片身份丢失")
	}
	mapping["self"].(map[string]interface{})["name"] = "snapshot"
	slice[1].(map[string]interface{})["name"] = "snapshot"
	if next := request.All(); next["mapping"].(map[string]interface{})["name"] != "before" ||
		next["slice"].([]interface{})[1].(map[string]interface{})["name"] != "before" {
		t.Fatal("有环快照污染内部请求值")
	}
}
