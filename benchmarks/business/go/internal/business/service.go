package business

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

// Service 由六个Go框架复用，保证SQL、数据映射和事务算法完全一致。
type Service struct {
	config   Config
	database *sql.DB
	cache    *redis.Client
	queries  map[string]string
}

func Open(ctx context.Context, config Config) (*Service, error) {
	if err := config.Dataset.Validate(); err != nil {
		return nil, err
	}
	queries, err := loadQueries(filepath.Join(config.ContractDir, "queries.json"))
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("postgres", config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(config.Dataset.DatabaseConnectionBudget)
	database.SetMaxIdleConns(config.Dataset.DatabaseConnectionBudget)
	timeout := time.Duration(config.Dataset.RequestTimeoutSeconds) * time.Second
	startup, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := database.PingContext(startup); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	cache := redis.NewClient(&redis.Options{
		Addr: config.RedisAddress, PoolSize: config.Dataset.RedisConnectionBudget,
		MaxRetries: -1, Protocol: 2, DisableIdentity: true,
		DialTimeout: timeout, ReadTimeout: timeout, WriteTimeout: timeout,
		PoolTimeout: timeout, ContextTimeoutEnabled: true,
	})
	if err := cache.Ping(startup).Err(); err != nil {
		return nil, errors.Join(err, cache.Close(), database.Close())
	}
	return &Service{config: config, database: database, cache: cache, queries: queries}, nil
}

func (service *Service) Close() error {
	return errors.Join(service.cache.Close(), service.database.Close())
}

func (service *Service) Limits() Dataset { return service.config.Dataset }

func (service *Service) Authenticate(header string) (Principal, error) {
	return authenticate(header, service.config.Secret, time.Now(), service.config.Dataset)
}

func (service *Service) cacheKey(principal Principal, productID int64) string {
	return fmt.Sprintf("%s:product:%d:%d", service.config.CacheNamespace, principal.TenantID, productID)
}

func loadQueries(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maximumConfigBytes {
		return nil, errors.New("SQL合同超过读取预算")
	}
	var queries map[string]string
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, err
	}
	required := []string{
		"catalog", "product", "specifications", "reserve_order", "existing_order",
		"existing_order_items", "lock_product", "deduct_stock", "insert_order_item",
		"finish_order", "order_audit", "lock_ticket", "insert_comment", "update_ticket",
	}
	if len(queries) != len(required) {
		return nil, errors.New("SQL合同的查询集合不完整")
	}
	// 仅启动时转换共享SQL合同，业务包不链接任何被比较框架。
	for _, name := range required {
		query := queries[name]
		if strings.TrimSpace(query) == "" {
			return nil, fmt.Errorf("SQL合同缺少查询: %s", name)
		}
		queries[name], err = postgresQuery(query)
		if err != nil {
			return nil, fmt.Errorf("SQL合同查询%s无效: %w", name, err)
		}
	}
	return queries, nil
}

// postgresQuery 只支持合同使用的SQL子集，显式拒绝注释和美元引用，避免误绑参数。
func postgresQuery(query string) (string, error) {
	var result strings.Builder
	var quote byte
	parameter := 0
	for index := 0; index < len(query); index++ {
		current := query[index]
		if quote != 0 {
			result.WriteByte(current)
			if current == quote {
				if index+1 < len(query) && query[index+1] == quote {
					index++
					result.WriteByte(quote)
				} else {
					quote = 0
				}
			}
			continue
		}
		switch {
		case current == '\'' || current == '"':
			quote = current
			result.WriteByte(current)
		case current == '$' || strings.HasPrefix(query[index:], "--") || strings.HasPrefix(query[index:], "/*"):
			return "", errors.New("SQL合同不支持美元引用或注释")
		case current == '?':
			parameter++
			fmt.Fprintf(&result, "$%d", parameter)
		default:
			result.WriteByte(current)
		}
	}
	if quote != 0 {
		return "", errors.New("SQL合同存在未闭合引用")
	}
	return result.String(), nil
}

func rollback(tx *sql.Tx, result *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*result = errors.Join(*result, err)
	}
}
