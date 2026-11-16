package context

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMethodOverrideHeaderOnlyAllowsPostToWriteMethods(t *testing.T) {
	const header = "X-HTTP-Method-Override"
	for _, test := range []struct {
		name, original, want string
		overrides            []string
	}{
		{"POST 转 PUT", http.MethodPost, http.MethodPut, []string{" put "}},
		{"POST 转 PATCH", http.MethodPost, http.MethodPatch, []string{"PATCH"}},
		{"POST 转 DELETE", http.MethodPost, http.MethodDelete, []string{"Delete"}},
		{"GET 不能转 DELETE", http.MethodGet, http.MethodGet, []string{"DELETE"}},
		{"HEAD 不能转 GET", http.MethodHead, http.MethodHead, []string{"GET"}},
		{"OPTIONS 不能转 PUT", http.MethodOptions, http.MethodOptions, []string{"PUT"}},
		{"PUT 不能转 PATCH", http.MethodPut, http.MethodPut, []string{"PATCH"}},
		{"POST 不能转 GET", http.MethodPost, http.MethodPost, []string{"GET"}},
		{"POST 不能转 HEAD", http.MethodPost, http.MethodPost, []string{"HEAD"}},
		{"拒绝合并头值", http.MethodPost, http.MethodPost, []string{"PUT, PATCH"}},
		{"拒绝重复头值", http.MethodPost, http.MethodPost, []string{"PUT", "PATCH"}},
		{"忽略未设置的头", http.MethodPost, http.MethodPost, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := httptest.NewRequest(test.original, "http://example.com/items", nil)
			for _, value := range test.overrides {
				raw.Header.Add(header, value)
			}
			request := newRequestForTest(t, raw)
			if got := request.Method(); got != test.want || request.Method(true) != test.original || raw.Method != test.original {
				t.Fatalf("方法覆写或原始方法错误: effective=%q original=%q raw=%q", got, request.Method(true), raw.Method)
			}
		})
	}
}
