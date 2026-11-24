package connector

import (
	"errors"
	"maps"
	"net/url"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

// TestOracleProtocolAliases 验证协议归一化与原始查询参数互不影响，所有输入均可重复使用。
func TestOracleProtocolAliases(t *testing.T) {
	const host, service = "localhost", "ORCL"
	for _, tc := range []struct {
		name      string
		values    []string
		want      string
		wantError bool
	}{
		{name: "默认", want: "tcps"},
		{name: "单个明文协议", values: []string{" TCP "}, want: "tcp"},
		{name: "混合大小写单键", values: []string{"tCpS"}, want: "tcps"},
		{name: "同义键值相同", values: []string{"tcp", " TCP ", "TcP"}, want: "tcp"},
		{name: "两个合法协议冲突", values: []string{"tcp", "tcps"}, wantError: true},
		{name: "合法及非法协议", values: []string{"unsupported", "tcps"}, wantError: true},
		{name: "多个不同非法值", values: []string{"other", "unsupported"}, wantError: true},
		{name: "相同非法值", values: []string{"unsupported", " UNSUPPORTED "}, wantError: true},
		{name: "显式空值", values: []string{""}, wantError: true},
		{name: "空白与空值", values: []string{" ", ""}, wantError: true},
		{name: "空值与合法协议", values: []string{"", "tcp"}, wantError: true},
		{name: "多键混合冲突", values: []string{"tcp", "tcps", "", "unsupported"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := []string{"PrOtOcOl", "protocol", "PROTOCOL", "Protocol"}
			var firstError string
			for _, reverse := range []bool{false, true} {
				// 其他查询参数保留原有键名和值，不能被协议键的折叠规则覆盖。
				params := map[string]string{"Mode": "Mixed Case", "mode": "lower"}
				for i := range tc.values {
					index := i
					if reverse {
						index = len(tc.values) - 1 - i
					}
					params[keys[index]] = tc.values[index]
				}
				before := maps.Clone(params)
				const reads = 32
				for range reads {
					dsn, err := buildOracleConnectString(db.Config{Hostname: host, Database: service, Params: params})
					if !maps.Equal(params, before) {
						t.Fatalf("构造连接串修改了原参数：%v", params)
					}
					if tc.wantError {
						if !errors.Is(err, db.ErrInvalidDatabaseConfig) || dsn != "" {
							t.Fatalf("非法配置没有在生成 DSN 前被拒绝：连接串 %q，错误 %v", dsn, err)
						}
						if firstError == "" {
							firstError = err.Error()
						} else if err.Error() != firstError {
							t.Fatalf("同一配置的错误不稳定：%q 和 %q", firstError, err)
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					uri, err := url.Parse(dsn)
					if err != nil {
						t.Fatal(err)
					}
					query := uri.Query()
					if uri.Scheme != tc.want || uri.Host != host || uri.Path != "/"+service ||
						len(query) != len(params)-len(tc.values) || query.Get("Mode") != "Mixed Case" || query.Get("mode") != "lower" {
						t.Fatalf("协议或普通参数错误：%s", dsn)
					}
				}
			}
		})
	}
}
