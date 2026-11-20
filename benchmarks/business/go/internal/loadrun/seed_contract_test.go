package loadrun

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"testing"
)

// TestFrozenSeedResponses 让每种错误保持字段合法、自报金额自洽，只改变一项业务事实。
func TestFrozenSeedResponses(t *testing.T) {
	c := testConfig()
	c.ProductsPerTenant, c.Categories, c.CatalogPageSize, c.DatasetSeed = 6, 2, 2, 0
	c.InactiveProductModulus = 3
	headers := http.Header{"Content-Type": {"application/json"}, "X-Benchmark-Cache": {"miss"}}
	card := func(id, price int) string {
		return fmt.Sprintf(`{"id":%d,"name":"通用商品-2-%d","price_cents":%d,"category":"分类-2","stock":0}`, id+6, id, price)
	}
	first, last := card(2, 15938), card(4, 31776)
	page := func(rows, cursor string) string { return `{"items":[` + rows + `],"next_cursor":` + cursor + `}` }
	req := requestSpec{Scenario: "catalog", Tenant: 2, CategoryID: 2}
	check := func(name string, request requestSpec, body string, valid bool) {
		t.Helper()
		err := validateResponse(c, request, expectedStatus(request.Scenario), headers, []byte(body))
		if valid && err != nil || !valid && !errors.Is(err, errResponseData) {
			t.Errorf("%s：期望合法=%t，实际 %v", name, valid, err)
		}
	}
	check("末页恰好填满", req, page(first+","+last, "null"), true)
	check("缺少首项", req, page(last, "null"), false)
	check("误报后续页", req, page(first+","+last, `{"price_cents":31776,"id":10}`), false)
	c.CatalogPageSize = 1
	check("非末页游标", req, page(first, `{"price_cents":15938,"id":8}`), true)
	check("提前结束", req, page(first, "null"), false)
	check("跳过首项", req, page(last, `{"price_cents":31776,"id":10}`), false)
	c.CatalogPageSize, c.MaximumPriceCents = 2, 15938
	check("按价格过滤的短末页", req, page(first, "null"), true)
	order := requestSpec{Scenario: "order", Tenant: 2, OrderItems: []orderItem{{8, 2}}}
	check("跨租户真实价格", order, `{"order_id":1,"total_cents":31876,"status":"created","items":[{"product_id":8,"quantity":2,"unit_price_cents":15938}]}`, true)
	check("自洽的错误金额", order, `{"order_id":1,"total_cents":2,"status":"created","items":[{"product_id":8,"quantity":2,"unit_price_cents":1}]}`, false)
	detail := requestSpec{Scenario: "detail", Tenant: 2, ResourceID: 8}
	detailBody := func(price int, category string) string {
		return fmt.Sprintf(`{"id":8,"name":"通用商品-2-2","price_cents":%d,"category":"%s","stock":0,"description":"说明","specs":[{"name":"规格","value":"值"}]}`, price, category)
	}
	check("真实详情", detail, detailBody(15938, "分类-2"), true)
	check("详情错价", detail, detailBody(1, "分类-2"), false)
	check("详情错类", detail, detailBody(15938, "分类-1"), false)
	ticket := requestSpec{Scenario: "ticket", Tenant: 2, ResourceID: 5001}
	comment := func(id, version int) string {
		return fmt.Sprintf(`{"comment_id":%d,"ticket_id":5001,"version":%d,"status":"open"}`, id, version)
	}
	check("合法跳号和并发版本", ticket, comment(60012, 7), true)
	check("已存在的评论", ticket, comment(60000, 4), false)
	check("未递增版本", ticket, comment(60001, 3), false)

	c.InitialCommentsPerTicket, c.TicketsPerTenant, c.TenantCount = 1, 3, 2
	ticket.ResourceID = 4
	check("自定义种子工单", ticket, `{"comment_id":7,"ticket_id":4,"version":2,"status":"open"}`, true)
	check("另一个租户的初始评论", ticket, `{"comment_id":6,"ticket_id":4,"version":8,"status":"open"}`, false)
}

// TestCatalogPageTieAndCacheCollision 验证同价按编号排序，固定槽被另一分类占用后仍可重建。
func TestCatalogPageTieAndCacheCollision(t *testing.T) {
	c := testConfig()
	c.ProductsPerTenant, c.Categories, c.CatalogPageSize = 199901, 199900, 1
	pages := newCatalogPages(c)
	for _, category := range []int{1, 1 + maxCatalogCacheSlots, 1} {
		page := pages.lookup(c, category)
		if page.category != category || page.count != 1 || page.more != (category == 1) {
			t.Fatalf("分页信息与同价种子不一致：%+v", page)
		}
		if page.more && page.lastLocalID != 1 {
			t.Fatalf("同价时必须选择较小商品编号：%+v", page)
		}
	}
	if pages.lookup(c, c.ProductsPerTenant+1).count != 0 {
		t.Fatal("无商品的分类必须为空页")
	}
}

// TestLoadConfigurationRejectsImpossibleSeed 验证独立压测 CLI 在启动前使用相同的库存和评论边界。
func TestLoadConfigurationRejectsImpossibleSeed(t *testing.T) {
	c := testConfig()
	c.DatasetSeed = -8020
	if err := c.Validate(); err == nil {
		t.Fatal("无法生成正价的种子不应启动负载")
	}
	c.DatasetSeed = -8018
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.InitialCommentsPerTicket = 0
	if err := c.Validate(); err == nil {
		t.Fatal("缺少初始评论不能验证版本")
	}
	c.InitialCommentsPerTicket, c.TicketsPerTenant = math.MaxInt32, math.MaxInt32
	if err := c.Validate(); err == nil {
		t.Fatal("已超过 bigint 标识上限的评论不能启动负载")
	}
}
