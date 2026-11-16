package binding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

func TestNumericParametersRejectPaddingBeforeRangeValidation(t *testing.T) {
	type queryInput struct {
		Amount json.Number `query:"n" validate:"min:0|max:100"`
	}
	type formInput struct {
		Amount json.Number `form:"n" validate:"min:0|max:100"`
	}
	for _, test := range []struct {
		query string
		want  json.Number
	}{
		{"n=-5+", ""},
		{"n=1000%20", ""},
		{"n=+5", ""},
		{"n=5%09", ""},
		{"n=5e1%0A", ""},
		{"n=-5", ""},
		{"n=1e3", ""},
		{"n=5e1", "5e1"},
		{"n=100", "100"},
	} {
		t.Run(test.query, func(t *testing.T) {
			query := queryInput{Amount: "17"}
			form := formInput{Amount: "17"}
			get := httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
			post := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.query))
			post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			queryErr := Bind(fwcontext.MustNewRequest(get), &query)
			formErr := Bind(fwcontext.MustNewRequest(post), &form)
			if test.want == "" {
				if queryErr == nil || formErr == nil || query.Amount != "17" || form.Amount != "17" {
					t.Fatalf("非法数值被接受或改写了目标: query=%q/%v form=%q/%v", query.Amount, queryErr, form.Amount, formErr)
				}
			} else if queryErr != nil || formErr != nil || query.Amount != test.want || form.Amount != test.want {
				t.Fatalf("合法数值原文未被保留: query=%q/%v form=%q/%v", query.Amount, queryErr, form.Amount, formErr)
			}
		})
	}
}

func TestOrdinaryAndExplicitNumericStringsKeepTheirValidationContracts(t *testing.T) {
	get := httptest.NewRequest(http.MethodGet, "/?n=1000+", nil)
	var text struct {
		Value string `query:"n" validate:"min:4|max:8"`
	}
	if err := Bind(fwcontext.MustNewRequest(get), &text); err != nil || text.Value != "1000 " {
		t.Fatalf("普通字符串应保留长度约束与原始内容: %q, %v", text.Value, err)
	}
	var numeric struct {
		Value string `query:"n" validate:"number|min:0|max:100"`
	}
	if err := Bind(fwcontext.MustNewRequest(get), &numeric); err == nil {
		t.Fatal("显式数字规则应拒绝带空白的数值")
	}
	get = httptest.NewRequest(http.MethodGet, "/?n=42", nil)
	if err := Bind(fwcontext.MustNewRequest(get), &numeric); err != nil || numeric.Value != "42" {
		t.Fatalf("合法数字字符串应通过数字与区间规则: %q, %v", numeric.Value, err)
	}
}
