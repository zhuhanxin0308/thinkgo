package context

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestBulkCopyPreservesFilteringAndNestedOwnership(t *testing.T) {
	type namedBytes []byte
	input := map[string]interface{}{
		"bytes": namedBytes("before"), "numbers": []int{1, 2},
		"texts": []string{" padded "}, "nested": [1][]byte{[]byte("nested")},
		"matrix": [][2]int{{3, 4}},
	}
	request := MustNewRequest(httptest.NewRequest(http.MethodGet, "/", nil)).WithRoute(input)
	request.Filter(strings.TrimSpace)
	first := request.All()
	first["bytes"].(namedBytes)[0] = 'X'
	first["numbers"].([]int)[0] = 99
	first["nested"].([1][]byte)[0][0] = 'X'
	first["matrix"].([][2]int)[0][0] = 99
	if first["texts"].([]string)[0] != "padded" {
		t.Fatal("批量复制绕过了字符串过滤")
	}
	again := request.All()
	if string(again["bytes"].(namedBytes)) != "before" || again["numbers"].([]int)[0] != 1 ||
		string(again["nested"].([1][]byte)[0]) != "nested" || again["matrix"].([][2]int)[0][0] != 3 {
		t.Fatal("批量复制借出了内部集合")
	}
}
