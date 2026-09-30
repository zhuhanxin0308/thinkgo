package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/cookie"
)

func TestSessionDecodedCollectionsRemainIsolated(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), nil, nil)
	request, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	original := map[string]interface{}{"items": []interface{}{map[string]interface{}{"count": 42, "ratio": 1.5}}, "empty": []interface{}{}, "null": nil}
	if err := request.Set("nested", original); err != nil {
		t.Fatal(err)
	}
	value, found := request.Get("nested")
	if !found {
		t.Fatal("missing nested value")
	}
	original["items"].([]interface{})[0].(map[string]interface{})["count"] = -1
	value.(map[string]interface{})["items"].([]interface{})[0].(map[string]interface{})["count"] = int64(-2)
	all := request.All()
	all["nested"].(map[string]interface{})["items"].([]interface{})[0] = "changed"
	expected := map[string]interface{}{"items": []interface{}{map[string]interface{}{"count": int64(42), "ratio": 1.5}}, "empty": []interface{}{}, "null": nil}
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 50; j++ {
				snapshot, ok := request.Get("nested")
				if !ok || !reflect.DeepEqual(snapshot, expected) {
					t.Error("decoded snapshot changed or shares another reader's storage")
					return
				}
				snapshot.(map[string]interface{})["items"].([]interface{})[0] = j
			}
		}()
	}
	wait.Wait()
}

func TestSessionNumberNormalizationBoundaries(t *testing.T) {
	raw := json.RawMessage(`[9223372036854775807,-9223372036854775808,1.5,1e10000,[],{},null]`)
	got, err := decodeSessionValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{int64(9223372036854775807), int64(-9223372036854775808), 1.5, "1e10000", []interface{}{}, map[string]interface{}{}, nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalization changed: %#v", got)
	}
}

func BenchmarkSessionGetNested(b *testing.B) {
	for _, size := range []int{10, 100} {
		b.Run(fmt.Sprintf("items_%d", size), func(b *testing.B) {
			factory, err := cookie.NewCookie(nil)
			if err != nil {
				b.Fatal(err)
			}
			manager, err := NewSession(nil, newCountingDriver(), factory)
			if err != nil {
				b.Fatal(err)
			}
			request, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), httptest.NewRecorder())
			if err != nil {
				b.Fatal(err)
			}
			items := make([]interface{}, size)
			for i := range items {
				items[i] = map[string]interface{}{"id": i, "amount": 1.5, "tags": []interface{}{"a", "b"}}
			}
			if err := request.Set("items", items); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, found := request.Get("items"); !found {
					b.Fatal("missing items")
				}
			}
		})
	}
}
