package exception

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestWithDataPreservesTypedNestedSnapshot(t *testing.T) {
	type details struct {
		Stock  map[string]int
		Labels []string
	}
	for _, test := range []struct {
		name  string
		build func(map[string]any) (map[string]any, error)
	}{
		{"业务异常", func(input map[string]any) (map[string]any, error) {
			result := NewBusinessException(1001, "库存不足").WithData(input)
			return result.Data, result
		}},
		{"HTTP 异常", func(input map[string]any) (map[string]any, error) {
			result := NewHttpException(http.StatusConflict, "库存不足").WithData(input)
			return result.Data, result
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := &details{Stock: map[string]int{"MixedCase": 7}, Labels: []string{"first"}}
			input := map[string]any{"Detail": value}
			snapshot, recovered := test.build(input)
			value.Stock["MixedCase"] = 0
			value.Labels[0] = "updated"
			input["another"] = true
			copied, ok := snapshot["Detail"].(*details)
			if !ok || copied == value || copied.Stock["MixedCase"] != 7 || copied.Labels[0] != "first" || len(snapshot) != 1 {
				t.Fatalf("异常附加数据没有保持原类型和隔离: %#v", snapshot)
			}
			request := httptest.NewRequest(http.MethodGet, "https://example.test", nil)
			request.Header.Set("Accept", "application/json")
			writer := httptest.NewRecorder()
			if err := (&Handle{}).Render(writer, request, recovered); err != nil {
				t.Fatal(err)
			}
			var output struct {
				Data map[string]details `json:"data"`
			}
			if err := json.Unmarshal(writer.Body.Bytes(), &output); err != nil ||
				output.Data["Detail"].Stock["MixedCase"] != 7 ||
				len(output.Data["Detail"].Labels) != 1 || output.Data["Detail"].Labels[0] != "first" {
				t.Fatalf("渲染后的异常数据变动: %s, %v", writer.Body.String(), err)
			}
		})
	}
}

func TestWithDataRejectsUncopyableStateBeforeRendering(t *testing.T) {
	var lock sync.Mutex
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, value := range []any{&lock, cycle, make(chan int)} {
		data := map[string]any{"invalid": value}
		for _, recovered := range []error{
			NewBusinessException(1001, "无法执行").WithData(data),
			NewHttpException(http.StatusBadRequest, "无法执行").WithData(data),
		} {
			request := httptest.NewRequest(http.MethodGet, "https://example.test", nil)
			request.Header.Set("Accept", "application/json")
			writer := httptest.NewRecorder()
			if err := (&Handle{}).Render(writer, request, recovered); !errors.Is(err, ErrExceptionDataSnapshot) || writer.Body.Len() != 0 {
				t.Errorf("不可安全复制的数据应在渲染时返回错误: %T, %v", value, err)
			}
		}
	}
}

func TestWithDataCanReplaceFailedSnapshot(t *testing.T) {
	recovered := NewBusinessException(1001, "无法执行").
		WithData(map[string]any{"invalid": make(chan int)}).
		WithData(map[string]any{"retry": true})
	request := httptest.NewRequest(http.MethodGet, "https://example.test", nil)
	request.Header.Set("Accept", "application/json")
	writer := httptest.NewRecorder()
	if err := (&Handle{}).Render(writer, request, recovered); err != nil || writer.Code != http.StatusBadRequest {
		t.Fatalf("后一次成功快照应替换旧错误: %d, %v", writer.Code, err)
	}
	var response struct {
		Data map[string]bool `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil || len(response.Data) != 1 || !response.Data["retry"] {
		t.Fatalf("异常应只输出新一轮快照: %s, %v", writer.Body.String(), err)
	}
}
