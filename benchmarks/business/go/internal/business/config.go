package business

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	minimumSecretBytes = 32
	maximumConfigBytes = 64 * 1024
	defaultAddress     = "127.0.0.1:18080"
)

var namespacePattern = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,96}$`)

// Dataset 是所有语言共同读取的业务边界和种子数据规模。
type Dataset struct {
	SchemaVersion            int            `json:"schema_version"`
	Seed                     int64          `json:"seed"`
	TenantCount              int64          `json:"tenant_count"`
	UsersPerTenant           int64          `json:"users_per_tenant"`
	ProductsPerTenant        int64          `json:"products_per_tenant"`
	CategoriesPerTenant      int64          `json:"categories_per_tenant"`
	TicketsPerTenant         int64          `json:"tickets_per_tenant"`
	SpecificationsPerProduct int            `json:"specifications_per_product"`
	InitialCommentsPerTicket int            `json:"initial_comments_per_ticket"`
	InitialStock             int64          `json:"initial_stock"`
	InactiveProductModulus   int64          `json:"inactive_product_modulus"`
	HotProductsPerTenant     int            `json:"hot_products_per_tenant"`
	CacheTTLSeconds          int            `json:"cache_ttl_seconds"`
	CacheHitWeight           int            `json:"cache_hit_weight"`
	CatalogPageSize          int            `json:"catalog_page_size"`
	MaximumPageSize          int            `json:"maximum_page_size"`
	MaximumPriceCents        int64          `json:"maximum_price_cents"`
	MaximumOrderItems        int            `json:"maximum_order_items"`
	MaximumItemQuantity      int64          `json:"maximum_item_quantity"`
	MinimumCommentCharacters int            `json:"minimum_comment_characters"`
	MaximumCommentCharacters int            `json:"maximum_comment_characters"`
	MaximumBodyBytes         int64          `json:"maximum_body_bytes"`
	DatabaseConnectionBudget int            `json:"database_connection_budget"`
	RedisConnectionBudget    int            `json:"redis_connection_budget"`
	RequestTimeoutSeconds    int            `json:"request_timeout_seconds"`
	MixedWeights             map[string]int `json:"mixed_weights"`
}

type Config struct {
	Address        string
	ContractDir    string
	DatabaseURL    string
	RedisAddress   string
	CacheNamespace string
	Secret         string
	Dataset        Dataset
}

func LoadDataset(path string) (Dataset, error) {
	var dataset Dataset
	data, err := readBoundedContractFile(path)
	if err != nil {
		return dataset, err
	}
	if len(data) > maximumConfigBytes {
		return dataset, errors.New("业务配置超过读取预算")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dataset); err != nil {
		return dataset, fmt.Errorf("业务配置非法: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return dataset, errors.New("业务配置包含多余数据")
	}
	return dataset, dataset.Validate()
}

func (dataset Dataset) Validate() error {
	if dataset.SchemaVersion != 1 || dataset.TenantCount < 1 || dataset.UsersPerTenant < 1 ||
		dataset.ProductsPerTenant < 1 || dataset.CategoriesPerTenant < 1 || dataset.TicketsPerTenant < 1 ||
		dataset.DatabaseConnectionBudget < 1 || dataset.RedisConnectionBudget < 1 ||
		dataset.RequestTimeoutSeconds < 1 || dataset.SpecificationsPerProduct < 1 ||
		dataset.InitialCommentsPerTicket < 1 || dataset.InitialStock < 1 || dataset.InactiveProductModulus < 2 ||
		dataset.HotProductsPerTenant < 1 || int64(dataset.HotProductsPerTenant) >= dataset.ProductsPerTenant ||
		dataset.CacheTTLSeconds < 1 || dataset.CacheHitWeight < 0 || dataset.CacheHitWeight > 100 ||
		dataset.CatalogPageSize < 1 || dataset.MaximumPageSize < dataset.CatalogPageSize ||
		dataset.MaximumPriceCents < 1 || dataset.MaximumOrderItems < 1 || dataset.MaximumItemQuantity < 1 ||
		dataset.MinimumCommentCharacters < 1 || dataset.MaximumCommentCharacters < dataset.MinimumCommentCharacters ||
		dataset.MaximumBodyBytes < int64(dataset.MaximumCommentCharacters) {
		return errors.New("业务配置的规模或预算无效")
	}
	weight := 0
	for _, scenario := range []string{"catalog", "detail", "order", "ticket"} {
		current, exists := dataset.MixedWeights[scenario]
		if !exists || current < 1 {
			return errors.New("混合场景必须包含全部业务")
		}
		weight += current
	}
	if len(dataset.MixedWeights) != 4 || weight != 100 {
		return errors.New("混合业务权重之和必须为100")
	}
	return nil
}

func LoadConfig() (Config, error) {
	config := Config{
		Address: os.Getenv("BENCH_ADDR"), ContractDir: os.Getenv("BENCH_CONTRACT_DIR"),
		DatabaseURL: os.Getenv("BENCH_DATABASE_URL"), RedisAddress: os.Getenv("BENCH_REDIS_ADDR"),
		CacheNamespace: os.Getenv("BENCH_NAMESPACE"), Secret: os.Getenv("BENCH_SECRET"),
	}
	if config.Address == "" {
		config.Address = defaultAddress
	}
	if config.ContractDir == "" {
		config.ContractDir = "../contracts"
	}
	if !localAddress(config.Address) || !localAddress(config.RedisAddress) {
		return config, errors.New("基准应用和Redis必须使用回环TCP地址")
	}
	database, err := url.Parse(config.DatabaseURL)
	if err != nil || database.Scheme != "postgres" || !loopbackHost(database.Hostname()) ||
		!strings.HasPrefix(strings.TrimPrefix(database.Path, "/"), "benchmark_") {
		return config, errors.New("基准数据库必须是回环服务上的benchmark_专用数据库")
	}
	if len(config.Secret) < minimumSecretBytes || !namespacePattern.MatchString(config.CacheNamespace) {
		return config, errors.New("签名密钥或缓存命名空间配置无效")
	}
	config.Dataset, err = LoadDataset(filepath.Join(config.ContractDir, "dataset.json"))
	return config, err
}

func localAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	return err == nil && port != "" && loopbackHost(host)
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
