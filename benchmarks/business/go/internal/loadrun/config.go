// Package loadrun 为本机业务应用提供可复现、有界并发的 HTTP 压测。
package loadrun

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTenants           = 4
	defaultUsersPerTenant    = 1000
	defaultProductsPerTenant = 25000
	defaultTicketsPerTenant  = 5000
	defaultCategories        = 32
	defaultConcurrency       = 32
	defaultMaxInflight       = 256
	defaultErrorSampleLimit  = 20
	maximumWorkers           = 65536
	maximumTokenEntries      = 1000000
	maximumErrorSamples      = 10000
	catalogPageSize          = 20
	catalogMaximumPrice      = 100000000
	inactiveProductEvery     = 10
	hotProductCount          = 100
	detailCycleSize          = 10
	detailHotCount           = 9
	maximumOrderQuantity     = 3
	maximumResponseBytes     = 1 << 20
	maximumHeaderBytes       = 64 << 10
	tokenExpiryGrace         = 5 * time.Minute
	tcpKeepAlive             = 30 * time.Second
	percentageTotal          = 100
)

// ScenarioWeights 与数据集的四类业务权重保持同一字段结构。
type ScenarioWeights struct {
	Catalog int `json:"catalog"`
	Detail  int `json:"detail"`
	Order   int `json:"order"`
	Ticket  int `json:"ticket"`
}

// Config 的数据规模和种子必须与被测应用的种子数据一致；Secret 永远不序列化。
type Config struct {
	URL                    string          `json:"url"`
	Scenario               string          `json:"scenario"`
	Rate                   float64         `json:"rate_per_second"`
	Concurrency            int             `json:"concurrency"`
	MaxInflight            int             `json:"max_inflight"`
	Duration               time.Duration   `json:"duration_ns"`
	Timeout                time.Duration   `json:"timeout_ns"`
	Seed                   uint64          `json:"seed"`
	RunID                  string          `json:"run_id"`
	Secret                 string          `json:"-"`
	TenantCount            int             `json:"tenant_count"`
	UsersPerTenant         int             `json:"users_per_tenant"`
	ProductsPerTenant      int             `json:"products_per_tenant"`
	TicketsPerTenant       int             `json:"tickets_per_tenant"`
	Categories             int             `json:"categories"`
	ErrorSampleLimit       int             `json:"error_sample_limit"`
	InactiveProductModulus int             `json:"inactive_product_modulus"`
	HotProductsPerTenant   int             `json:"hot_products_per_tenant"`
	CacheHitWeight         int             `json:"cache_hit_weight"`
	MixedWeights           ScenarioWeights `json:"mixed_weights"`
}

// DefaultConfig 集中定义可通过命令行覆盖的实验默认值。
func DefaultConfig() Config {
	return Config{
		URL: "http://127.0.0.1:8080", Scenario: "mixed", Duration: time.Minute,
		Timeout: 5 * time.Second, Concurrency: defaultConcurrency, MaxInflight: defaultMaxInflight,
		Seed: 1, TenantCount: defaultTenants, UsersPerTenant: defaultUsersPerTenant,
		ProductsPerTenant: defaultProductsPerTenant, TicketsPerTenant: defaultTicketsPerTenant,
		Categories: defaultCategories, ErrorSampleLimit: defaultErrorSampleLimit,
		InactiveProductModulus: inactiveProductEvery, HotProductsPerTenant: hotProductCount,
		CacheHitWeight: detailHotCount * percentageTotal / detailCycleSize,
		MixedWeights:   ScenarioWeights{Catalog: 40, Detail: 30, Order: 20, Ticket: 10},
	}
}

