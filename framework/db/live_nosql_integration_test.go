//go:build integration

package db_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"thinkgo/framework/db"
	"thinkgo/framework/db/connector"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var liveNoSQLSequence atomic.Uint64

func liveResourceName(prefix string) string {
	return fmt.Sprintf("%s_%d_%d_%d", prefix, os.Getpid(), time.Now().UnixNano(), liveNoSQLSequence.Add(1))
}

func liveMongoConfigFromEnv() (db.Config, bool) {
	host, hostOK := os.LookupEnv("THINKGO_LIVE_MONGO_HOST")
	port, portOK := os.LookupEnv("THINKGO_LIVE_MONGO_PORT")
	database, databaseOK := os.LookupEnv("THINKGO_LIVE_MONGO_DATABASE")
	if !hostOK || !portOK || !databaseOK || host == "" || port == "" || database == "" {
		return db.Config{}, false
	}
	config := db.Config{
		Type:     "mongo",
		Hostname: host,
		Hostport: port,
		Database: database,
		Username: os.Getenv("THINKGO_LIVE_MONGO_USER"),
		Password: os.Getenv("THINKGO_LIVE_MONGO_PASSWORD"),
		Params:   map[string]string{},
	}
	if tls, exists := os.LookupEnv("THINKGO_LIVE_MONGO_TLS"); exists {
		config.Params["tls"] = tls
	}
	if authSource, exists := os.LookupEnv("THINKGO_LIVE_MONGO_AUTH_SOURCE"); exists {
		config.Params["authSource"] = authSource
	}
	return config, true
}

func liveNeoConfigFromEnv() (db.Config, bool) {
	host, hostOK := os.LookupEnv("THINKGO_LIVE_NEO4J_HOST")
	port, portOK := os.LookupEnv("THINKGO_LIVE_NEO4J_PORT")
	database, databaseOK := os.LookupEnv("THINKGO_LIVE_NEO4J_DATABASE")
	if !hostOK || !portOK || !databaseOK || host == "" || port == "" || database == "" {
		return db.Config{}, false
	}
	config := db.Config{
		Type:     "neo4j",
		Hostname: host,
		Hostport: port,
		Database: database,
		Username: os.Getenv("THINKGO_LIVE_NEO4J_USER"),
		Password: os.Getenv("THINKGO_LIVE_NEO4J_PASSWORD"),
		Params:   map[string]string{},
	}
	if scheme, exists := os.LookupEnv("THINKGO_LIVE_NEO4J_SCHEME"); exists {
		config.Params["scheme"] = scheme
	}
	return config, true
}

func connectLiveMongo(tb testing.TB, config db.Config) *db.DB {
	tb.Helper()
	connection, err := (&connector.Mongo{}).Connect(config)
	if err != nil {
		tb.Fatalf("connect live MongoDB: %v", err)
	}
	database := db.NewDB(connection)
	tb.Cleanup(func() {
		if err := database.Close(); err != nil {
			tb.Errorf("close live MongoDB: %v", err)
		}
	})
	return database
}

func connectLiveNeo(tb testing.TB, config db.Config) *db.DB {
	tb.Helper()
	connection, err := (&connector.Neo4j{}).Connect(config)
	if err != nil {
		tb.Fatalf("connect live Neo4j: %v", err)
	}
	database := db.NewDB(connection)
	tb.Cleanup(func() {
		if err := database.Close(); err != nil {
			tb.Errorf("close live Neo4j: %v", err)
		}
	})
	return database
}

func dropLiveMongoCollection(database *db.DB, collection string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return database.WithConnection(func(connection db.Connection) error {
		mongoConnection, ok := connection.(*db.MongoConnection)
		if !ok || mongoConnection.Client == nil {
			return fmt.Errorf("%w: live Mongo connection type is %T", db.ErrDatabaseUnavailable, connection)
		}
		return mongoConnection.Client.Database(mongoConnection.Database).Collection(collection).Drop(ctx)
	})
}

