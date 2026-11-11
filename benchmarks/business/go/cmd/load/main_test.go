package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIValidatesFlagsAndSecret(t *testing.T) {
	for _, args := range [][]string{
		{"-unknown"},
		{"-duration=broken"},
		{"-inactive-product-modulus=1", "-run-id=test", "-output=result.json"},
		{"-cache-hit-weight=101", "-run-id=test", "-output=result.json"},
		{"-catalog-weight=99", "-run-id=test", "-output=result.json"},
		{"-url=http://example.com", "-run-id=test", "-output=result.json"},
		{"-run-id=test"},
		{"-output=result.json"},
		{"-run-id=test", "-output=result.json", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, func(string) string { return "test-secret" }); code != 2 || stderr.Len() == 0 {
			t.Fatalf("无效参数未失败: %#v code=%d err=%s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-run-id=test", "-output=result.json"}, &stdout, &stderr, func(string) string { return "" }); code != 2 {
		t.Fatalf("缺少密钥未失败: %d", code)
	}
	if code := run(context.Background(), []string{"-h"}, &stdout, &stderr, func(string) string { return "" }); code != 0 {
		t.Fatalf("帮助命令失败: %d", code)
	}
}

func TestCLIProducesReportAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"items":[{"id":1,"name":"商品","price_cents":100,"stock":10,"category":"分类"}],"next_cursor":null}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "measurement.json.gz")
	args := []string{"-url=" + server.URL, "-scenario=catalog", "-tenant-count=1", "-run-id=cli-unit", "-duration=20ms", "-concurrency=1", "-output=" + path}
	var stdout, stderr bytes.Buffer
	lookup := func(name string) string {
		if name != "BENCH_SECRET" {
			t.Fatalf("错误默认密钥变量: %s", name)
		}
		return "private-cli-secret"
	}
	if code := run(context.Background(), args, &stdout, &stderr, lookup); code != 0 {
		t.Fatalf("运行失败: code=%d %s", code, stderr.String())
	}
	if _, err := os.Stat(path); err != nil || !strings.Contains(stdout.String(), "success_rps") || strings.Contains(stdout.String()+stderr.String(), "private-cli-secret") {
		t.Fatalf("结果文件、摘要或凭据隔离错误: %v %s", err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), args, &stdout, &stderr, lookup); code != 1 {
		t.Fatalf("重复输出没有拒绝覆盖: code=%d", code)
	}
}

func TestCLICancellationSavesPartialResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "canceled.json")
	var stdout, stderr bytes.Buffer
	args := []string{"-run-id=canceled", "-output=" + path, "-duration=1h", "-rate=100", "-max-inflight=1"}
	if code := run(ctx, args, &stdout, &stderr, func(string) string { return "private-test-secret" }); code != 1 {
		t.Fatalf("取消状态错误: %d", code)
	}
	if _, err := os.Stat(path); err != nil || !strings.Contains(stdout.String(), `"canceled":true`) || !strings.Contains(stderr.String(), "已保存部分结果") {
		t.Fatalf("取消结果未保留: %v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

type failingOutput struct{}

func (failingOutput) Write([]byte) (int, error) { return 0, errors.New("测试输出不可写") }

func TestCLIRejectsMissingDirectoryAndReportsSummaryFailure(t *testing.T) {
	var stderr bytes.Buffer
	lookup := func(string) string { return "private-test-secret" }
	args := []string{"-run-id=directory", "-output=" + filepath.Join(t.TempDir(), "absent", "run.json")}
	if code := run(context.Background(), args, failingOutput{}, &stderr, lookup); code != 1 {
		t.Fatalf("缺失目录未阻止启动: %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	args = []string{"-run-id=summary", "-output=" + filepath.Join(t.TempDir(), "run.json"), "-concurrency=1"}
	if code := run(ctx, args, failingOutput{}, &stderr, lookup); code != 1 || !strings.Contains(stderr.String(), "输出摘要失败") {
		t.Fatalf("摘要写入失败被忽略: %d %s", code, stderr.String())
	}
}
