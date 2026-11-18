package connector

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"github.com/microsoft/go-mssqldb/msdsn"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestSQLURLDSNPreservesHostAndOptionalPort(t *testing.T) {
	cases := []struct {
		name, input, host string
		port              uint16
		hasZone           bool
	}{
		{"回环 IPv6", "::1", "::1", 0, false},
		{"完整 IPv6", "2001:db8::1", "2001:db8::1", 0, false},
		{"带区域 IPv6", "fe80::1%eth0", "fe80::1%eth0", 0, true},
		{"带括号区域 IPv6", "[fe80::1%eth0]", "fe80::1%eth0", 0, true},
		{"带括号 IPv6", "[::1]", "::1", 0, false},
		{"带端口 IPv6", "::1", "::1", 30432, false},
		{"带括号和端口 IPv6", "[::1]", "::1", 30432, false},
		{"IPv4", "127.0.0.1", "127.0.0.1", 0, false},
		{"IPv4 带端口", "127.0.0.1", "127.0.0.1", 30432, false},
		{"域名", "db.internal", "db.internal", 0, false},
		{"域名带端口", "db.internal", "db.internal", 30432, false},
	}

	defaultPgsql, err := pq.NewConfig(buildPgsqlDSN(db.Config{Hostname: "db.internal", Database: "thinkgo"}))
	if err != nil {
		t.Fatalf("解析 PostgreSQL 默认端口失败: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var port string
			wantPgsqlPort := tc.port
			wantMysqlPort := "3306"
			if tc.port == 0 {
				wantPgsqlPort = defaultPgsql.Port
			} else {
				port = strconv.Itoa(int(tc.port))
				wantMysqlPort = port
			}
			config := db.Config{Hostname: tc.input, Hostport: port, Database: "thinkgo"}

			// 1 & 2: PostgreSQL & SQL Server
			pgsqlDSN := buildPgsqlDSN(config)
			sqlsrvDSN := buildSqlsrvDSN(config)
			for _, dsn := range []string{pgsqlDSN, sqlsrvDSN} {
				parsed, err := url.Parse(dsn)
				if err != nil {
					t.Errorf("解析连接串 %q 失败: %v", dsn, err)
					continue
				}
				if parsed.Hostname() != tc.host || parsed.Port() != port {
					t.Errorf("%s 解析为主机 %q 端口 %q，期望 %q 和 %q", parsed.Scheme, parsed.Hostname(), parsed.Port(), tc.host, port)
				}
			}

			pgsql, err := pq.NewConfig(pgsqlDSN)
			if err != nil {
				t.Fatalf("PostgreSQL 驱动解析失败: %v", err)
			}
			if pgsql.Host != tc.host || pgsql.Port != wantPgsqlPort {
				t.Errorf("PostgreSQL 驱动解析为主机 %q 端口 %d，期望 %q 和 %d", pgsql.Host, pgsql.Port, tc.host, wantPgsqlPort)
			}

			sqlsrv, err := msdsn.Parse(sqlsrvDSN)
			if err != nil {
				t.Fatalf("SQL Server 驱动解析失败: %v", err)
			}
			if sqlsrv.Host != tc.host || sqlsrv.Port != uint64(tc.port) {
				t.Errorf("SQL Server 驱动解析为主机 %q 端口 %d，期望 %q 和 %d", sqlsrv.Host, sqlsrv.Port, tc.host, tc.port)
			}

			// 3: MySQL
			mysqlDSN, err := buildMysqlDSN(config)
			if err != nil {
				t.Fatalf("构建 MySQL DSN 失败: %v", err)
			}
			if strings.Contains(mysqlDSN, "[[") || strings.Contains(mysqlDSN, "]]") {
				t.Fatalf("MySQL DSN 出现双层方括号: %s", mysqlDSN)
			}
			parsedMysql, err := mysqlDriver.ParseDSN(mysqlDSN)
			if err != nil {
				t.Fatalf("MySQL 驱动解析 DSN %q 失败: %v", mysqlDSN, err)
			}
			mysqlHost, mysqlPort, err := net.SplitHostPort(parsedMysql.Addr)
			if err != nil {
				t.Fatalf("MySQL 解析后的 Addr %q 非合法 host:port: %v", parsedMysql.Addr, err)
			}
			if mysqlHost != tc.host || mysqlPort != wantMysqlPort {
				t.Errorf("MySQL Addr 解析为主机 %q 端口 %q，期望 %q 和 %q", mysqlHost, mysqlPort, tc.host, wantMysqlPort)
			}

			// 4, 5, 6: MongoDB, Neo4j, Oracle
			mongoURI, err := buildMongoURI(config)
			if err != nil {
				t.Fatalf("构建 MongoDB URI 失败: %v", err)
			}
			neo4jURI, err := buildNeo4jURI(db.Config{Hostname: tc.input, Hostport: port})
			if err != nil {
				t.Fatalf("构建 Neo4j URI 失败: %v", err)
			}
			oracleURI, err := buildOracleConnectString(config)
			if err != nil {
				t.Fatalf("构建 Oracle 连接串失败: %v", err)
			}
			for _, rawURI := range []string{mongoURI, neo4jURI, oracleURI} {
				if strings.Contains(rawURI, "[[") || strings.Contains(rawURI, "]]") {
					t.Fatalf("连接串出现非法双层方括号: %s", rawURI)
				}
				parsedURI, err := url.Parse(rawURI)
				if err != nil {
					t.Fatalf("解析 URI %q 失败: %v", rawURI, err)
				}
				if parsedURI.Hostname() != tc.host || parsedURI.Port() != port {
					t.Errorf("%s URI 解析为主机 %q 端口 %q，期望 %q 和 %q (raw=%q)", parsedURI.Scheme, parsedURI.Hostname(), parsedURI.Port(), tc.host, port, rawURI)
				}
			}
			if !tc.hasZone {
				if err := options.Client().ApplyURI(mongoURI).Validate(); err != nil {
					t.Fatalf("MongoDB 官方驱动拒绝 URI %q: %v", mongoURI, err)
				}
			}
		})
	}

	if _, err := buildOracleConnectString(db.Config{
		Hostname: "::1",
		Database: "ORCLPDB1",
		Params:   map[string]string{"protocol": "http"},
	}); !errors.Is(err, db.ErrInvalidDatabaseConfig) {
		t.Fatalf("Oracle 非法协议应返回 ErrInvalidDatabaseConfig，实际为 %v", err)
	}
}
