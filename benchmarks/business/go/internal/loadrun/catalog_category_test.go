package loadrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCatalogCategoryContract 使用独立期望分类检查同租户错类、名称伪装和既有排序边界。
func TestCatalogCategoryContract(t *testing.T) {
	configuration := testConfig()
	const correct = `{"items":[{"id":2,"name":"商品","price_cents":100,"category":"分类-2","stock":30}],"next_cursor":null}`
	request := requestSpec{Scenario: "catalog", Tenant: 1, CategoryID: 2}
	headers := http.Header{"Content-Type": {"application/json"}}
	if err := validateResponse(configuration, request, http.StatusOK, headers, []byte(correct)); err != nil {
		t.Fatal(err)
	}
	otherTenant := request
	otherTenant.Tenant = 2
	if err := validateResponse(configuration, otherTenant, http.StatusOK, headers, []byte(strings.Replace(correct, `"id":2`, `"id":25002`, 1))); err != nil {
		t.Fatalf("分类应使用租户内编号而非全局编号: %v", err)
	}
	for _, body := range []string{
		`{"items":[{"id":1,"name":"通用商品-1-1","price_cents":79226,"category":"分类-1","stock":1000000}],"next_cursor":null}`,
		strings.Replace(correct, `"id":2`, `"id":1`, 1),
		strings.Replace(correct, "分类-2", "分类-1", 1),
		strings.Replace(correct, "分类-2", "分类-02", 1),
		strings.Replace(correct, `"category":"分类-2",`, "", 1),
		strings.Replace(correct, `"id":2`, `"id":25002`, 1),
		`{"items":[{"id":34,"name":"商品","price_cents":200,"category":"分类-2","stock":30},{"id":2,"name":"商品","price_cents":100,"category":"分类-2","stock":30}],"next_cursor":null}`,
	} {
		if err := validateResponse(configuration, request, http.StatusOK, headers, []byte(body)); !errors.Is(err, errResponseData) {
			t.Errorf("错误分类或业务边界未被拒绝: err=%v body=%s", err, body)
		}
	}
	for _, category := range []int{-1, 0, configuration.Categories + 1} {
		request.CategoryID = category
		if err := validateResponse(configuration, request, http.StatusOK, headers, []byte(correct)); !errors.Is(err, errResponseData) {
			t.Errorf("无效请求分类被放行: %d err=%v", category, err)
		}
	}
	request.CategoryID = 2
	for _, tenant := range []int{0, configuration.TenantCount + 1} {
		request.Tenant = tenant
		if err := validateResponse(configuration, request, http.StatusOK, headers, []byte(correct)); !errors.Is(err, errResponseData) {
			t.Errorf("非法期望租户被放行: tenant=%d err=%v", tenant, err)
		}
	}
}

func TestCatalogRequestCarriesCategoryExpectation(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "catalog"
	requests := newWorkload(configuration, time.Now())
	for sequence := uint64(0); sequence < 128; sequence++ {
		request := requests.request(sequence)
		target, err := url.Parse(request.Path)
		if err != nil {
			t.Fatal(err)
		}
		category, err := strconv.Atoi(target.Query().Get("category_id"))
		if err != nil || request.CategoryID != category || category < 1 || category > configuration.Categories {
			t.Fatalf("URL 与独立期望分类不一致: path=%s category=%d err=%v", request.Path, request.CategoryID, err)
		}
	}
}

type catalogContractTransport func(*http.Request) (*http.Response, error)

func (transport catalogContractTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// TestWrongCatalogCategoryCountsAsSemanticError 经过实际 perform/统计链验证错类响应不会进入成功吞吐。
func TestWrongCatalogCategoryCountsAsSemanticError(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario, configuration.TenantCount = "catalog", 1
	start := time.Now()
	client := &http.Client{Transport: catalogContractTransport(func(request *http.Request) (*http.Response, error) {
		requested, err := strconv.Atoi(request.URL.Query().Get("category_id"))
		if err != nil {
			t.Fatal(err)
		}
		other := requested%configuration.Categories + 1
		product := other
		for product%inactiveProductEvery == 0 {
			product += configuration.Categories
		}
		body := fmt.Sprintf(`{"items":[{"id":%d,"name":"商品","price_cents":100,"category":"分类-%d","stock":30}],"next_cursor":null}`, product, other)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	event := perform(context.Background(), configuration, newWorkload(configuration, start), client, scheduledRequest{When: start})
	statistics := newStatistics(configuration, start)
	statistics.add(event)
	report := statistics.finish(start.Add(time.Second), time.Second, false, 1)
	assertConservation(t, report)
	if report.Totals.SemanticErrors != 1 || report.Totals.Success != 0 || report.SuccessRPS != 0 ||
		report.IssuedSuccessRPS != 0 || report.ElapsedSuccessRPS != 0 || report.SuccessLatency.Count != 0 ||
		len(report.Errors) != 1 || report.Errors[0].Reason != errResponseData.Error() {
		t.Fatalf("错误分类被计为成功: totals=%+v errors=%+v rps=%v", report.Totals, report.Errors, report.SuccessRPS)
	}
}
