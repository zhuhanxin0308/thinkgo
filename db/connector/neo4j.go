package connector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/zhuhanxin0308/thinkgo/framework/db"
	neodriver "github.com/zhuhanxin0308/thinkgo/framework/db/driver/neo4j"

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
	return connectNeoWithDriver(validated, driver)
}

func connectNeoWithDriver(validated db.Config, driver neo4j.DriverWithContext) (db.Connection, error) {
	if driver == nil {
		return nil, db.ErrDatabaseUnavailable
	}
	connectivityCtx, connectivityCancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
	connectivityErr := driver.VerifyConnectivity(connectivityCtx)
	connectivityCancel()
	if connectivityErr != nil {
		return nil, errors.Join(connectivityErr, closeNeo4jDriver(driver))
	}

	probeCtx, probeCancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
	probeErr := verifyNeo4jTargetDatabase(probeCtx, driver, validated.Database)
	probeCancel()
	if probeErr != nil {
		return nil, errors.Join(probeErr, closeNeo4jDriver(driver))
	}

	return &neodriver.Neo4jConnection{
		Driver:           driver,
		Database:         validated.Database,
		OperationTimeout: remoteConnectionTimeout,
	}, nil
}

func verifyNeo4jTargetDatabase(ctx context.Context, driver neo4j.DriverWithContext, database string) (resultErr error) {
	if ctx == nil || driver == nil {
		return db.ErrDatabaseUnavailable
	}
	session := driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead, DatabaseName: database})
	if session == nil {
		return fmt.Errorf("%w: Neo4j 驱动返回空目标数据库会话", db.ErrDatabaseUnavailable)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
		defer cancel()
		resultErr = errors.Join(resultErr, session.Close(closeCtx))
	}()

	value, err := session.ExecuteRead(ctx, func(transaction neo4j.ManagedTransaction) (interface{}, error) {
		result, err := transaction.Run(ctx, "RETURN 1 AS thinkgo_probe", nil)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("%w: Neo4j 目标数据库探测返回空结果", db.ErrDatabaseUnavailable)
		}
		return result.Single(ctx)
	})
	if err != nil {
		return err
	}
	record, ok := value.(*neo4j.Record)
	if !ok || record == nil {
		return fmt.Errorf("%w: Neo4j 目标数据库探测结果类型为 %T", db.ErrDatabaseUnavailable, value)
	}
	probe, exists := record.Get("thinkgo_probe")
	if !exists || probe != int64(1) {
		return fmt.Errorf("%w: Neo4j 目标数据库探测结果非法", db.ErrDatabaseUnavailable)
	}
	return nil
}

func closeNeo4jDriver(driver neo4j.DriverWithContext) error {
	if driver == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteConnectionTimeout)
	defer cancel()
	return driver.Close(ctx)
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
