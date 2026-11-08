package loadrun

import (
	"math"
	"reflect"
	"testing"
	"time"
)

// TestDetailColdOrdinalAtSequenceBoundary 保证最大序号下冷热请求不越界，也不破坏固定种子重放。
func TestDetailColdOrdinalAtSequenceBoundary(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "detail"
	configuration.TenantCount = 4
	configuration.ProductsPerTenant = 200
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	first := newWorkload(configuration, now)
	second := newWorkload(configuration, now)
	for _, sequence := range []uint64{0, 9, math.MaxUint64 - 6, math.MaxUint64 - 1, math.MaxUint64} {
		left := first.request(sequence)
		right := second.request(sequence)
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("相同种子和最大序号无法重放: %d", sequence)
		}
		if left.Tenant < 1 || left.Tenant > configuration.TenantCount {
			t.Fatalf("请求租户越界: sequence=%d tenant=%d", sequence, left.Tenant)
		}
		local := left.ResourceID - int64(left.Tenant-1)*int64(configuration.ProductsPerTenant)
		if local < 1 || local > int64(configuration.ProductsPerTenant) || local%inactiveProductEvery == 0 {
			t.Fatalf("请求商品越界或被下架: sequence=%d local=%d", sequence, local)
		}
		if sequence%detailCycleSize == detailCycleSize-1 && local <= activeProductID(hotProductCount-1) {
			t.Fatalf("冷请求意外命中热点: sequence=%d local=%d", sequence, local)
		}
	}
}
