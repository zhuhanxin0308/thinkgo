package context_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuhanxin0308/thinkgo/v3/binding"
	fwcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
)

type requestJSONBenchmarkCheckout struct {
	Sequence   int64                      `json:"sequence"`
	CustomerID int64                      `json:"customer_id"`
	Items      []requestJSONBenchmarkItem `json:"items"`
}

type requestJSONBenchmarkItem struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

var requestJSONBenchmarkSink any

// BenchmarkRequestJSONPaths 同时衡量原文绑定与需要参数树的首次访问，防止只展示受益路径。
func BenchmarkRequestJSONPaths(b *testing.B) {
	const checkout = `{"sequence":123456789,"customer_id":12345,"items":[{"product_id":123,"quantity":2},{"product_id":456,"quantity":3}]}`
	const largeItemCount = 32
	large := `{"sequence":123456789,"customer_id":12345,"items":[` + strings.TrimSuffix(strings.Repeat(`{"product_id":123,"quantity":2},`, largeItemCount), ",") + `]}`
	for _, body := range []struct {
		name string
		text string
	}{
		{"Checkout", checkout},
		{"Large", large},
	} {
		b.Run(body.name, func(b *testing.B) {
			for _, operation := range []string{"StandaloneJson", "ValidateOnly", "ValidateJson", "ValidateThenFirstBind", "ParseOnly", "ParseJson", "ParseAll", "ParseSources", "FirstBind", "ParseThenFirstBind"} {
				b.Run(operation, func(b *testing.B) {
					template := httptest.NewRequest(http.MethodPost, "/json", nil)
					template.Header.Set("Content-Type", "application/json")
					template.ContentLength = int64(len(body.text))
					// 类型计划预热一次；每轮仍在一个新请求上进行首次 Bind。
					warm := requestJSONBenchmarkRequest(template, body.text)
					var target requestJSONBenchmarkCheckout
					if err := binding.Bind(warm, &target); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.SetBytes(int64(len(body.text)))
					b.ResetTimer()
					for b.Loop() {
						request := requestJSONBenchmarkRequest(template, body.text)
						if operation == "ValidateOnly" || operation == "ValidateJson" || operation == "ValidateThenFirstBind" {
							if err := request.ValidateBody(); err != nil {
								b.Fatal(err)
							}
						} else if operation != "FirstBind" && operation != "StandaloneJson" {
							if err := request.Parse(); err != nil {
								b.Fatal(err)
							}
						}
						switch operation {
						case "ParseOnly", "ValidateOnly":
							requestJSONBenchmarkSink = request
						case "StandaloneJson", "ParseJson", "ValidateJson":
							var target requestJSONBenchmarkCheckout
							if err := request.Json(&target); err != nil {
								b.Fatal(err)
							}
							requestJSONBenchmarkSink = target
						case "ParseAll":
							requestJSONBenchmarkSink = request.All()
						case "ParseSources":
							value, err := request.Sources()
							if err != nil {
								b.Fatal(err)
							}
							requestJSONBenchmarkSink = value
						case "FirstBind", "ParseThenFirstBind", "ValidateThenFirstBind":
							var target requestJSONBenchmarkCheckout
							if err := binding.Bind(request, &target); err != nil {
								b.Fatal(err)
							}
							requestJSONBenchmarkSink = target
						}
					}
				})
			}
		})
	}
}

func requestJSONBenchmarkRequest(template *http.Request, body string) *fwcontext.Request {
	raw := *template
	raw.Body = io.NopCloser(strings.NewReader(body))
	return fwcontext.MustNewRequest(&raw)
}

// TestRequestJSONLayoutMeasurement 保留请求结构尺寸的可比较证据，不绑定具体平台的布局常量。
func TestRequestJSONLayoutMeasurement(t *testing.T) {
	t.Logf("Request 结构尺寸: %d 字节", reflect.TypeFor[fwcontext.Request]().Size())
}

// TestRequestJSONBindingAllocationReduction 验证独立绑定省去实际不使用的值树分配。
func TestRequestJSONBindingAllocationReduction(t *testing.T) {
	const body = `{"sequence":123456789,"customer_id":12345,"items":[{"product_id":123,"quantity":2},{"product_id":456,"quantity":3}]}`
	const runs, minimumSavedAllocations = 100, 8
	template := httptest.NewRequest(http.MethodPost, "/json", nil)
	template.Header.Set("Content-Type", "application/json")
	template.ContentLength = int64(len(body))
	measure := func(parse bool) float64 {
		return testing.AllocsPerRun(runs, func() {
			request := requestJSONBenchmarkRequest(template, body)
			if parse {
				if err := request.Parse(); err != nil {
					panic(err)
				}
			}
			var target requestJSONBenchmarkCheckout
			if err := request.Json(&target); err != nil {
				panic(err)
			}
		})
	}
	standalone, parsed := measure(false), measure(true)
	t.Logf("独立绑定与先构树再绑定分配: standalone=%.0f parsed=%.0f", standalone, parsed)
	if standalone+minimumSavedAllocations > parsed {
		t.Fatalf("独立绑定未省去值树成本: standalone=%.0f parsed=%.0f", standalone, parsed)
	}
}
