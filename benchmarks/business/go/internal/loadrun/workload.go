package loadrun

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

const ticketCommentBody = "本次业务压测验证真实工单评论、用户归属与事务版本更新。Benchmark comment validates the complete business transaction."

type orderItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int64 `json:"quantity"`
}

type requestSpec struct {
	Scenario       string
	Method         string
	Path           string
	Body           []byte
	Token          string
	IdempotencyKey string
	Tenant         int
	CategoryID     int
	ResourceID     int64
	OrderItems     []orderItem
}

type workload struct {
	configuration Config
	tokens        [][]string
	mix           []string
	positions     []uint64
	weights       map[string]uint64
	detailCycle   uint64
	detailHot     uint64
}

// newWorkload 在计时前缓存所有合法身份；每个请求仅由种子和全局序号决定。
func newWorkload(configuration Config, now time.Time) *workload {
	result := &workload{
		configuration: configuration,
		tokens:        make([][]string, configuration.TenantCount),
		weights:       make(map[string]uint64),
	}
	weights := configuration.MixedWeights.values()
	divisor := 0
	for _, weight := range weights {
		divisor = weightGCD(divisor, weight)
	}
	for index, scenario := range []string{"catalog", "detail", "order", "ticket"} {
		count := weights[index] / divisor
		result.weights[scenario] = uint64(count)
		for range count {
			result.mix = append(result.mix, scenario)
		}
	}
	hotDivisor := weightGCD(configuration.CacheHitWeight, percentageTotal)
	result.detailCycle = uint64(percentageTotal / hotDivisor)
	result.detailHot = uint64(configuration.CacheHitWeight / hotDivisor)
	expiry := now.Add(configuration.Duration + configuration.Timeout + tokenExpiryGrace).Unix()
	for tenant := 1; tenant <= configuration.TenantCount; tenant++ {
		result.tokens[tenant-1] = make([]string, configuration.UsersPerTenant)
		for localUser := 1; localUser <= configuration.UsersPerTenant; localUser++ {
			user := int64(tenant-1)*int64(configuration.UsersPerTenant) + int64(localUser)
			payload := fmt.Sprintf("%d.%d.%d", tenant, user, expiry)
			mac := hmac.New(sha256.New, []byte(configuration.Secret))
			_, _ = mac.Write([]byte(payload))
			result.tokens[tenant-1][localUser-1] = "Bearer " + payload + "." + hex.EncodeToString(mac.Sum(nil))
		}
	}
	// #nosec G404 -- PCG 仅生成可复现的非安全性场景排列，鉴权令牌仍使用独立的 HMAC-SHA256。
	random := rand.New(rand.NewPCG(configuration.Seed, configuration.Seed))
	random.Shuffle(len(result.mix), func(left, right int) { result.mix[left], result.mix[right] = result.mix[right], result.mix[left] })
	result.positions = make([]uint64, len(result.mix))
	seen := make(map[string]uint64)
	for index, scenario := range result.mix {
		result.positions[index] = seen[scenario]
		seen[scenario]++
	}
	return result
}

func (workload *workload) scenario(sequence uint64) (string, uint64) {
	if workload.configuration.Scenario != "mixed" {
		return workload.configuration.Scenario, sequence
	}
	position := sequence % uint64(len(workload.mix))
	scenario := workload.mix[position]
	ordinal := sequence/uint64(len(workload.mix))*workload.weights[scenario] + workload.positions[position]
	return scenario, ordinal
}

