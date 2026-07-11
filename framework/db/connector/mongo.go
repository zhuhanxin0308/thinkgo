package connector

import (
	"context"
	"net/url"
	"time"

	"thinkgo/framework/db"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Mongo connector
type Mongo struct{}

// Connect connects to MongoDB
func (m *Mongo) Connect(config db.Config) (db.Connection, error) {
	uri := buildMongoURI(config)

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

// buildMongoURI 使用标准 URL 构造 MongoDB 地址，防止凭据中的特殊字符改写 URI 结构。
func buildMongoURI(config db.Config) string {
	query := url.Values{}
	for key, value := range config.Params {
		query.Set(key, value)
	}

	uri := url.URL{
		Scheme:   "mongodb",
		User:     url.UserPassword(config.Username, config.Password),
		Host:     joinHostPort(config.Hostname, config.Hostport),
		Path:     config.Database,
		RawQuery: query.Encode(),
	}
	return uri.String()
}

func init() {
	db.RegisterConnector("mongo", &Mongo{})
}
