package loadrun

import (
	"net/http"
	"testing"
)

// 业务种子和订单写入均保证商品单价为正，压测校验不能接受伪造的零价结果。
func TestResponseRejectsZeroProductPrices(t *testing.T) {
	configuration := testConfig()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Benchmark-Cache", "miss")
	cases := []struct {
		name    string
		request requestSpec
		status  int
		body    string
	}{
		{
			name:    "目录零价格",
			request: requestSpec{Scenario: "catalog", Tenant: 1},
			status:  http.StatusOK,
			body:    `{"items":[{"id":1,"name":"商品","price_cents":0,"category":"分类","stock":100}],"next_cursor":null}`,
		},
		{
			name:    "详情零价格",
			request: requestSpec{Scenario: "detail", Tenant: 1, ResourceID: 1},
			status:  http.StatusOK,
			body:    `{"id":1,"name":"商品","price_cents":0,"category":"分类","stock":100,"description":"商品描述","specs":[{"name":"颜色","value":"蓝色"}]}`,
		},
		{
			name:    "订单零单价",
			request: requestSpec{Scenario: "order", Tenant: 1, OrderItems: []orderItem{{ProductID: 1, Quantity: 2}}},
			status:  http.StatusCreated,
			body:    `{"order_id":1,"total_cents":0,"status":"created","items":[{"product_id":1,"quantity":2,"unit_price_cents":0}]}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := validateResponse(configuration, test.request, test.status, headers, []byte(test.body)); err == nil {
				t.Fatal("业务合同要求价格大于零，不得把零价响应计为成功")
			}
		})
	}
}