func seedLiveMongo(database *db.DB, collection string, rows int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return database.WithConnection(func(connection db.Connection) error {
		mongoConnection, ok := connection.(*db.MongoConnection)
		if !ok || mongoConnection.Client == nil {
			return fmt.Errorf("%w: live Mongo connection type is %T", db.ErrDatabaseUnavailable, connection)
		}
		nativeCollection := mongoConnection.Client.Database(mongoConnection.Database).Collection(collection)
		for start := 1; start <= rows; start += 1_000 {
			batch := make([]bson.D, 0, 1_000)
			for index := start; index < start+1_000 && index <= rows; index++ {
				batch = append(batch, bson.D{{Key: "sequence", Value: index}, {Key: "name", Value: fmt.Sprintf("user-%06d", index)}})
			}
			if _, err := nativeCollection.InsertMany(ctx, batch); err != nil {
				return err
			}
		}
		return nil
	})
}

func executeLiveNeoWrite(database *db.DB, cypher string, params map[string]interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return database.WithConnection(func(connection db.Connection) (resultErr error) {
		neoConnection, ok := connection.(*db.Neo4jConnection)
		if !ok || neoConnection.Driver == nil {
			return fmt.Errorf("%w: live Neo4j connection type is %T", db.ErrDatabaseUnavailable, connection)
		}
		session := neoConnection.Driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite, DatabaseName: neoConnection.Database})
		if session == nil {
			return db.ErrDatabaseUnavailable
		}
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer closeCancel()
			resultErr = errors.Join(resultErr, session.Close(closeCtx))
		}()
		_, resultErr = session.ExecuteWrite(ctx, func(transaction neo4j.ManagedTransaction) (interface{}, error) {
			result, err := transaction.Run(ctx, cypher, params)
			if err != nil {
				return nil, err
			}
			if result == nil {
				return nil, db.ErrInvalidDatabaseRow
			}
			_, err = result.Consume(ctx)
			return nil, err
		})
		return resultErr
	})
}

func cleanupLiveNeo(database *db.DB, labels ...string) error {
	for _, label := range labels {
		if err := executeLiveNeoWrite(database, fmt.Sprintf("MATCH (n:`%s`) DETACH DELETE n", label), nil); err != nil {
			return err
		}
	}
	return nil
}

type liveMongoModelUser struct {
	ID   string `thinkgo:"id"`
	Name string `thinkgo:"name"`
}

