package connector

import (
	"context"
	"errors"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
	neodriver "github.com/zhuhanxin0308/thinkgo/v3/db/driver/neo4j"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type connectorNeoProbeResult struct {
	neo4j.ResultWithContext
	record *neo4j.Record
}

func (result *connectorNeoProbeResult) Single(context.Context) (*neo4j.Record, error) {
	return result.record, nil
}

type connectorNeoProbeTransaction struct {
	neo4j.ManagedTransaction
	result neo4j.ResultWithContext
}

func (transaction *connectorNeoProbeTransaction) Run(context.Context, string, map[string]interface{}) (neo4j.ResultWithContext, error) {
	return transaction.result, nil
}

type connectorNeoProbeSession struct {
	neo4j.SessionWithContext
	databaseError    error
	executeReadCalls int
	closeCalls       int
}

func (session *connectorNeoProbeSession) ExecuteRead(ctx context.Context, work neo4j.ManagedTransactionWork, _ ...func(*neo4j.TransactionConfig)) (interface{}, error) {
	session.executeReadCalls++
	if session.databaseError != nil {
		return nil, session.databaseError
	}
	result := &connectorNeoProbeResult{record: &neo4j.Record{Keys: []string{"thinkgo_probe"}, Values: []interface{}{int64(1)}}}
	return work(&connectorNeoProbeTransaction{result: result})
}

func (session *connectorNeoProbeSession) Close(context.Context) error {
	session.closeCalls++
	return nil
}

type connectorFakeNeoDriver struct {
	neo4j.DriverWithContext
	connectivityError error
	databaseError     error
	lastDatabase      string
	verifyCalls       int
	closeCalls        int
	session           *connectorNeoProbeSession
}

func (driver *connectorFakeNeoDriver) VerifyConnectivity(context.Context) error {
	driver.verifyCalls++
	return driver.connectivityError
}

func (driver *connectorFakeNeoDriver) NewSession(_ context.Context, config neo4j.SessionConfig) neo4j.SessionWithContext {
	driver.lastDatabase = config.DatabaseName
	driver.session = &connectorNeoProbeSession{databaseError: driver.databaseError}
	return driver.session
}

func (driver *connectorFakeNeoDriver) Close(context.Context) error {
	driver.closeCalls++
	return nil
}

func TestNeoConnectVerifiesConfiguredDatabase(t *testing.T) {
	driver := &connectorFakeNeoDriver{databaseError: errors.New("database not found")}
	config := db.Config{Type: "neo4j", Database: "tenant_a"}
	connection, err := connectNeoWithDriver(config, driver)
	if err == nil || connection != nil {
		t.Fatalf("missing target database must fail: connection=%#v err=%v", connection, err)
	}
	if driver.verifyCalls != 1 || driver.lastDatabase != "tenant_a" || driver.closeCalls != 1 {
		t.Fatalf("target database was not verified and cleaned up: %#v", driver)
	}
	if driver.session == nil || driver.session.executeReadCalls != 1 || driver.session.closeCalls != 1 {
		t.Fatalf("target database probe did not consume and close its session: %#v", driver.session)
	}
}

func TestNeoConnectReturnsOnlyAfterSuccessfulTargetProbe(t *testing.T) {
	driver := &connectorFakeNeoDriver{}
	config := db.Config{Type: "neo4j", Database: "tenant_a"}
	connection, err := connectNeoWithDriver(config, driver)
	if err != nil {
		t.Fatal(err)
	}
	neoConnection, ok := connection.(*neodriver.Neo4jConnection)
	if !ok || neoConnection.Database != "tenant_a" {
		t.Fatalf("connector returned wrong connection: %#v", connection)
	}
	if driver.verifyCalls != 1 || driver.lastDatabase != "tenant_a" || driver.closeCalls != 0 || driver.session.executeReadCalls != 1 || driver.session.closeCalls != 1 {
		t.Fatalf("successful target probe lifecycle is wrong: %#v", driver)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if driver.closeCalls != 1 {
		t.Fatalf("connection close did not close the driver: %d", driver.closeCalls)
	}
}

func TestNeoConnectClosesDriverOnConnectivityFailure(t *testing.T) {
	driver := &connectorFakeNeoDriver{connectivityError: errors.New("offline")}
	connection, err := connectNeoWithDriver(db.Config{Type: "neo4j", Database: "tenant_a"}, driver)
	if err == nil || connection != nil || driver.closeCalls != 1 || driver.session != nil {
		t.Fatalf("connectivity failure lifecycle is wrong: connection=%#v driver=%#v err=%v", connection, driver, err)
	}
}

// TestBuildNeo4jURIUsesSecureDefault 验证 Neo4j 默认使用加密协议，避免明文 Bolt 成为默认连接方式。
func TestBuildNeo4jURIUsesSecureDefault(t *testing.T) {
	uri, err := buildNeo4jURI(db.Config{
		Hostname: "neo4j.internal",
		Hostport: "7687",
	})
	if err != nil {
		t.Fatalf("默认 Neo4j URI 不应报错: %v", err)
	}
	if uri != "neo4j+s://neo4j.internal:7687" {
		t.Fatalf("默认 Neo4j URI 应使用 neo4j+s，实际为 %q", uri)
	}
}

// TestBuildNeo4jURIAllowsExplicitSchemeOverride 验证本地开发可显式降级协议。
func TestBuildNeo4jURIAllowsExplicitSchemeOverride(t *testing.T) {
	uri, err := buildNeo4jURI(db.Config{
		Hostname: "127.0.0.1",
		Hostport: "7687",
		Params: map[string]string{
			"scheme": "bolt",
		},
	})
	if err != nil {
		t.Fatalf("显式 Neo4j URI 协议不应报错: %v", err)
	}
	if uri != "bolt://127.0.0.1:7687" {
		t.Fatalf("Neo4j URI 应尊重显式协议，实际为 %q", uri)
	}
}

// TestBuildNeo4jURIRejectsUnsupportedScheme 验证不允许通过配置注入未知协议。
func TestBuildNeo4jURIRejectsUnsupportedScheme(t *testing.T) {
	if _, err := buildNeo4jURI(db.Config{Params: map[string]string{"scheme": "http"}}); err == nil {
		t.Fatal("Neo4j URI 应拒绝不受支持的协议")
	}
}

// TestBuildNeo4jURIRejectsIgnoredParameters 验证连接器不会接受随后被静默丢弃的参数。
func TestBuildNeo4jURIRejectsIgnoredParameters(t *testing.T) {
	for _, params := range []map[string]string{
		{"scheme": "neo4j+s", "ignored": "value"},
		{" scheme ": "bolt"},
	} {
		_, err := buildNeo4jURI(db.Config{Params: params})
		if !errors.Is(err, db.ErrInvalidDatabaseConfig) {
			t.Fatalf("未知或空白 Neo4j 参数应返回 ErrInvalidDatabaseConfig: params=%v err=%v", params, err)
		}
	}
}
