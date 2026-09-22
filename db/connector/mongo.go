package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	mongodriver "github.com/zhuhanxin0308/thinkgo/v3/db/driver/mongo"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const remoteConnectionTimeout = 10 * time.Second

// mongoConnectFunc 描述 MongoDB v2 客户端的无上下文连接入口。
type mongoConnectFunc func(...*options.ClientOptions) (*mongo.Client, error)

// Mongo 提供 MongoDB 连接器实现。
type Mongo struct{}

// Connect 建立 MongoDB 连接并验证服务端可达性。
func (m *Mongo) Connect(config db.Config) (db.Connection, error) {
	return m.connectWith(config, mongo.Connect)
}

// connectWith 注入客户端创建函数，以便完整验证连接与失败清理路径。
func (m *Mongo) connectWith(config db.Config, connect mongoConnectFunc) (db.Connection, error) {
	if connect == nil {
		return nil, db.ErrDatabaseUnavailable
	}
	validated, err := validateConnectorConfig(config, "mongo", true, true)
	if err != nil {
		return nil, err
	}
	uri, err := buildMongoURI(validated)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
	defer cancel()

	client, err := connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}

	// 建连后主动探测，失败时使用独立上下文释放驱动资源。
	if err := client.Ping(ctx, nil); err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
		defer closeCancel()
		return nil, errors.Join(err, client.Disconnect(closeCtx))
	}

	return &mongodriver.MongoConnection{
		Client:           client,
		Database:         validated.Database,
		OperationTimeout: remoteConnectionTimeout,
	}, nil
}

// buildMongoURI 使用标准 URL 构造 MongoDB 地址，防止凭据中的特殊字符改写 URI 结构。
func buildMongoURI(config db.Config) (string, error) {
	if err := validateMongoDatabaseName(config.Database); err != nil {
		return "", err
	}
	if config.Username == "" && config.Password != "" {
		return "", fmt.Errorf("%w: MongoDB 密码不能脱离用户名配置", db.ErrInvalidDatabaseConfig)
	}
	if strings.ContainsRune(config.Username, 0) || strings.ContainsRune(config.Password, 0) {
		return "", fmt.Errorf("%w: MongoDB 凭据包含非法字符", db.ErrInvalidDatabaseConfig)
	}

	query := url.Values{}
	seenOptions := make(map[string]struct{}, len(config.Params))
	tlsConfigured := false
	for key, value := range config.Params {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || trimmedKey != key {
			return "", fmt.Errorf("%w: MongoDB 连接参数名 %q 非法", db.ErrInvalidDatabaseConfig, key)
		}
		normalized := strings.ToLower(trimmedKey)
		if _, exists := seenOptions[normalized]; exists {
			return "", fmt.Errorf("%w: MongoDB 连接参数 %q 重复", db.ErrInvalidDatabaseConfig, key)
		}
		seenOptions[normalized] = struct{}{}
		if normalized == "tls" || normalized == "ssl" {
			if tlsConfigured {
				return "", fmt.Errorf("%w: MongoDB TLS 参数重复", db.ErrInvalidDatabaseConfig)
			}
			normalizedValue := strings.ToLower(strings.TrimSpace(value))
			if normalizedValue != "true" && normalizedValue != "false" {
				return "", fmt.Errorf("%w: MongoDB %s 必须是 true 或 false", db.ErrInvalidDatabaseConfig, key)
			}
			value = normalizedValue
			tlsConfigured = true
		}
		query.Set(key, value)
	}
	if !tlsConfigured {
		query.Set("tls", "true")
	}

	uri := url.URL{
		Scheme:   "mongodb",
		Host:     joinHostPort(config.Hostname, config.Hostport),
		Path:     config.Database,
		RawQuery: query.Encode(),
	}
	if config.Username != "" {
		uri.User = url.UserPassword(config.Username, config.Password)
	}
	return uri.String(), nil
}

// validateMongoDatabaseName 使用跨平台最严格字符集约束数据库名。
func validateMongoDatabaseName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) >= 64 || strings.ContainsRune(name, 0) || strings.ContainsAny(name, `/\\.$*<>:|?`) {
		return fmt.Errorf("%w: MongoDB 数据库名非法", db.ErrInvalidDatabaseConfig)
	}
	return nil
}
