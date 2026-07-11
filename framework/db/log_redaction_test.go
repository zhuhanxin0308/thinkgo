package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestInsertErrorDoesNotLogValues 验证插入失败时错误日志不包含字段值（仅记录字段名），
// 防止密码/令牌/PII 通过日志泄露。
func TestInsertErrorDoesNotLogValues(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{executeErr: errors.New("boom")})
	database.SetLogger(logger)

	_, err := database.Table("users").Insert(map[string]interface{}{
		"username": "alice",
		"password": "S3cr3t-PII-value",
	})
	if err == nil {
		t.Fatal("插入失败应返回错误")
	}
	if len(logger.errorCalls) != 1 {
		t.Fatalf("应记录 1 条错误日志，实际 %d", len(logger.errorCalls))
	}

	ctx := logger.errorCalls[0].ctx
	dumped := fmt.Sprintf("%#v", ctx)
	if strings.Contains(dumped, "S3cr3t-PII-value") || strings.Contains(dumped, "alice") {
		t.Fatalf("错误日志不应包含字段值，实际 ctx: %s", dumped)
	}
	fields, ok := ctx["fields"].([]string)
	if !ok {
		t.Fatalf("错误日志应包含字段名列表，实际 ctx: %s", dumped)
	}
	// 字段名本身（非敏感）可保留，便于排障。
	joined := strings.Join(fields, ",")
	if !strings.Contains(joined, "password") || !strings.Contains(joined, "username") {
		t.Fatalf("字段名列表应包含 username/password，实际 %v", fields)
	}
}

// TestSelectErrorDoesNotLogArgValues 验证查询失败日志不含 WHERE 绑定值，只记录参数数量。
func TestSelectErrorDoesNotLogArgValues(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{selectErr: errors.New("boom")})
	database.SetLogger(logger)

	_, err := database.Table("users").Where("api_token = ?", "tok-PII-987").Select()
	if err == nil {
		t.Fatal("查询失败应返回错误")
	}
	ctx := logger.errorCalls[0].ctx
	dumped := fmt.Sprintf("%#v", ctx)
	if strings.Contains(dumped, "tok-PII-987") {
		t.Fatalf("错误日志不应包含 WHERE 绑定值，实际 ctx: %s", dumped)
	}
	if _, ok := ctx["arg_count"]; !ok {
		t.Fatalf("错误日志应以 arg_count 记录参数数量，实际 ctx: %s", dumped)
	}
}

// TestDBQueryRedactsSensitiveRawSQLLiterals 验证原生 SQL 失败日志不会记录字面量中的敏感值。
func TestDBQueryRedactsSensitiveRawSQLLiterals(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{queryErr: errors.New("boom")})
	database.SetLogger(logger)

	_, err := database.Query("SELECT * FROM users WHERE api_token = 'raw-secret-token'")
	if err == nil {
		t.Fatal("原生 SQL 查询失败应返回错误")
	}

	dumped := fmt.Sprintf("%#v", logger.errorCalls[0].ctx)
	if strings.Contains(dumped, "raw-secret-token") {
		t.Fatalf("原生 SQL 错误日志不应包含字面量敏感值，实际 ctx: %s", dumped)
	}
	if !strings.Contains(dumped, "[REDACTED]") {
		t.Fatalf("原生 SQL 错误日志应保留脱敏标记，实际 ctx: %s", dumped)
	}
}

// TestWhereRawErrorLogRedactsLiteralSecrets 验证 WhereRaw 失败日志不会记录原始条件中的敏感字面量。
func TestWhereRawErrorLogRedactsLiteralSecrets(t *testing.T) {
	logger := &dbTestLogger{}
	database := NewDB(&failingQueryConnection{selectErr: errors.New("boom")})
	database.SetLogger(logger)

	_, err := database.Table("users").WhereRaw("api_token = 'where-raw-secret'").Select()
	if err == nil {
		t.Fatal("WhereRaw 查询失败应返回错误")
	}

	dumped := fmt.Sprintf("%#v", logger.errorCalls[0].ctx)
	if strings.Contains(dumped, "where-raw-secret") {
		t.Fatalf("WhereRaw 错误日志不应包含字面量敏感值，实际 ctx: %s", dumped)
	}
	if !strings.Contains(dumped, "[REDACTED]") {
		t.Fatalf("WhereRaw 错误日志应保留脱敏标记，实际 ctx: %s", dumped)
	}
}