func (workload *workload) request(sequence uint64) requestSpec {
	configuration := workload.configuration
	scenario, ordinal := workload.scenario(sequence)
	// #nosec G404 -- 每个请求的 PCG 仅确定租户与业务样本，不生成密钥、签名或认证随机数。
	random := rand.New(rand.NewPCG(configuration.Seed, sequence))
	tenant := random.IntN(configuration.TenantCount) + 1
	localUser := random.IntN(configuration.UsersPerTenant) + 1
	request := requestSpec{Scenario: scenario, Method: http.MethodGet, Tenant: tenant}
	productOffset := int64(tenant-1) * int64(configuration.ProductsPerTenant)
	activeProducts := configuration.ProductsPerTenant - configuration.ProductsPerTenant/configuration.InactiveProductModulus
	switch scenario {
	case "catalog":
		category := random.IntN(configuration.Categories) + 1
		request.CategoryID = category
		request.Path = fmt.Sprintf("/api/catalog?category_id=%d&min_price=0&max_price=%d&cursor_price=0&cursor_id=0&limit=%d", category, catalogMaximumPrice, catalogPageSize)
	case "detail":
		var activeIndex int
		if ordinal%workload.detailCycle < workload.detailHot {
			activeIndex = random.IntN(configuration.HotProductsPerTenant)
		} else {
			// 冷请求按租户交错遍历全域键空间，全部租户的冷键访问完之后才允许复用。
			coldOrdinal := ordinal/workload.detailCycle*(workload.detailCycle-workload.detailHot) + ordinal%workload.detailCycle - workload.detailHot
			// #nosec G115 -- 配置验证要求租户数为正且总身份数至多一百万，取模后必能装入 int。
			tenant = int(coldOrdinal%uint64(configuration.TenantCount)) + 1
			request.Tenant = tenant
			productOffset = int64(tenant-1) * int64(configuration.ProductsPerTenant)
			// #nosec G115 -- 冷商品总数为正，余数小于已验证的 int 商品规模，索引加热点偏移仍在界内。
			activeIndex = configuration.HotProductsPerTenant + int(coldOrdinal/uint64(configuration.TenantCount)%uint64(activeProducts-configuration.HotProductsPerTenant))
		}
		request.ResourceID = productOffset + activeProductID(activeIndex, configuration.InactiveProductModulus)
		request.Path = "/api/products/" + strconv.FormatInt(request.ResourceID, 10)
	case "order":
		first := random.IntN(activeProducts)
		// 从排除首个索引后的区间抽样，避免大数据规模时两个索引相加溢出。
		second := random.IntN(activeProducts - 1)
		if second >= first {
			second++
		}
		request.OrderItems = []orderItem{
			{ProductID: productOffset + activeProductID(first, configuration.InactiveProductModulus), Quantity: int64(random.IntN(maximumOrderQuantity) + 1)},
			{ProductID: productOffset + activeProductID(second, configuration.InactiveProductModulus), Quantity: int64(random.IntN(maximumOrderQuantity) + 1)},
		}
		request.Method, request.Path = http.MethodPost, "/api/orders"
		request.Body, _ = json.Marshal(struct {
			Items []orderItem `json:"items"`
		}{Items: request.OrderItems})
		digest := sha256.Sum256([]byte(configuration.RunID + strconv.FormatUint(sequence, 10)))
		request.IdempotencyKey = hex.EncodeToString(digest[:16])
	case "ticket":
		localTicket := random.IntN(configuration.TicketsPerTenant) + 1
		localUser = (localTicket-1)%configuration.UsersPerTenant + 1
		request.ResourceID = int64(tenant-1)*int64(configuration.TicketsPerTenant) + int64(localTicket)
		request.Method = http.MethodPost
		request.Path = "/api/tickets/" + strconv.FormatInt(request.ResourceID, 10) + "/comments"
		request.Body, _ = json.Marshal(struct {
			Body string `json:"body"`
		}{Body: ticketCommentBody})
	}
	request.Token = workload.tokens[tenant-1][localUser-1]
	return request
}

// activeProductID 按数据集的下架模数，将连续活动序号映射到种子主键。
func activeProductID(index, modulus int) int64 {
	activePerBlock := modulus - 1
	return int64(index/activePerBlock)*int64(modulus) + int64(index%activePerBlock) + 1
}
