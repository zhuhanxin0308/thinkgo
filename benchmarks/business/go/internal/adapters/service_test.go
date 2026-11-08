package adapters

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONBoundaryRejectsAmbiguousDocuments(t *testing.T) {
	for _, body := range []string{
		"", "null", "[]", "{}{}", `{"id":1,"id":2}`, `{"items":[{"quantity":1,"Quantity":2}]}`,
		`{"body":"` + string([]byte{0xff}) + `"}`, `{"a":`,
	} {
		if err := ValidateObject([]byte(body), 256); err == nil {
			t.Errorf("接受了歧义或无效正文: %q", body)
		}
	}
	valid := []byte(`{"items":[{"product_id":1,"quantity":2}]}`)
	if err := ValidateObject(valid, int64(len(valid))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateObject(valid, int64(len(valid)-1)); err == nil {
		t.Fatal("超过字节上限的正文被接受")
	}
}

func TestBusinessJSONShapeRejectsUnknownAndCaseVariantMembers(t *testing.T) {
	for _, body := range []string{
		`{"items":[{"product_id":1,"quantity":2}],"tenant_id":2}`,
		`{"items":[{"product_id":1,"quantity":2,"price":1}]}`,
		`{"body":"合法业务评论内容需要严格字段合同","extra":true}`,
		`{"Body":"合法业务评论内容需要严格字段合同"}`,
		`{"items":[{"product_id":1}]}`, `{"items":{}}`, `{"body":null}`,
		`{"items":[{"product_id":"1","quantity":2}]}`,
		`{"body":"第一条完全合法的工单评论正文","body":"第二条完全合法的工单评论正文"}`,
	} {
		if ValidateObject([]byte(body), 1024) == nil {
			t.Fatalf("非合同正文被接受: %s", body)
		}
	}
	for _, body := range []string{
		`{"body":"合法业务评论内容需要严格字段合同"}`,
		`{"items":[{"product_id":1,"quantity":2},{"product_id":2,"quantity":1}]}`,
	} {
		if err := ValidateObject([]byte(body), 1024); err != nil {
			t.Fatalf("合法业务正文被拒绝: %s %v", body, err)
		}
	}
}

func TestMediaBoundary(t *testing.T) {
	for _, media := range []string{"application/json", "application/json; charset=UTF-8", "Application/Json; Charset=\"utf-8\""} {
		if err := ValidateMedia(media, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range [][2]string{{"", ""}, {"text/plain", ""}, {"application/json; charset=gbk", ""}, {"application/json", "gzip"},
		{"application/json; extra=1", ""}, {"application/json; charset=\"\"", ""},
		{"application/json; charset=utf-8; charset=utf-8", ""}, {"application/json;", ""}} {
		if err := ValidateMedia(input[0], input[1]); err == nil {
			t.Fatalf("接受了未定义媒体: %v", input)
		}
	}
}

func TestCatalogQueryPreservesDecodingFailures(t *testing.T) {
	for _, raw := range []string{"limit=%GG", "limit=2&", "&limit=2", "limit", "limit=1;limit=2"} {
		if _, err := parseCatalogQuery(raw); err == nil {
			t.Fatalf("无效查询被忽略: %s", raw)
		}
	}
	query, err := parseCatalogQuery("category_id=1&limit=%32")
	if err != nil || query.Get("limit") != "2" {
		t.Fatalf("合法查询解码失败: %+v %v", query, err)
	}
	if query, err := parseCatalogQuery(""); err != nil || len(query) != 0 {
		t.Fatalf("默认查询无效: %+v %v", query, err)
	}
}

type failingBody struct{ closed bool }

func (*failingBody) Read([]byte) (int, error) { return 0, errors.New("读取失败") }
func (body *failingBody) Close() error        { body.closed = true; return nil }

func TestPrepareBodyOwnershipAndFailures(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"body":"ok"}`))
	request.Header.Set("Content-Type", "application/json")
	if err := PrepareBody(request, 128); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil || string(body) != `{"body":"ok"}` {
		t.Fatalf("原生绑定器无法重读已验证正文: %s %v", body, err)
	}
	broken := &failingBody{}
	request.Body = broken
	if err := PrepareBody(request, 128); err == nil || !broken.closed {
		t.Fatal("错误读取未中止或未关闭原始正文")
	}
	request.Body = nil
	if err := PrepareBody(request, 128); err == nil {
		t.Fatal("nil正文被接受")
	}
}
