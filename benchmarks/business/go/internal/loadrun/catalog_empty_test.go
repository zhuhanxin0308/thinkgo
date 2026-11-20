package loadrun

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCatalogResponseRespectsConfiguredPriceAndPageSize(t *testing.T) {
	configuration := testConfig()
	configuration.Categories = 1
	configuration.CatalogPageSize = 30
	request := requestSpec{Scenario: "catalog", Tenant: 1, CategoryID: 1}
	body := fixtureCatalogPage(configuration, request.CategoryID)
	if err := validateCatalog(configuration, request, []byte(body)); err != nil {
		t.Errorf("按配置返回 30 件商品应通过校验: %v", err)
	}
	configuration.MaximumPriceCents = 99
	if err := validateCatalog(configuration, request, []byte(body)); err == nil {
		t.Error("响应包含超过请求价格上限的商品")
	}
	const empty = `{"items":[],"next_cursor":null}`
	if err := validateCatalog(configuration, request, []byte(empty)); err != nil {
		t.Errorf("没有满足价格范围的货品时空页合法: %v", err)
	}
	configuration.ProductsPerTenant = 1
	configuration.DatasetSeed = -5
	configuration.MaximumPriceCents = 8013
	if err := validateCatalog(configuration, request, []byte(empty)); err != nil {
		t.Errorf("商品价格为 8014 时，上限 8013 的空页合法: %v", err)
	}
	configuration.MaximumPriceCents = 8014
	if err := validateCatalog(configuration, request, []byte(empty)); err == nil {
		t.Error("存在满足价格范围的货品时不允许空页")
	}
}

// TestCatalogEmptyMatchesSeed 验证空目录只在种子合同确实无活动商品时合法。
func TestCatalogEmptyMatchesSeed(t *testing.T) {
	for _, test := range []struct {
		name       string
		products   int
		categories int
		category   int
		body       string
		valid      bool
	}{
		{"全分类下架", 25000, 10, 10, `{"items":[],"next_cursor":null}`, true},
		{"分类尚无商品", 200, 300, 201, `{"items":[],"next_cursor":null}`, true},
		{"唯一商品下架", 200, 200, 10, `{"items":[],"next_cursor":null}`, true},
		{"默认分类应非空", 25000, 32, 1, `{"items":[],"next_cursor":null}`, false},
		{"后续商品仍活动", 200, 11, 10, `{"items":[],"next_cursor":null}`, false},
		{"缺失商品列表", 25000, 10, 10, `{"next_cursor":null}`, false},
		{"空值不是数组", 25000, 10, 10, `{"items":null,"next_cursor":null}`, false},
		{"空页不能有游标", 25000, 10, 10, `{"items":[],"next_cursor":{"id":10,"price_cents":100}}`, false},
		{"缺失游标", 25000, 10, 10, `{"items":[]}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			configuration := testConfig()
			configuration.ProductsPerTenant = test.products
			configuration.Categories = test.categories
			request := requestSpec{Scenario: "catalog", Tenant: 1, CategoryID: test.category}
			if err := validateCatalog(configuration, request, []byte(test.body)); (err == nil) != test.valid {
				t.Fatalf("空目录判断错误: valid=%v err=%v", test.valid, err)
			}
		})
	}
}

// TestEmptyCatalogCountsAsSuccess 验证合法空目录通过实际请求链计入成功吞吐。
func TestEmptyCatalogCountsAsSuccess(t *testing.T) {
	synctest.Test(t, testEmptyCatalogCountsAsSuccess)
}

func testEmptyCatalogCountsAsSuccess(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "catalog"
	configuration.Categories = inactiveProductEvery
	start := time.Now()
	workload := newWorkload(configuration, start)
	var sequence uint64
	for workload.request(sequence).CategoryID != inactiveProductEvery {
		sequence++
	}
	client := &http.Client{Transport: catalogContractTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"items":[],"next_cursor":null}`)),
		}, nil
	})}
	event := perform(context.Background(), configuration, workload, client, scheduledRequest{When: start, Sequence: sequence})
	statistics := newStatistics(configuration, start)
	statistics.add(event)
	report := statistics.finish(start.Add(time.Second), time.Second, false, 1)
	assertConservation(t, report)
	if report.Totals.SemanticErrors != 0 || report.Totals.Success != 1 || report.SuccessRPS != 1 {
		t.Fatalf("合法空目录未计入成功吞吐: %+v", report)
	}
}

func TestPriceFilteredEmptyCatalogCountsAsSuccess(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "catalog"
	configuration.MaximumPriceCents = 1
	start := time.Now()
	workload := newWorkload(configuration, start)
	client := &http.Client{Transport: catalogContractTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("max_price") != "1" {
			t.Error("负载器未应用数据集价格上限")
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"items":[],"next_cursor":null}`)),
		}, nil
	})}
	for sequence := uint64(0); sequence < 2; sequence++ {
		event := perform(t.Context(), configuration, workload, client, scheduledRequest{When: start, Sequence: sequence})
		if event.Outcome != "success" {
			t.Fatalf("正确的价格过滤空页应计为成功: %+v", event)
		}
	}
}
