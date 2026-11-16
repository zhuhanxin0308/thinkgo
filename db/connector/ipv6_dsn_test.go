package connector

import (
	"net/url"
	"strconv"
	"testing"

	"github.com/lib/pq"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

func TestSQLURLDSNPreservesHostAndOptionalPort(t *testing.T) {
	cases := []struct {
		name, input, host string
		port              uint16
	}{
		{"回环 IPv6", "::1", "::1", 0},
		{"完整 IPv6", "2001:db8::1", "2001:db8::1", 0},
		{"带区域 IPv6", "fe80::1%eth0", "fe80::1%eth0", 0},
		{"带括号 IPv6", "[::1]", "::1", 0},
		{"带端口 IPv6", "::1", "::1", 30432},
		{"带括号和端口 IPv6", "[::1]", "::1", 30432},
		{"IPv4", "127.0.0.1", "127.0.0.1", 0},
		{"域名", "db.internal", "db.internal", 0},
	}

	defaultPgsql, err := pq.NewConfig(buildPgsqlDSN(db.Config{Hostname: "db.internal", Database: "thinkgo"}))
	if err != nil {
		t.Fatalf("解析 PostgreSQL 默认端口失败: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var port string
			wantPgsqlPort := tc.port
			if tc.port == 0 {
				wantPgsqlPort = defaultPgsql.Port
			} else {
				port = strconv.Itoa(int(tc.port))
			}
			config := db.Config{Hostname: tc.input, Hostport: port, Database: "thinkgo"}
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
		})
	}
}
