package context

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

var requestMIMEBenchmarkSink *Request
var requestMIMETypeBenchmarkSink string

// TestRequestMIMEOverrideIsolationAndOrder 保证默认匹配顺序和请求局部修改不会相互污染。
func TestRequestMIMEOverrideIsolationAndOrder(t *testing.T) {
	firstRaw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	firstRaw.Header.Set("Accept", "text/html, application/json")
	secondRaw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	secondRaw.Header.Set("Accept", "application/json")
	first := newRequestForTest(t, firstRaw)
	second := newRequestForTest(t, secondRaw)
	if first.Type() != "json" {
		t.Fatalf("匹配必须遵循默认类型表顺序，实际为 %q", first.Type())
	}
	first.MimeType("json", "application/custom")
	firstRaw.Header.Set("Accept", "application/json")
	if first.Type() != "" || second.Type() != "json" {
		t.Fatalf("覆盖应仅改变当前请求: first=%q second=%q", first.Type(), second.Type())
	}
	first.MimeType("problem", "application/problem+json")
	firstRaw.Header.Set("Accept", "application/problem+json, application/custom")
	if first.Type() != "json" {
		t.Fatalf("覆盖后的原有类型仍应排在追加类型前面，实际为 %q", first.Type())
	}
	first.MimeType("json", "")
	if first.Type() != "problem" {
		t.Fatalf("空定义应移除当前请求的原有匹配，实际为 %q", first.Type())
	}
	first.MimeType("  ", "application/json")
	firstRaw.Header.Set("Accept", "application/json")
	if first.Type() != "" || second.Type() != "json" {
		t.Fatalf("无效类型名不得恢复覆盖或污染默认表: first=%q second=%q", first.Type(), second.Type())
	}
	third := newRequestForTest(t, secondRaw)
	if third.Type() != "json" {
		t.Fatalf("后续请求必须继续使用原始默认表，实际为 %q", third.Type())
	}
}

// TestRequestMIMEConcurrentReadsAndOverrides 验证同一请求并发读写和跨请求默认表隔离。
func TestRequestMIMEConcurrentReadsAndOverrides(t *testing.T) {
	const workers, iterations = 8, 250
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("Accept", "application/custom")
	request := newRequestForTest(t, raw).MimeType("json", "application/custom")
	defaultRaw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	defaultRaw.Header.Set("Accept", "application/json")
	unchanged := newRequestForTest(t, defaultRaw)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Go(func() {
			for index := 0; index < iterations; index++ {
				if index%2 == 0 {
					request.MimeType("json", "application/custom, text/json")
				} else {
					request.MimeType("json", "application/custom")
				}
				if request.Type() != "json" || unchanged.Type() != "json" {
					t.Error("并发修改改变了匹配结果或其他请求的默认类型")
					return
				}
			}
		})
	}
	group.Wait()
}

// TestRequestCreationTimePrecedesOptions 保证请求时间在选项执行前确定且后续读取不改变。
func TestRequestCreationTimePrecedesOptions(t *testing.T) {
	const optionDelay = 5 * time.Millisecond
	var optionStarted time.Time
	request, err := NewRequest(nil, func(current *Request) error {
		optionStarted = time.Now()
		time.Sleep(optionDelay)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	created := request.Time(true).(float64)
	latestAllowed := float64(optionStarted.UnixNano()) / float64(time.Second)
	if created <= 0 || created > latestAllowed {
		t.Fatalf("请求创建时间必须早于选项执行: created=%v option=%v", created, latestAllowed)
	}
	if request.Time(true) != created {
		t.Fatal("请求创建时间不得随访问重新生成")
	}
}

// TestRequestDefaultMIMEAllocationBudget 保证普通请求构造只分配包装器，兼容状态按需创建。
func TestRequestDefaultMIMEAllocationBudget(t *testing.T) {
	const runs, constructionAllocationBudget = 100, 1
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("Accept", "application/json")
	allocations := testing.AllocsPerRun(runs, func() {
		var err error
		requestMIMEBenchmarkSink, err = NewRequest(raw)
		if err != nil {
			panic(err)
		}
	})
	if allocations > constructionAllocationBudget {
		t.Errorf("默认 MIME 不应增加请求构造分配: got=%.0f limit=%d", allocations, constructionAllocationBudget)
	}
	request := newRequestForTest(t, raw)
	allocations = testing.AllocsPerRun(runs, func() {
		requestMIMETypeBenchmarkSink = request.Type()
	})
	if allocations != 0 {
		t.Errorf("只读 MIME 类型匹配不应复制整份类型表: allocations=%.0f", allocations)
	}
}

// BenchmarkRequestMIMEAllocation 分离请求构造和类型读取，排除 httptest 创建成本。
func BenchmarkRequestMIMEAllocation(b *testing.B) {
	raw := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	raw.Header.Set("Accept", "application/json")
	b.Run("construction", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			var err error
			requestMIMEBenchmarkSink, err = NewRequest(raw)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("default_type", func(b *testing.B) {
		request, err := NewRequest(raw)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			requestMIMETypeBenchmarkSink = request.Type()
		}
	})
}
