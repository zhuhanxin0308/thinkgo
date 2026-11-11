package loadrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

// TestDefaultWorkloadTrajectory 固定修复前请求轨迹摘要，确保默认实验的每个请求保持兼容。
func TestDefaultWorkloadTrajectory(t *testing.T) {
	for scenario, expected := range map[string]string{
		"catalog": "41256115ab9dee58aa7eff5add8b2bfd68a88084d9a0052eeed04192e14e0c70",
		"detail":  "84ec348904e96fe76592e0052e23fea9d4319a9c9557c54620c12b9bc9a3ef70",
		"order":   "45dd7839566fe121de6ca6b97859a7656fa0b45f317633b95fa33ae4efe2d713",
		"ticket":  "0f236b92ff84c87855904843f07968fc9826ec87039bfaa25411930ddd687d00",
		"mixed":   "d04f6889b45b33ce26b3bf1f8622c90514163aac6703a51e3d1d60834bfd0238",
	} {
		configuration := testConfig()
		configuration.Scenario = scenario
		workload := newWorkload(configuration, time.Unix(1700000000, 0))
		digest := sha256.New()
		for sequence := uint64(0); sequence < 1000; sequence++ {
			encoded, err := json.Marshal(workload.request(sequence))
			if err != nil {
				t.Fatal(err)
			}
			_, _ = digest.Write(encoded)
		}
		if got := hex.EncodeToString(digest.Sum(nil)); got != expected {
			t.Errorf("默认 %s 轨迹变化: got=%s", scenario, got)
		}
	}
}

// TestConfiguredDatasetControlsWorkload 验证模数、热点数量和热点比例由同一配置驱动。
func TestConfiguredDatasetControlsWorkload(t *testing.T) {
	const samples = 1000
	for _, modulus := range []int{2, 3, 10} {
		for _, hotWeight := range []int{0, 25, 80, 100} {
			configuration := testConfig()
			configuration.ProductsPerTenant = 400
			configuration.InactiveProductModulus = modulus
			configuration.HotProductsPerTenant = 75
			configuration.CacheHitWeight = hotWeight
			configuration.Scenario = "detail"
			if err := configuration.Validate(); err != nil {
				t.Fatal(err)
			}
			requests := newWorkload(configuration, time.Now())
			hot, cold := 0, 0
			seenCold := make(map[int64]bool)
			coldSpace := (configuration.ProductsPerTenant - configuration.ProductsPerTenant/modulus - configuration.HotProductsPerTenant) * configuration.TenantCount
			for sequence := uint64(0); sequence < samples; sequence++ {
				request := requests.request(sequence)
				local := request.ResourceID - int64(request.Tenant-1)*int64(configuration.ProductsPerTenant)
				if local < 1 || local > int64(configuration.ProductsPerTenant) || local%int64(modulus) == 0 {
					t.Fatalf("详情选择了下架或越界商品: modulus=%d local=%d", modulus, local)
				}
				if local-local/int64(modulus) <= int64(configuration.HotProductsPerTenant) {
					hot++
				} else {
					cold++
					if cold <= coldSpace && seenCold[request.ResourceID] {
						t.Fatal("尚未遍历全域冷键就提前复用")
					}
					seenCold[request.ResourceID] = true
				}
			}
			if hot != samples*hotWeight/100 {
				t.Fatalf("热点比例未生效: weight=%d hot=%d cold=%d", hotWeight, hot, cold)
			}
			configuration.Scenario = "order"
			requests = newWorkload(configuration, time.Now())
			for sequence := uint64(0); sequence < samples; sequence++ {
				request := requests.request(sequence)
				for _, item := range request.OrderItems {
					local := item.ProductID - int64(request.Tenant-1)*int64(configuration.ProductsPerTenant)
					if local%int64(modulus) == 0 || local <= 0 || local > int64(configuration.ProductsPerTenant) {
						t.Fatalf("订单商品与种子不一致: modulus=%d local=%d", modulus, local)
					}
				}
			}
		}
	}
}

// TestConfiguredMixedWeights 验证非默认权重会改变混合请求数量，而非被静默忽略。
func TestConfiguredMixedWeights(t *testing.T) {
	configuration := testConfig()
	configuration.Scenario = "mixed"
	configuration.MixedWeights = ScenarioWeights{Catalog: 10, Detail: 10, Order: 10, Ticket: 70}
	requests := newWorkload(configuration, time.Now())
	counts := make(map[string]int)
	for sequence := uint64(0); sequence < 1000; sequence++ {
		counts[requests.request(sequence).Scenario]++
	}
	for scenario, expected := range map[string]int{"catalog": 100, "detail": 100, "order": 100, "ticket": 700} {
		if counts[scenario] != expected {
			t.Errorf("混合权重未生效: scenario=%s got=%d want=%d", scenario, counts[scenario], expected)
		}
	}
}
