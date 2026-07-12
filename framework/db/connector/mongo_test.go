package connector

import (
	"context"
	"errors"
	"net/url"
	"sync/atomic"
	"testing"

	"thinkgo/framework/db"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/xoptions"
)

// trackingMongoDeployment 记录连接器是否确实释放了底层驱动资源。
type trackingMongoDeployment struct {
	*drivertest.MockDeployment
	disconnected atomic.Bool
}

// Disconnect 记录断开行为后调用官方模拟部署的资源释放逻辑。
func (d *trackingMongoDeployment) Disconnect(ctx context.Context) error {
	d.disconnected.Store(true)
	return d.MockDeployment.Disconnect(ctx)
}

// newTrackingMongoClient 创建可观测断开行为的 MongoDB v2 模拟客户端。
func newTrackingMongoClient(t *testing.T, responses ...bson.D) (*mongo.Client, *trackingMongoDeployment) {
	t.Helper()

	deployment := &trackingMongoDeployment{MockDeployment: drivertest.NewMockDeployment(responses...)}
	clientOptions := options.Client()
	if err := xoptions.SetInternalClientOptions(clientOptions, "deployment", deployment); err != nil {
		t.Fatalf("配置 MongoDB 模拟部署失败: %v", err)
	}
	client, err := mongo.Connect(clientOptions)
	if err != nil {
		t.Fatalf("创建 MongoDB 模拟客户端失败: %v", err)
	}
	return client, deployment
}

// TestMongoConnectVerifiesReachabilityAndReturnsV2Connection 验证连接器使用 v2 客户端探测可达性并返回完整连接。
func TestMongoConnectVerifiesReachabilityAndReturnsV2Connection(t *testing.T) {
	client, deployment := newTrackingMongoClient(t, bson.D{{Key: "ok", Value: 1}})
	connector := &Mongo{}
	connection, err := connector.connectWith(db.Config{
		Type:     "mongo",
		Hostname: "mongo.internal",
		Hostport: "27017",
		Database: "app",
	}, func(clientOptions ...*options.ClientOptions) (*mongo.Client, error) {
		if len(clientOptions) != 1 || clientOptions[0] == nil {
			t.Fatalf("MongoDB 连接器必须传入唯一有效的客户端配置")
		}
		if err := clientOptions[0].Validate(); err != nil {
			t.Fatalf("MongoDB v2 客户端配置必须有效: %v", err)
		}
		return client, nil
	})
	if err != nil {
		t.Fatalf("MongoDB 连接不应失败: %v", err)
	}
	mongoConnection, ok := connection.(*db.MongoConnection)
	if !ok || mongoConnection.Client != client || mongoConnection.Database != "app" {
		t.Fatalf("MongoDB 连接结果不完整: %#v", connection)
	}
	if err := connection.Close(); err != nil {
		t.Fatalf("关闭 MongoDB 连接失败: %v", err)
	}
	if !deployment.disconnected.Load() {
		t.Fatal("关闭连接必须释放 MongoDB 驱动部署")
	}
}

// TestMongoConnectClosesClientAfterPingFailure 验证可达性探测失败不会泄漏已创建的客户端。
func TestMongoConnectClosesClientAfterPingFailure(t *testing.T) {
	client, deployment := newTrackingMongoClient(t, bson.D{
		{Key: "ok", Value: 0},
		{Key: "code", Value: int32(91)},
		{Key: "errmsg", Value: "shutdown in progress"},
	})
	connector := &Mongo{}
	connection, err := connector.connectWith(db.Config{Hostname: "mongo.internal", Database: "app"}, func(...*options.ClientOptions) (*mongo.Client, error) {
		return client, nil
	})
	if err == nil || connection != nil {
		t.Fatalf("MongoDB 探测失败必须返回错误且不暴露连接: connection=%#v err=%v", connection, err)
	}
	if !deployment.disconnected.Load() {
		t.Fatal("MongoDB 探测失败必须释放已创建的客户端")
	}
	if connection, err := connector.connectWith(db.Config{Hostname: "mongo.internal", Database: "app"}, nil); !errors.Is(err, db.ErrDatabaseUnavailable) || connection != nil {
		t.Fatalf("缺少客户端创建函数必须返回 ErrDatabaseUnavailable: connection=%#v err=%v", connection, err)
	}
}

// TestBuildMongoURIEscapesCredentials 验证 MongoDB URI 不会被账号、密码或库名中的特殊字符破坏。
func TestBuildMongoURIEscapesCredentials(t *testing.T) {
	uri, err := buildMongoURI(db.Config{
		Username: "mongo:user",
		Password: "p@ss/word?authSource=admin",
		Hostname: "mongo.internal",
		Hostport: "27017",
		Database: "think go",
		Params: map[string]string{
			"authSource": "admin",
		},
	})
	if err != nil {
		t.Fatalf("构造 MongoDB URI 失败: %v", err)
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("MongoDB URI 应为可解析 URL，实际错误: %v", err)
	}
	password, _ := parsed.User.Password()
	if parsed.User.Username() != "mongo:user" || password != "p@ss/word?authSource=admin" {
		t.Fatalf("MongoDB URI 凭据解析错误: user=%q password=%q", parsed.User.Username(), password)
	}
	if parsed.Host != "mongo.internal:27017" || parsed.EscapedPath() != "/think%20go" {
		t.Fatalf("MongoDB URI 地址或库名解析错误: host=%q path=%q", parsed.Host, parsed.EscapedPath())
	}
	if parsed.Query().Get("authSource") != "admin" {
		t.Fatalf("MongoDB URI 应保留显式参数，实际 authSource=%q", parsed.Query().Get("authSource"))
	}
	if parsed.Query().Get("tls") != "true" {
		t.Fatalf("MongoDB URI 默认必须启用 TLS，实际 tls=%q", parsed.Query().Get("tls"))
	}
}

// TestBuildMongoURIHonorsExplicitTLSAndRejectsAmbiguity 验证显式本地降级可用，
// 同时拒绝大小写不同的重复选项和无用户名密码，避免驱动解析产生歧义。
func TestBuildMongoURIHonorsExplicitTLSAndRejectsAmbiguity(t *testing.T) {
	uri, err := buildMongoURI(db.Config{
		Hostname: "127.0.0.1",
		Database: "app",
		Params:   map[string]string{"tls": "false"},
	})
	if err != nil {
		t.Fatalf("显式关闭本地 TLS 不应失败: %v", err)
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Query().Get("tls") != "false" {
		t.Fatalf("显式 TLS 设置未保留: uri=%q err=%v", uri, err)
	}

	_, err = buildMongoURI(db.Config{Database: "app", Params: map[string]string{"tls": "true", "TLS": "false"}})
	if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("重复 MongoDB 选项应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
	_, err = buildMongoURI(db.Config{Password: "secret", Database: "app"})
	if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("无用户名密码应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
	for _, params := range []map[string]string{
		{" tls ": "false"},
		{"tls": "not-a-bool"},
	} {
		if _, err := buildMongoURI(db.Config{Database: "app", Params: params}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("非法 MongoDB TLS 参数应返回 ErrInvalidDatabaseConfig: params=%v err=%v", params, err)
		}
	}
}
