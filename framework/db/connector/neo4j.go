package connector

import (
	"context"
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
	uri, err := buildNeo4jURI(config)
	if err != nil {
		return nil, err
	}
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(config.Username, config.Password, ""))
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	err = driver.VerifyConnectivity(ctx)
	if err != nil {
		return nil, err
	}

	return &db.Neo4jConnection{
		Driver: driver,
	}, nil
}

// buildNeo4jURI 默认使用加密协议，并只允许 Neo4j 官方驱动支持的协议。
func buildNeo4jURI(config db.Config) (string, error) {
	scheme := "neo4j+s"
	if value := strings.TrimSpace(config.Params["scheme"]); value != "" {
		scheme = strings.ToLower(value)
	}
	if !isAllowedNeo4jScheme(scheme) {
		return "", fmt.Errorf("不支持的 Neo4j 连接协议: %s", scheme)
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
	db.RegisterConnector("neo4j", &Neo4j{})
}
