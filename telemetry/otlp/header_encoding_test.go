package otlp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	framework "github.com/zhuhanxin0308/thinkgo/v3"
	"github.com/zhuhanxin0308/thinkgo/v3/telemetry"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestOTLPRejectsContentEncodingHeader 配置不得伪造由导出器控制的实际传输编码。
func TestOTLPRejectsContentEncodingHeader(t *testing.T) {
	for _, name := range []string{"Content-Encoding", "content-encoding", "CONTENT-ENCODING", "cOnTeNt-EnCoDiNg"} {
		for _, value := range []string{"gzip", "identity", ""} {
			headers := map[string]interface{}{name: value}
			encoded, err := json.Marshal(headers)
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range []interface{}{headers, string(encoded)} {
				values := enabledTelemetryValues()
				values["headers"] = raw
				if _, err := parseProviderConfig(values, "orders", "1", "test"); err == nil || !strings.Contains(err.Error(), name) {
					t.Fatalf("保留请求头 %q 未被配置边界明确拒绝: %v", name, err)
				}
			}
		}
	}
}

// TestOTLPExportPreservesBusinessHeaders 验证真实导出字节与编码头一致，同时允许认证和业务头。
func TestOTLPExportPreservesBusinessHeaders(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_COMPRESSION", "none")
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
		writer.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	app, configuration := telemetryProviderTestApp(t)
	values := enabledTelemetryValues()
	values["endpoint"] = collector.URL + "/v1/traces"
	values["headers"] = map[string]interface{}{"Authorization": "Bearer synthetic-test", "X-Tenant": "tenant-test"}
	configuration.Set("telemetry", values)
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
	if err := provider.Shutdown(app); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-results:
		if result.err != nil || result.header.Get("Content-Encoding") != "" || result.header.Get("Content-Type") != "application/x-protobuf" {
			t.Fatalf("导出协议不一致: 请求头=%v，错误=%v", result.header, result.err)
		}
		if result.header.Get("Authorization") != "Bearer synthetic-test" || result.header.Get("X-Tenant") != "tenant-test" {
			t.Fatal("认证或业务请求头未被保留")
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
