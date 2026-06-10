package connector

import (
	"context"
	"fmt"
	"thinkgo/framework/db"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Neo4j connector
type Neo4j struct{}

// Connect connects to Neo4j
func (n *Neo4j) Connect(config db.Config) (db.Connection, error) {
	uri := fmt.Sprintf("bolt://%s:%s", config.Hostname, config.Hostport)
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

func init() {
	db.RegisterConnector("neo4j", &Neo4j{})
}
