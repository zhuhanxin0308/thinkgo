package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"thinkgo/framework/db"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Neo4j connector
type Neo4j struct{}

// Connect connects to Neo4j
func (n *Neo4j) Connect(config db.Config) (db.Connection, error) {
	validated, err := validateConnectorConfig(config, "neo4j", true, false)
	if err != nil {
		return nil, err
	}
	if validated.Username == "" && validated.Password != "" {
		return nil, fmt.Errorf("%w: Neo4j 密码不能脱离用户名配置", db.ErrInvalidDatabaseConfig)
	}
	uri, err := buildNeo4jURI(validated)
	if err != nil {
		return nil, err
	}
	auth := neo4j.NoAuth()
	if validated.Username != "" {
		auth = neo4j.BasicAuth(validated.Username, validated.Password, "")
	}
	driver, err := neo4j.NewDriverWithContext(uri, auth)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
	defer cancel()
	if err := driver.VerifyConnectivity(ctx); err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
		defer closeCancel()
		return nil, errors.Join(err, driver.Close(closeCtx))
	}

	return &db.Neo4jConnection{
		Driver:           driver,
		Database:         validated.Database,
		OperationTimeout: remoteConnectionTimeout,
	}, nil
}

// buildNeo4jURI 默认使用加密协议，并只允许 Neo4j 官方驱动支持的协议。
func buildNeo4jURI(config db.Config) (string, error) {
	scheme := "neo4j+s"
	schemeConfigured := false
	for key, value := range config.Params {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey != key || !strings.EqualFold(trimmedKey, "scheme") || schemeConfigured {
			return "", fmt.Errorf("%w: 不支持的 Neo4j 连接参数 %q", db.ErrInvalidDatabaseConfig, key)
		}
		schemeConfigured = true
		if value = strings.TrimSpace(value); value != "" {
			scheme = strings.ToLower(value)
		}
	}
	if !isAllowedNeo4jScheme(scheme) {
		return "", fmt.Errorf("%w: 不支持的 Neo4j 连接协议 %q", db.ErrInvalidDatabaseConfig, scheme)
	}

	uri := url.URL{
		Scheme: scheme,
		Host:   joinHostPort(config.Hostname, config.Hostport),
	}
	return uri.String(), nil
}

// isAllowedNeo4jScheme 限制协议白名单，防止配置注入任意 URI scheme。
func isAllowedNeo4jScheme(scheme string) bool {
	switch scheme {
	case "neo4j+s", "neo4j+ssc", "neo4j", "bolt+s", "bolt+ssc", "bolt":
		return true
	default:
		return false
	}
}

func init() {
	mustRegisterConnector("neo4j", &Neo4j{})
}
