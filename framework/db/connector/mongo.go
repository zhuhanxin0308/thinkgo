package connector

import (
	"context"
	"fmt"
	"time"

	"thinkgo/framework/db"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Mongo connector
type Mongo struct{}

// Connect connects to MongoDB
func (m *Mongo) Connect(config db.Config) (db.Connection, error) {
	// Build URI: mongodb://user:pass@host:port/db
	uri := fmt.Sprintf("mongodb://%s:%s@%s:%s",
		config.Username,
		config.Password,
		config.Hostname,
		config.Hostport,
	)
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}

	// Verify connection
	err = client.Ping(ctx, nil)
	if err != nil {
		return nil, err
	}

	return &db.MongoConnection{
		Client:   client,
		Database: config.Database,
	}, nil
}

func init() {
	db.RegisterConnector("mongo", &Mongo{})
}