// Validate 在启动工作协程前拒绝歧义目标、溢出和无效资源预算。
func (configuration Config) Validate() error {
	if _, err := parseTarget(configuration.URL); err != nil {
		return err
	}
	if !validScenario(configuration.Scenario) {
		return errors.New("scenario 必须为 catalog、detail、order、ticket 或 mixed")
	}
	if configuration.Duration <= 0 || configuration.Timeout <= 0 || configuration.Duration > time.Duration(math.MaxInt64)-configuration.Timeout-tokenExpiryGrace {
		return errors.New("duration 和 timeout 必须为正且总时长不能溢出")
	}
	if configuration.Rate < 0 || math.IsNaN(configuration.Rate) || math.IsInf(configuration.Rate, 0) || configuration.Rate*configuration.Duration.Seconds() >= float64(math.MaxInt64) {
		return errors.New("rate 必须是有限非负数，且计划请求数不能溢出")
	}
	if configuration.Concurrency < 1 || configuration.Concurrency > maximumWorkers || configuration.MaxInflight < 1 || configuration.MaxInflight > maximumWorkers {
		return fmt.Errorf("concurrency 和 max-inflight 必须在 1..%d 之间", maximumWorkers)
	}
	if configuration.TenantCount < 1 || configuration.UsersPerTenant < 1 || configuration.TenantCount > maximumTokenEntries/configuration.UsersPerTenant {
		return errors.New("tenant-count 和 users-per-tenant 必须为正，且缓存身份数不能超过预算")
	}
	if configuration.ProductsPerTenant < 1 || configuration.TicketsPerTenant < 1 || configuration.Categories < 1 || configuration.InactiveProductModulus < 2 {
		return errors.New("商品、工单与分类数量必须为正，下架模数至少为2")
	}
	activeProducts := configuration.ProductsPerTenant - configuration.ProductsPerTenant/configuration.InactiveProductModulus
	if configuration.HotProductsPerTenant < 1 || activeProducts <= configuration.HotProductsPerTenant {
		return errors.New("活动商品必须容纳配置的热点集合并至少保留一个冷商品")
	}
	if configuration.CacheHitWeight < 0 || configuration.CacheHitWeight > percentageTotal {
		return errors.New("热点权重必须在0到100之间")
	}
	weight := 0
	for _, current := range configuration.MixedWeights.values() {
		if current < 1 || current > percentageTotal {
			return errors.New("混合场景必须包含全部业务，单项权重必须在1到100之间")
		}
		weight += current
	}
	if weight != percentageTotal {
		return errors.New("混合业务权重之和必须为100")
	}
	for _, size := range []int{configuration.ProductsPerTenant, configuration.TicketsPerTenant} {
		if int64(size) > math.MaxInt64/int64(configuration.TenantCount) {
			return errors.New("全局数据主键溢出")
		}
	}
	if strings.TrimSpace(configuration.RunID) == "" || strings.TrimSpace(configuration.Secret) == "" {
		return errors.New("run-id 和认证密钥不能为空")
	}
	if configuration.ErrorSampleLimit < 0 || configuration.ErrorSampleLimit > maximumErrorSamples {
		return fmt.Errorf("error-samples 必须在 0..%d 之间", maximumErrorSamples)
	}
	return nil
}

func validScenario(scenario string) bool {
	switch scenario {
	case "catalog", "detail", "order", "ticket", "mixed":
		return true
	default:
		return false
	}
}

func parseTarget(target string) (*url.URL, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed == nil {
		return nil, errors.New("url 格式无效")
	}
	if parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("url 必须是无凭据、路径、查询与片段的本机 HTTP 地址")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			return nil, errors.New("压测目标仅允许 loopback 地址")
		}
	}
	if port := parsed.Port(); port != "" {
		value, conversionErr := strconv.Atoi(port)
		if conversionErr != nil || value < 1 || value > 65535 {
			return nil, errors.New("url 端口无效")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return nil, errors.New("url 不允许空端口")
	}
	return parsed, nil
}

// dialLoopback 不使用代理或 DNS，localhost 固定映射本机，避免目标校验与连接之间漂移。
func dialLoopback(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("仅允许 TCP 连接")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("连接地址无效")
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
		if network == "tcp6" {
			host = "::1"
		}
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("拒绝非 loopback 连接")
	}
	dialer := net.Dialer{KeepAlive: tcpKeepAlive}
	return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
}
