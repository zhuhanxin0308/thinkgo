package otlp

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestOTLPRejectsManagedEncodingHeaders 验证传输层独立管理请求编码和响应编码协商。
func TestOTLPRejectsManagedEncodingHeaders(t *testing.T) {
	for _, name := range []string{"Content-Encoding", "content-encoding", "cOnTeNt-EnCoDiNg", "Accept-Encoding", "accept-encoding", "AcCePt-EnCoDiNg"} {
		for _, value := range []string{"gzip", "identity", ""} {
			headers := map[string]interface{}{name: value}
			encoded, err := json.Marshal(headers)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []interface{}{headers, string(encoded), map[string]string{name: value}} {
				values := enabledTelemetryValues()
				values["headers"] = raw
				if _, err := parseProviderConfig(values, "orders", "1", "test"); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("保留请求头 %q 未被配置边界明确拒绝: %v", name, err)
				}
			}
		}
	}
}

func TestOTLPRejectsEffectiveEnvironmentAcceptEncoding(t *testing.T) {
	const global = "OTEL_EXPORTER_OTLP_HEADERS"
	const traces = "OTEL_EXPORTER_OTLP_TRACES_HEADERS"
	for _, tc := range []struct {
		name, globalValue, tracesValue, blockedVariable string
		localHeaders                                    map[string]string
	}{
		{name: "通用环境变量", globalValue: "X-Region=test, aCcEpT-EnCoDiNg =gzip", blockedVariable: global},
		{name: "Trace 环境变量", tracesValue: "accept-encoding=identity", blockedVariable: traces},
		{name: "Trace 环境覆盖通用环境", globalValue: "Accept-Encoding=gzip", tracesValue: "X-Region=test"},
		{name: "应用请求头覆盖环境", tracesValue: "Accept-Encoding=gzip", localHeaders: map[string]string{"X-Region": "test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(global, tc.globalValue)
			t.Setenv(traces, tc.tracesValue)
			app, configuration := telemetryProviderTestApp(t)
			values := enabledTelemetryValues()
			if tc.localHeaders != nil {
				values["headers"] = tc.localHeaders
			}
			if err := configuration.Set("telemetry", values); err != nil {
				t.Fatal(err)
			}
			provider := NewProvider()
			t.Cleanup(func() { _ = provider.Shutdown(app) })
			err := provider.Initialize(app)
			if tc.blockedVariable == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.blockedVariable) || !strings.Contains(err.Error(), "Accept-Encoding") {
				t.Fatalf("应明确拒绝生效环境变量中的 Accept-Encoding: %v", err)
			}
		})
	}
}

// TestOTLPExportPreservesBusinessHeaders 验证真实导出字节与编码头一致，同时允许认证和业务头。
func TestOTLPExportPreservesBusinessHeaders(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "none")
	response, err := proto.Marshal(&collectortrace.ExportTraceServiceResponse{
		PartialSuccess: &collectortrace.ExportTracePartialSuccess{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	encoder := gzip.NewWriter(&compressed)
	if _, err := encoder.Write(response); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	type exportResult struct {
		header http.Header
		data   []byte
		err    error
	}
	results := make(chan exportResult, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, err := io.ReadAll(request.Body)
		results <- exportResult{header: request.Header.Clone(), data: data, err: err}
		writer.Header().Set("Content-Type", "application/x-protobuf")
		writer.Header().Set("Content-Encoding", "gzip")
		if _, err := writer.Write(compressed.Bytes()); err != nil {
			t.Errorf("发送压缩 Collector 响应失败: %v", err)
		}
	}))
	defer collector.Close()
	app, configuration := telemetryProviderTestApp(t)
	values := enabledTelemetryValues()
	values["endpoint"] = collector.URL + "/v1/traces"
	const tenant = "tenant test\tregion"
	headers := map[string]string{"Authorization": "Bearer synthetic-test", "X-Tenant": tenant}
	values["headers"] = headers
	if err := configuration.Set("telemetry", values); err != nil {
		t.Fatal(err)
	}
	headers["X-Tenant"] = "mutated"
	snapshot := configuration.GetMap("telemetry")
	snapshot["headers"].(map[string]string)["Authorization"] = "mutated"
	provider := NewProvider()
	if err := provider.Initialize(app); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(app) })
	tracing, err := framework.ResolveServiceAs[*telemetry.Tracing](app, framework.ServiceTelemetry)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := tracing.StartServer(context.Background(), http.Header{}, "GET", "/orders/42", "api.example.com")
	telemetry.EndHTTPServer(ctx, http.StatusOK, nil)
	flushContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := provider.sdk.ForceFlush(flushContext); err != nil {
		t.Fatalf("刷新 Span 时应能解码 Collector gzip 响应: %v", err)
	}
	if err := provider.Shutdown(app); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		if result.err != nil || result.header.Get("Content-Encoding") != "" || result.header.Get("Content-Type") != "application/x-protobuf" {
			t.Fatalf("导出协议不一致: 请求头=%v，错误=%v", result.header, result.err)
		}
		if result.header.Get("Authorization") != "Bearer synthetic-test" || result.header.Get("X-Tenant") != tenant {
			t.Fatal("认证或业务请求头未被保留")
		}
		if result.header.Get("Accept-Encoding") != "gzip" {
			t.Fatal("HTTP Transport 应自动协商 gzip 响应")
		}
		if len(result.header.Values("Authorization")) != 1 || len(result.header.Values("X-Tenant")) != 1 {
			t.Fatal("实际导出请求应只有唯一认证和租户字段")
		}
		var request collectortrace.ExportTraceServiceRequest
		if err := proto.Unmarshal(result.data, &request); err != nil || len(request.ResourceSpans) == 0 {
			t.Fatalf("实际正文不是有效的非空 OTLP protobuf: %v", err)
		}
	default:
		t.Fatal("关闭 Provider 后未收到真实 Span 导出")
	}
}
