package main

import (
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNewLoadRequestCoversFullBusinessPayload 验证全特性业务请求会发送真实 JSON 输入和协商语言。
func TestNewLoadRequestCoversFullBusinessPayload(t *testing.T) {
	request, err := newLoadRequest(http.MethodPost, "http://127.0.0.1/bench/full/business?product_id=7")
	if err != nil {
		t.Fatalf("创建全特性业务请求失败: %v", err)
	}
	if request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("全特性业务请求 Content-Type 错误: %q", request.Header.Get("Content-Type"))
	}
	if request.Header.Get("Accept-Language") != "en-us,en;q=0.8" {
		t.Fatalf("全特性业务请求语言协商头错误: %q", request.Header.Get("Accept-Language"))
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("读取全特性业务请求体失败: %v", err)
	}
	var input map[string]interface{}
	if err = json.Unmarshal(body, &input); err != nil {
		t.Fatalf("全特性业务请求体不是有效 JSON: %v", err)
	}
	if input["email"] != "bench@example.com" || input["name"] != "Ada" || input["quantity"] != float64(1) {
		t.Fatalf("全特性业务请求体未覆盖真实输入: %#v", input)
	}
}

// TestScenarioIncludesFullFeatureWorkloads 验证全量场景会同时覆盖业务、缓存命中、缓存未命中和验证失败。
func TestScenarioIncludesFullFeatureWorkloads(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	seen := make(map[string]bool)
	for index := 0; index < 4000; index++ {
		method, url := scenario("http://127.0.0.1:18081", rng, "full")
		switch {
		case strings.Contains(url, "/bench/full/business") && method == http.MethodPost:
			if strings.Contains(url, "invalid=1") {
				seen["invalid"] = true
			} else {
				seen["business"] = true
			}
		case strings.Contains(url, "/bench/full/cache") && strings.Contains(url, "bench-full-cache-"):
			seen["cache-hit"] = true
		case strings.Contains(url, "/bench/full/cache") && strings.Contains(url, "bench-full-miss-"):
			seen["cache-miss"] = true
		}
	}
	for _, workload := range []string{"business", "cache-hit", "cache-miss", "invalid"} {
		if !seen[workload] {
			t.Fatalf("全量场景未覆盖 %s 工作负载: %#v", workload, seen)
		}
	}
}

// TestScenarioFullBusinessUsesStableSessionValue 验证稳定业务场景可用于测量 Session 幂等写入优化。
func TestScenarioFullBusinessUsesStableSessionValue(t *testing.T) {
	method, url := scenario("http://127.0.0.1:18081", rand.New(rand.NewPCG(1, 2)), "full-business")
	if method != http.MethodPost || url != "http://127.0.0.1:18081/bench/full/business?product_id=7" {
		t.Fatalf("稳定业务场景请求错误: method=%s url=%s", method, url)
	}
}

// TestFullBenchmarkValidationRulesUseFrameworkValidator 验证基准使用框架验证器而不是客户端假数据。
func TestFullBenchmarkValidationRulesUseFrameworkValidator(t *testing.T) {
	controller := &fullBenchmarkController{}
	valid, err := controller.Validate(map[string]interface{}{
		"email": "bench@example.com", "name": "Ada", "quantity": "1",
	}, fullBenchmarkValidationRules)
	if err != nil || !valid.Valid() {
		t.Fatalf("有效业务输入应通过框架验证器: valid=%v err=%v", valid.Valid(), err)
	}
	invalid, err := controller.Validate(map[string]interface{}{
		"email": "invalid-email", "name": "A", "quantity": "0",
	}, fullBenchmarkValidationRules)
	if err != nil || invalid.Valid() {
		t.Fatalf("无效业务输入应被框架验证器拒绝: valid=%v err=%v", invalid.Valid(), err)
	}
}

// TestFullBenchmarkNilControllerErrorDoesNotPanic 验证异常边界不会因空控制器再次触发 panic。
func TestFullBenchmarkNilControllerErrorDoesNotPanic(t *testing.T) {
	response := fullBenchmarkError(nil, "unavailable", http.StatusInternalServerError)
	if response == nil || response.GetStatus() != http.StatusInternalServerError {
		t.Fatalf("空控制器错误响应不稳定: %#v", response)
	}
}

// TestFullProfileCaptureWritesRequestedProfiles 验证全特性剖析开关会实际生成四类运行时剖析文件。
func TestFullProfileCaptureWritesRequestedProfiles(t *testing.T) {
	directory := t.TempDir()
	paths := map[string]string{
		"cpu":   filepath.Join(directory, "cpu.pprof"),
		"heap":  filepath.Join(directory, "heap.pprof"),
		"mutex": filepath.Join(directory, "mutex.pprof"),
		"block": filepath.Join(directory, "block.pprof"),
	}
	capture, err := startFullProfileCapture(fullProfileOptions{
		cpuPath: paths["cpu"], heapPath: paths["heap"], mutexPath: paths["mutex"], blockPath: paths["block"], duration: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("启动全特性剖析失败: %v", err)
	}
	capture.stop()
	for name, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("%s 剖析文件未生成: %v", name, statErr)
		}
		if info.Size() == 0 {
			t.Fatalf("%s 剖析文件为空", name)
		}
	}
}
