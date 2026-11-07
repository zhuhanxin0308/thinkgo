package loadrun

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWorkloadReproducibilityAndMix(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "mixed"
	configuration.Seed = 712
	first := newWorkload(configuration, time.Unix(1700000000, 0))
	second := newWorkload(configuration, time.Unix(1700000000, 0))
	counts := make(map[string]int)
	for sequence := uint64(0); sequence < 1000; sequence++ {
		left, right := first.request(sequence), second.request(sequence)
		if !reflect.DeepEqual(left, right) {
			t.Fatalf("相同种子没有产生相同请求: %d", sequence)
		}
		counts[left.Scenario]++
		if left.Tenant < 1 || left.Tenant > configuration.TenantCount {
			t.Fatalf("租户映射越界: %#v", left)
		}
		parts := strings.Split(strings.TrimPrefix(left.Token, "Bearer "), ".")
		if len(parts) != 4 {
			t.Fatalf("认证格式无效: %d段", len(parts))
		}
		mac := hmac.New(sha256.New, []byte(configuration.Secret))
		mac.Write([]byte(strings.Join(parts[:3], ".")))
		if parts[3] != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatal("认证签名与共同协议不符")
		}
		if left.Scenario == "ticket" {
			local := left.ResourceID - int64(left.Tenant-1)*int64(configuration.TicketsPerTenant)
			owner := int64(left.Tenant-1)*int64(configuration.UsersPerTenant) + (local-1)%int64(configuration.UsersPerTenant) + 1
			if parts[1] != strconv.FormatInt(owner, 10) {
				t.Fatal("工单身份与所属用户不符")
			}
		}
		if left.Scenario == "order" {
			if len(left.OrderItems) != 2 || left.OrderItems[0].ProductID == left.OrderItems[1].ProductID || len(left.IdempotencyKey) != 32 {
				t.Fatalf("订单输入无效: %#v", left)
			}
			for _, item := range left.OrderItems {
				local := item.ProductID - int64(left.Tenant-1)*int64(configuration.ProductsPerTenant)
				if local <= 0 || local > int64(configuration.ProductsPerTenant) || local%10 == 0 || item.Quantity < 1 || item.Quantity > 3 {
					t.Fatalf("订单商品映射越界: %#v", item)
				}
			}
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"catalog": 400, "detail": 300, "order": 200, "ticket": 100}) {
		t.Fatalf("混合比例漂移: %#v", counts)
	}
}

func TestDetailHotColdSchedule(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "detail"
	configuration.TenantCount = 1
	workload := newWorkload(configuration, time.Now())
	hot, cold := 0, 0
	previousCold := int64(111)
	for sequence := uint64(0); sequence < 1000; sequence++ {
		request := workload.request(sequence)
		if request.ResourceID%10 == 0 {
			t.Fatal("读取了下架商品")
		}
		if request.ResourceID <= 111 {
			hot++
		} else {
			cold++
			if request.ResourceID <= previousCold {
				t.Fatalf("冷键未顺序推进: %d -> %d", previousCold, request.ResourceID)
			}
			previousCold = request.ResourceID
		}
	}
	if hot != 900 || cold != 100 {
		t.Fatalf("冷热比例错误: hot=%d cold=%d", hot, cold)
	}
}

func TestDetailColdKeysVisitCompleteTenantSpaceBeforeReuse(t *testing.T) {
	for _, scenario := range []string{"detail", "mixed"} {
		t.Run(scenario, func(t *testing.T) {
			configuration := testConfig()
			configuration.Scenario = scenario
			configuration.TenantCount = 4
			configuration.ProductsPerTenant = 200
			requests := newWorkload(configuration, time.Now())
			coldPerTenant := configuration.ProductsPerTenant - configuration.ProductsPerTenant/inactiveProductEvery - hotProductCount
			coldSpace := coldPerTenant * configuration.TenantCount
			seen := make(map[int64]bool, coldSpace)
			byTenant := make(map[int]int)
			coldRequests, detailRequests := 0, 0
			sequenceLimit := uint64((coldSpace + 1) * detailCycleSize * len(requests.mix))
			for sequence := uint64(0); sequence < sequenceLimit; sequence++ {
				request := requests.request(sequence)
				if request.Scenario != "detail" {
					continue
				}
				detailRequests++
				if (request.ResourceID-1)/int64(configuration.ProductsPerTenant)+1 != int64(request.Tenant) {
					t.Fatalf("详情商品与租户不一致: tenant=%d product=%d", request.Tenant, request.ResourceID)
				}
				claims := strings.Split(strings.TrimPrefix(request.Token, "Bearer "), ".")
				if len(claims) != 4 || claims[0] != strconv.Itoa(request.Tenant) {
					t.Fatal("详情认证租户与目标不一致")
				}
				user, err := strconv.ParseInt(claims[1], 10, 64)
				if err != nil || (user-1)/int64(configuration.UsersPerTenant)+1 != int64(request.Tenant) {
					t.Fatal("详情认证用户不属于目标租户")
				}
				local := request.ResourceID - int64(request.Tenant-1)*int64(configuration.ProductsPerTenant)
				if local <= activeProductID(hotProductCount-1) {
					continue
				}
				if local > int64(configuration.ProductsPerTenant) || local%inactiveProductEvery == 0 {
					t.Fatalf("冷键越界或指向下架商品: %d", local)
				}
				coldRequests++
				if coldRequests > coldSpace {
					if !seen[request.ResourceID] {
						t.Fatal("完整冷集合之外出现新商品")
					}
					break
				}
				if seen[request.ResourceID] {
					t.Fatalf("尚未遍历完整租户冷集合就复用: ordinal=%d unique=%d expected=%d", coldRequests, len(seen), coldSpace)
				}
				seen[request.ResourceID] = true
				byTenant[request.Tenant]++
			}
			if len(seen) != coldSpace || coldRequests != coldSpace+1 || detailRequests != coldRequests*detailCycleSize {
				t.Fatalf("全域遍历或独立90/10比例错误: unique=%d cold=%d detail=%d", len(seen), coldRequests, detailRequests)
			}
			for tenant := 1; tenant <= configuration.TenantCount; tenant++ {
				if byTenant[tenant] != coldPerTenant {
					t.Fatalf("租户冷键没有完整遍历: tenant=%d got=%d want=%d", tenant, byTenant[tenant], coldPerTenant)
				}
			}
		})
	}
}
