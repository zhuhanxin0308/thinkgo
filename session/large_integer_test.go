package session

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestSessionLargeIntegersRoundTrip 验证同请求读取、全集及持久化后的嵌套整数均无损。
func TestSessionLargeIntegersRoundTrip(t *testing.T) {
	manager := newTestSessionManager(t, newCountingDriver(), nil, nil)
	recorder := httptest.NewRecorder()
	first, err := manager.NewRequestSession(httptest.NewRequest(http.MethodGet, "http://example.com/", nil), recorder)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		input interface{}
		want  interface{}
	}{
		"max-int":   {int64(math.MaxInt64), int64(math.MaxInt64)},
		"max-uint":  {uint64(math.MaxUint64), json.Number("18446744073709551615")},
		"above-int": {json.Number("9223372036854775808"), json.Number("9223372036854775808")},
		"below-int": {json.Number("-9223372036854775809"), json.Number("-9223372036854775809")},
		"huge":      {json.Number("123456789012345678901234567890"), json.Number("123456789012345678901234567890")},
		"decimal":   {json.Number("1.5"), 1.5},
		"nested": {map[string]interface{}{"values": []interface{}{uint64(math.MaxUint64), nil}},
			map[string]interface{}{"values": []interface{}{json.Number("18446744073709551615"), nil}}},
	}
	for key, test := range cases {
		if err := first.Set(key, test.input); err != nil {
			t.Fatal(err)
		}
	}
	check := func(request *Session) {
		t.Helper()
		all := request.All()
		for key, test := range cases {
			got, found := request.Get(key)
			if !found || !reflect.DeepEqual(got, test.want) || !reflect.DeepEqual(all[key], test.want) {
				t.Errorf("整数往返变化: key=%s got=%#v all=%#v want=%#v", key, got, all[key], test.want)
			}
		}
	}
	check(first)
	if err := first.Save(); err != nil {
		t.Fatal(err)
	}
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	for _, cookie := range recorder.Result().Cookies() {
		raw.AddCookie(cookie)
	}
	second, err := manager.NewRequestSession(raw, httptest.NewRecorder())
	if err != nil {
		t.Fatal(err)
	}
	check(second)
}