func TestLiveMongoOperationContract(t *testing.T) {
	config, ok := liveMongoConfigFromEnv()
	if !ok {
		t.Skip("THINKGO_LIVE_MONGO_* 未配置")
	}
	database := connectLiveMongo(t, config)
	collection := liveResourceName("bench_orm_users")
	t.Cleanup(func() {
		if err := dropLiveMongoCollection(database, collection); err != nil {
			t.Errorf("drop live Mongo collection: %v", err)
		}
	})

	id, err := database.Name(collection).InsertGetId(map[string]interface{}{"name": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := database.Name(collection).WhereField("_id", "=", id).UpdateResult(map[string]interface{}{"name": "Grace"})
	if err != nil || !updated.MatchedKnown || updated.Matched != 1 || !updated.ModifiedKnown || updated.Modified != 1 {
		t.Fatalf("Mongo result=%#v err=%v", updated, err)
	}
	row, err := database.Name(collection).Field("name").WhereField("_id", "=", id).Find()
	if err != nil || row["name"] != "Grace" {
		t.Fatalf("Mongo projected row=%#v err=%v", row, err)
	}
	if _, exists := row["_id"]; exists {
		t.Fatalf("Mongo explicit projection leaked _id: %#v", row)
	}

	model := db.NewModel(database, collection)
	modelUser := &liveMongoModelUser{Name: "Model Ada"}
	if err := model.Create(modelUser); err != nil || modelUser.ID == "" {
		t.Fatalf("Mongo default Model create=%#v err=%v", modelUser, err)
	}
	modelUser.Name = "Model Grace"
	if err := model.Save(modelUser); err != nil {
		t.Fatalf("Mongo default Model save: %v", err)
	}
	modelRow, err := model.Where("id", modelUser.ID).Find()
	if err != nil || modelRow["id"] != modelUser.ID || modelRow["name"] != "Model Grace" {
		t.Fatalf("Mongo default Model row=%#v err=%v", modelRow, err)
	}
	if _, exists := modelRow["_id"]; exists {
		t.Fatalf("Mongo default Model exposed storage _id: %#v", modelRow)
	}
	deleted, err := model.Where("id", modelUser.ID).Delete()
	if err != nil || deleted != 1 {
		t.Fatalf("Mongo default Model delete=%d err=%v", deleted, err)
	}
}

// TestLiveMongoStreamingContract 验证真实 MongoDB 游标流式读取和回调提前停止。
func TestLiveMongoStreamingContract(t *testing.T) {
	config, ok := liveMongoConfigFromEnv()
	if !ok {
		t.Skip("THINKGO_LIVE_MONGO_* 未配置")
	}
	database := connectLiveMongo(t, config)
	collection := liveResourceName("live_mongo_stream")
	if err := seedLiveMongo(database, collection, 2_000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dropLiveMongoCollection(database, collection); err != nil {
			t.Errorf("drop live Mongo stream collection: %v", err)
		}
	})
	count := 0
	err := database.Name(collection).Field("sequence,name").Order("sequence ASC").Each(func(row map[string]interface{}) bool {
		count++
		return count < 37
	})
	if err != nil {
		t.Fatalf("真实 MongoDB 流式读取失败: %v", err)
	}
	if count != 37 {
		t.Fatalf("真实 MongoDB 流式读取提前停止错误: %d", count)
	}
}

func TestLiveNeoStrictAndDetachDelete(t *testing.T) {
	config, ok := liveNeoConfigFromEnv()
	if !ok {
		t.Skip("THINKGO_LIVE_NEO4J_* 未配置")
	}
	database := connectLiveNeo(t, config)
	parentLabel := liveResourceName("bench_orm_parent")
	childLabel := liveResourceName("bench_orm_child")
	t.Cleanup(func() {
		if err := cleanupLiveNeo(database, parentLabel, childLabel); err != nil {
			t.Errorf("cleanup live Neo4j labels: %v", err)
		}
	})
	parentID := liveResourceName("parent")
	childID := liveResourceName("child")
	seed := fmt.Sprintf("CREATE (p:`%s` {id:$parent}), (c:`%s` {id:$child}), (p)-[:BENCH_ORM_REL]->(c)", parentLabel, childLabel)
	if err := executeLiveNeoWrite(database, seed, map[string]interface{}{"parent": parentID, "child": childID}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Name(parentLabel).WhereField("id", "=", parentID).Delete(); err == nil {
		t.Fatal("strict Delete should reject a node with an existing relationship")
	}
	result, err := database.Name(parentLabel).WhereField("id", "=", parentID).DetachDeleteResult()
	if err != nil || result.Deleted != 1 || !result.RelatedDeletedKnown || result.RelatedDeleted < 1 {
		t.Fatalf("detach result=%#v err=%v", result, err)
	}
}

func runFixedBenchmarkWorkers(b *testing.B, concurrency int, operation func() error) {
	b.Helper()
	var next atomic.Int64
	var failed atomic.Bool
	var failureOnce sync.Once
	var failure error
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for worker := 0; worker < concurrency; worker++ {
		go func() {
			defer workers.Done()
			for !failed.Load() {
				index := next.Add(1)
				if index > int64(b.N) {
					return
				}
				if err := operation(); err != nil {
					failureOnce.Do(func() {
						failure = err
						failed.Store(true)
					})
					return
				}
			}
		}()
	}
	workers.Wait()
	if failure != nil {
		b.Fatal(failure)
	}
}

func BenchmarkLiveMongoMaterialization(b *testing.B) {
	config, ok := liveMongoConfigFromEnv()
	if !ok {
		b.Skip("THINKGO_LIVE_MONGO_* 未配置")
	}
	database := connectLiveMongo(b, config)
	for _, rows := range []int{10_000, 100_000} {
		collection := liveResourceName("bench_orm_mongo")
		if err := seedLiveMongo(database, collection, rows); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() {
			if err := dropLiveMongoCollection(database, collection); err != nil {
				b.Errorf("drop live Mongo benchmark collection: %v", err)
			}
		})
		for _, scenario := range []string{"materialize", "deep-skip", "escaped-regex"} {
			for _, concurrency := range []int{1, 32, 256} {
				b.Run(fmt.Sprintf("rows-%d/%s/concurrency-%d", rows, scenario, concurrency), func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					runFixedBenchmarkWorkers(b, concurrency, func() error {
						var result []map[string]interface{}
						var err error
						switch scenario {
						case "materialize":
							result, err = database.Name(collection).Field("sequence,name").Order("sequence ASC").Limit(rows).Select()
							if err == nil && len(result) != rows {
								err = fmt.Errorf("materialized=%d want=%d", len(result), rows)
							}
						case "deep-skip":
							result, err = database.Name(collection).Field("sequence,name").Order("sequence ASC").Offset(rows - 100).Limit(100).Select()
							if err == nil && len(result) != 100 {
								err = fmt.Errorf("deep-skip rows=%d want=100", len(result))
							}
						case "escaped-regex":
							result, err = database.Name(collection).Field("sequence,name").WhereLike("name", "%999%").Limit(100).Select()
							if err == nil && len(result) == 0 {
								err = errors.New("escaped regex returned no rows")
							}
						}
						return err
					})
				})
			}
		}
	}
}

