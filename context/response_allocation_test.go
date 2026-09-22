package context

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

var responseAllocationSink *Response

// TestResponseHeaderOverwriteReusesStorage 约束热路径覆盖已有响应头时不再分配临时切片。
func TestResponseHeaderOverwriteReusesStorage(t *testing.T) {
	response := NewResponse()
	allocations := testing.AllocsPerRun(100, func() {
		response.Header("Content-Type", "application/json")
	})
	if allocations != 0 {
		t.Fatalf("覆盖已有响应头不应产生堆分配，实际为 %.2f", allocations)
	}
}

// TestResponseHeaderOverwritePreservesSnapshots 验证覆盖、追加、大小写和快照隔离保持同一契约。
func TestResponseHeaderOverwritePreservesSnapshots(t *testing.T) {
	response := NewResponse().AddHeader("X-Trace", "first").AddHeader("X-Trace", "second")
	before := response.Headers()
	response.Header("content-type", "application/json").Header("x-trace", "replacement")
	response.AddHeader("X-Trace", "last")
	if got := before.Values("X-Trace"); !reflect.DeepEqual(got, []string{"first", "second"}) {
		t.Fatalf("响应头快照被后续覆盖修改: %#v", got)
	}
	if before.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal("默认响应类型快照被后续修改")
	}
	if got := response.Headers().Values("X-Trace"); !reflect.DeepEqual(got, []string{"replacement", "last"}) {
		t.Fatalf("响应头覆盖和追加结果错误: %#v", got)
	}
	writer := httptest.NewRecorder()
	if err := response.Data([]byte(`{"ok":true}`)).Send(writer); err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusOK || writer.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("响应头发送结果错误: %#v", writer.Header())
	}
}

// TestResponseOptionsAndDataKeepOwnership 验证选项延迟构造不影响合并，实体仍保存调用时的副本。
func TestResponseOptionsAndDataKeepOwnership(t *testing.T) {
	response := NewResponse().Options(nil).Options(map[string]interface{}{"first": true})
	response.Options(map[string]interface{}{"first": false, "second": "value"})
	if !reflect.DeepEqual(response.options, map[string]interface{}{"first": false, "second": "value"}) {
		t.Fatalf("响应选项合并错误: %#v", response.options)
	}
	input := []byte("original")
	response.Data(input)
	input[0] = 'x'
	copyOfBody := response.GetBody()
	copyOfBody[0] = 'y'
	if response.GetContent() != "original" {
		t.Fatalf("响应实体被外部字节修改: %q", response.GetContent())
	}
}

// BenchmarkResponseConstruction 记录常见响应构造的分配成本，供优化前后逐项比较。
func BenchmarkResponseConstruction(b *testing.B) {
	b.Run("default", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse()
		}
	})
	b.Run("json_header", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse().Header("Content-Type", "application/json")
		}
	})
	b.Run("json_header_extra", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse().Header("Content-Type", "application/json").Header("X-Trace", "request-id")
		}
	})
	b.Run("json_header_security", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse().Header("Content-Type", "application/json").
				Header("X-Content-Type-Options", "nosniff").
				Header("X-Frame-Options", "DENY").
				Header("Referrer-Policy", "strict-origin-when-cross-origin").
				Header("Cache-Control", "private, no-store")
		}
	})
	b.Run("content", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse().Content("hello")
		}
	})
	b.Run("json_body", func(b *testing.B) {
		payload := struct {
			OK bool `json:"ok"`
		}{OK: true}
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewResponse().Json(payload)
		}
	})
	b.Run("committed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			responseAllocationSink = NewCommittedResponse(http.StatusAccepted)
		}
	})
}
