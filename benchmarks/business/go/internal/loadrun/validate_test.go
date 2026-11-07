package loadrun

import (
	"net/http"
	"strings"
	"testing"
)

func TestResponseSemanticContracts(t *testing.T) {
	configuration := testConfig()
	cases := []struct {
		name    string
		request requestSpec
		status  int
		cache   string
		body    string
	}{
		{"catalog", requestSpec{Scenario: "catalog", Tenant: 1}, 200, "", `{"items":[{"id":1,"name":"商品","price_cents":100,"category":"分类","stock":30},{"id":2,"name":"商品2","price_cents":100,"category":"分类","stock":30}],"next_cursor":{"price_cents":100,"id":2}}`},
		{"detail", requestSpec{Scenario: "detail", Tenant: 1, ResourceID: 1}, 200, "hit", `{"id":1,"name":"商品","price_cents":100,"stock":30,"category":"分类","description":"商品说明","specs":[{"name":"颜色","value":"黑色"}]}`},
		{"order", requestSpec{Scenario: "order", Tenant: 1, OrderItems: []orderItem{{1, 2}, {2, 1}}}, 201, "", `{"order_id":1,"total_cents":400,"status":"created","items":[{"product_id":1,"quantity":2,"unit_price_cents":100},{"product_id":2,"quantity":1,"unit_price_cents":200}]}`},
		{"ticket", requestSpec{Scenario: "ticket", Tenant: 1, ResourceID: 7}, 201, "", `{"comment_id":1,"ticket_id":7,"version":2,"status":"open"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Content-Type", "application/json; charset=utf-8")
			headers.Set("X-Benchmark-Cache", test.cache)
			if err := validateResponse(configuration, test.request, test.status, headers, []byte(test.body)); err != nil {
				t.Fatalf("正确响应被拒绝: %v", err)
			}
			for _, invalid := range []string{`null`, `{}`, test.body + `{}`, strings.TrimSuffix(test.body, "}") + `,"secret":"hidden"}`, strings.Replace(test.body, `"id":1`, `"id":25001`, 1)} {
				if invalid == test.body {
					continue
				}
				if err := validateResponse(configuration, test.request, test.status, headers, []byte(invalid)); err == nil {
					t.Errorf("无效响应未被拒绝: %s", invalid)
				}
			}
			if err := validateResponse(configuration, test.request, http.StatusInternalServerError, headers, []byte(test.body)); err == nil {
				t.Fatal("错误HTTP状态被计为成功")
			}
		})
	}
}

func TestResponseRejectsWrongBusinessValues(t *testing.T) {
	configuration := testConfig()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Benchmark-Cache", "miss")
	cases := []struct {
		request requestSpec
		status  int
		body    string
	}{
		{requestSpec{Scenario: "catalog", Tenant: 1}, 200, `{"items":[],"next_cursor":null}`},
		{requestSpec{Scenario: "catalog", Tenant: 1}, 200, `{"items":[{"id":2,"name":"x","price_cents":200,"category":"c","stock":1},{"id":1,"name":"x","price_cents":100,"category":"c","stock":1}],"next_cursor":null}`},
		{requestSpec{Scenario: "catalog", Tenant: 1}, 200, `{"items":[{"id":1,"name":"x","price_cents":100,"category":"c","stock":1}],"next_cursor":{"price_cents":100,"id":2}}`},
		{requestSpec{Scenario: "detail", Tenant: 1, ResourceID: 1}, 200, `{"id":2,"name":"x","price_cents":100,"stock":1,"category":"c","description":"d","specs":[]}`},
		{requestSpec{Scenario: "order", Tenant: 1, OrderItems: []orderItem{{1, 2}, {2, 1}}}, 201, `{"order_id":1,"total_cents":301,"status":"created","items":[{"product_id":1,"quantity":2,"unit_price_cents":100},{"product_id":2,"quantity":1,"unit_price_cents":100}]}`},
		{requestSpec{Scenario: "order", Tenant: 1, OrderItems: []orderItem{{1, 2}, {2, 1}}}, 200, `{"order_id":1,"total_cents":300,"status":"created","items":[{"product_id":1,"quantity":2,"unit_price_cents":100},{"product_id":2,"quantity":1,"unit_price_cents":100}]}`},
		{requestSpec{Scenario: "order", Tenant: 1, OrderItems: []orderItem{{1, 2}, {2, 1}}}, 201, `{"order_id":1,"total_cents":300,"status":"created","items":[{"product_id":1,"quantity":1,"unit_price_cents":100},{"product_id":2,"quantity":2,"unit_price_cents":100}]}`},
		{requestSpec{Scenario: "ticket", Tenant: 1, ResourceID: 1}, 201, `{"comment_id":1,"ticket_id":2,"version":1,"status":"open"}`},
	}
	for index, test := range cases {
		if err := validateResponse(configuration, test.request, test.status, headers, []byte(test.body)); err == nil {
			t.Errorf("错误业务结果 #%d 未被拒绝", index)
		}
	}
}