// BenchmarkLiveMongoProjectionWidth 量化 MongoDB 结果列数对客户端物化成本的影响。
func BenchmarkLiveMongoProjectionWidth(b *testing.B) {
	config, ok := liveMongoConfigFromEnv()
	if !ok {
		b.Skip("THINKGO_LIVE_MONGO_* 未配置")
	}
	database := connectLiveMongo(b, config)
	collection := liveResourceName("bench_orm_mongo_projection")
	const rows = 100_000
	if err := seedLiveMongo(database, collection, rows); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := dropLiveMongoCollection(database, collection); err != nil {
			b.Errorf("drop live Mongo projection collection: %v", err)
		}
	})
	for _, scenario := range []struct {
		name   string
		fields string
	}{
		{name: "one-column", fields: "sequence"},
		{name: "two-columns", fields: "sequence,name"},
		{name: "all-fields", fields: "*"},
	} {
		b.Run(scenario.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				result, err := database.Name(collection).Field(scenario.fields).Order("sequence ASC").Limit(rows).Select()
				if err != nil {
					b.Fatal(err)
				}
				if len(result) != rows {
					b.Fatalf("Mongo projection rows=%d want=%d", len(result), rows)
				}
			}
		})
	}
}

// BenchmarkLiveMongoStreaming 对比真实 MongoDB 完整物化和逐行消费的延迟与分配。
func BenchmarkLiveMongoStreaming(b *testing.B) {
	config, ok := liveMongoConfigFromEnv()
	if !ok {
		b.Skip("THINKGO_LIVE_MONGO_* 未配置")
	}
	database := connectLiveMongo(b, config)
	collection := liveResourceName("bench_orm_mongo_stream")
	const rows = 100_000
	if err := seedLiveMongo(database, collection, rows); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := dropLiveMongoCollection(database, collection); err != nil {
			b.Errorf("drop live Mongo stream benchmark collection: %v", err)
		}
	})
	for _, scenario := range []string{"materialize", "stream"} {
		b.Run(scenario, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				switch scenario {
				case "materialize":
					result, err := database.Name(collection).Field("sequence,name").Order("sequence ASC").Limit(rows).Select()
					if err != nil || len(result) != rows {
						b.Fatalf("MongoDB 完整物化失败: rows=%d err=%v", len(result), err)
					}
				case "stream":
					count := 0
					err := database.Name(collection).Field("sequence,name").Order("sequence ASC").Limit(rows).Each(func(map[string]interface{}) bool {
						count++
						return true
					})
					if err != nil || count != rows {
						b.Fatalf("MongoDB 流式读取失败: rows=%d err=%v", count, err)
					}
				}
			}
		})
	}
}

func BenchmarkLiveNeoSessionCollect(b *testing.B) {
	config, ok := liveNeoConfigFromEnv()
	if !ok {
		b.Skip("THINKGO_LIVE_NEO4J_* 未配置")
	}
	database := connectLiveNeo(b, config)
	for _, rows := range []int{10_000, 100_000} {
		label := liveResourceName("bench_orm_neo")
		seed := fmt.Sprintf("UNWIND range(1, $rows) AS id CREATE (n:`%s` {id:id, name:'user-'+toString(id)})", label)
		if err := executeLiveNeoWrite(database, seed, map[string]interface{}{"rows": int64(rows)}); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() {
			if err := cleanupLiveNeo(database, label); err != nil {
				b.Errorf("cleanup live Neo4j benchmark label: %v", err)
			}
		})
		for _, concurrency := range []int{1, 32, 256} {
			b.Run(fmt.Sprintf("rows-%d/concurrency-%d", rows, concurrency), func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				runFixedBenchmarkWorkers(b, concurrency, func() error {
					result, err := database.Name(label).Field("id,name").Order("id ASC").Limit(rows).Select()
					if err == nil && len(result) != rows {
						err = fmt.Errorf("Neo4j collected=%d want=%d", len(result), rows)
					}
					return err
				})
			})
		}
	}
}
